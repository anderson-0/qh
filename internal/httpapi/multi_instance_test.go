package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

// freshPostgres starts a Postgres container and returns the DSN of an empty
// database on it.
func freshPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("qh"), tcpostgres.WithPassword("store-pw-123"), tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	admin, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE DATABASE qh_multi"); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/qh_multi"
	return u.String()
}

// newInstance builds one complete server (its own service, pools and catalog
// cache) on st, as a separate qh serve process would.
func newInstance(t *testing.T, st store.Store, keys *secret.Keyring) *env {
	t.Helper()
	pools := pool.New(core.OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	svc := core.New(st, keys, pools, catalog.New(10*time.Minute))
	// Cleanups run last in, first out: the context ends before the caller's
	// store cleanup closes the store.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc.Start(ctx)
	t.Cleanup(svc.Close)
	e := &env{svc: svc, st: st}
	e.srv = httptest.NewServer(New(Config{Service: svc, Store: st, Auth: auth.NewAuthenticator(st), Version: "test"}))
	t.Cleanup(e.srv.Close)
	return e
}

// eventually polls until fn reports true, failing after d.
func eventually(t *testing.T, d time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMultiInstanceSharedStore(t *testing.T) {
	dsn := freshPostgres(t)
	ctx := context.Background()

	// Each instance opens its own store handle, as separate processes do:
	// a handle skips the invalidations it sent itself.
	open := func() store.Store {
		st, err := store.Open(ctx, dsn, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	stA, stB := open(), open()

	raw := make([]byte, secret.KeySize)
	rand.Read(raw)
	keys, err := secret.NewKeyring(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, b := newInstance(t, stA, keys), newInstance(t, stB, keys)

	// 1. Bootstrap: instances starting together create one admin key.
	admin := "qh_abcdefgh_" + strings.Repeat("a1B2c3D4", 5)
	var wg sync.WaitGroup
	created := make([]bool, 2)
	errs := make([]error, 2)
	for i, st := range []store.Store{stA, stB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created[i], errs[i] = auth.Bootstrap(ctx, st, admin)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("bootstrap: %v, %v", errs[0], errs[1])
	}
	if created[0] == created[1] {
		t.Fatalf("bootstrap created = %v; exactly one instance should create the key", created)
	}
	if all, err := stA.ListAllKeys(ctx); err != nil || len(all) != 1 {
		t.Fatalf("keys after bootstrap: %d, %v", len(all), err)
	}
	// Admin keys name their workspace in a header.
	adm := func(e *env, method, path string, body any) reply {
		t.Helper()
		return e.do(t, method, path, admin, body, "X-QH-Workspace", store.DefaultWorkspace)
	}

	for name, e := range map[string]*env{"A": a, "B": b} {
		if r := adm(e, "GET", "/api/v1/connections", nil); r.status != 200 {
			t.Fatalf("admin key on %s: %d %s", name, r.status, r.body)
		}
	}

	// 2. A connection created through A is queryable through B.
	db := testutil.SQLiteFixture(t)
	if r := adm(a, "POST", "/api/v1/connections", map[string]any{"name": "shop", "engine": "sqlite", "dsn": db}); r.status != 201 {
		t.Fatalf("create on A: %d %s", r.status, r.body)
	}
	const count = "SELECT count(*) AS n FROM orders"
	if r := adm(b, "POST", "/api/v1/connections/shop/query", map[string]any{"query": count}); r.status != 200 || !strings.HasPrefix(r.body, "n\n250\n") {
		t.Fatalf("query on B: %d %q", r.status, r.body)
	}

	// 3. A key revoked through A is refused by B at once.
	var k keyJSON
	r := adm(a, "POST", "/api/v1/keys", map[string]any{"workspace": store.DefaultWorkspace, "scopes": []string{auth.ScopeQuery}, "name": "reader"})
	r.json(t, &k)
	if r.status != 201 || k.Key == "" || k.ID == "" {
		t.Fatalf("create key on A: %d %s", r.status, r.body)
	}
	if r := b.do(t, "POST", "/api/v1/connections/shop/query", k.Key, map[string]any{"query": count}); r.status != 200 {
		t.Fatalf("new key on B: %d %s", r.status, r.body)
	}
	if r := adm(a, "DELETE", "/api/v1/keys/"+k.ID, nil); r.status != 204 && r.status != 200 {
		t.Fatalf("revoke on A: %d %s", r.status, r.body)
	}
	expectProblem(t, b.do(t, "POST", "/api/v1/connections/shop/query", k.Key, map[string]any{"query": count}), 401, "auth", "")

	// 4. A schema change made through A drops B's cached schema.
	if r := adm(a, "PATCH", "/api/v1/connections/shop", map[string]any{"mode": "rw"}); r.status != 200 {
		t.Fatalf("make rw on A: %d %s", r.status, r.body)
	}
	// The text form echoes the pattern when nothing matches, so read JSON.
	hasTable := func() bool {
		var s struct {
			Objects []struct{ Name string } `json:"objects"`
		}
		r := b.do(t, "GET", "/api/v1/connections/shop/schema?pattern=multi_t", admin, nil, "X-QH-Workspace", store.DefaultWorkspace, "Accept", "application/json")
		r.json(t, &s)
		return r.status == 200 && len(s.Objects) == 1 && s.Objects[0].Name == "multi_t"
	}
	if hasTable() {
		t.Fatal("B lists multi_t before it exists")
	}
	if r := adm(a, "POST", "/api/v1/connections/shop/query", map[string]any{"query": "CREATE TABLE multi_t (x int)"}); r.status != 200 {
		t.Fatalf("DDL on A: %d %s", r.status, r.body)
	}
	eventually(t, 5*time.Second, "B to list multi_t", hasTable)

	// 5. The query log interleaves both instances' calls. Entries are written
	// in the background, so wait for each before sending the next.
	logged := func(e *env) []queryLogJSON {
		var out []queryLogJSON
		adm(e, "GET", "/api/v1/query-log?connection=shop&limit=100", nil).json(t, &out)
		var mine []queryLogJSON
		for _, l := range out {
			if strings.HasPrefix(l.Query, "SELECT 100") {
				mine = append(mine, l)
			}
		}
		return mine
	}
	for i, e := range []*env{a, b, a} {
		q := fmt.Sprintf("SELECT 100%d AS n", i+1)
		if r := adm(e, "POST", "/api/v1/connections/shop/query", map[string]any{"query": q}); r.status != 200 {
			t.Fatalf("log query %d: %d %s", i+1, r.status, r.body)
		}
		eventually(t, 5*time.Second, "query "+q+" to be logged", func() bool { return len(logged(a)) == i+1 })
	}
	got := logged(a)
	if len(got) != 3 || got[0].Query != "SELECT 1003 AS n" || got[1].Query != "SELECT 1002 AS n" || got[2].Query != "SELECT 1001 AS n" ||
		got[0].ID <= got[1].ID || got[1].ID <= got[2].ID {
		t.Fatalf("query log, newest first: %+v", got)
	}
	if logB := logged(b); len(logB) != 3 || logB[0].ID != got[0].ID {
		t.Fatalf("B sees a different log: %+v", logB)
	}
}
