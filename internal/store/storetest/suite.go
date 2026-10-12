// Package storetest is the conformance suite every store.Store implementation
// must pass.
package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/store"
)

// Run runs every conformance case against stores made by open. open returns
// a fresh, empty, migrated store and registers its own cleanup.
func Run(t *testing.T, open func(t *testing.T) store.Store) {
	for name, fn := range map[string]func(*testing.T, store.Store){
		"CreateGetList":                            testCreateGetList,
		"DuplicateAndMissing":                      testDuplicateAndMissing,
		"UpdateVersioning":                         testUpdateVersioning,
		"StaleUpdateDoesNotHitRecreatedConnection": testStaleUpdateDoesNotHitRecreatedConnection,
		"Workspaces":                               testWorkspaces,
		"Keys":                                     testKeys,
		"QueryLog":                                 testQueryLog,
		"ConnectionTLSSummary":                     testConnectionTLSSummary,
		"ConnectionSSHAndRefsSummaries":            testConnectionSSHAndRefsSummaries,
		"ResealConnections":                        testResealConnections,
	} {
		t.Run(name, func(t *testing.T) { fn(t, open(t)) })
	}
}

// sameTime reports whether a and b are equal at the given precision, so stores
// that keep less than nanosecond precision still conform.
func sameTime(a, b time.Time, precision time.Duration) bool {
	return a.Truncate(precision).Equal(b.Truncate(precision))
}

func sample(name string) *store.Connection {
	return &store.Connection{
		ID: store.NewID(), WorkspaceID: store.DefaultWorkspace, Name: name, Engine: "postgres",
		Secret: []byte{1, 2, 3}, SecretKeyID: 7, Host: "db.local", Port: 5432,
		Database: "shop", Username: "reader", Description: "Shop DB", Mode: "ro",
		MaxRows: 100, TimeoutMS: 30000, PoolSize: 5, AllowedNamespaces: []string{"public"},
	}
}

func testCreateGetList(t *testing.T, s store.Store) {
	ctx := context.Background()
	c := sample("shop")
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || c.CreatedAt.IsZero() {
		t.Fatalf("after create: version %d created %v", c.Version, c.CreatedAt)
	}
	got, err := s.GetConnection(ctx, store.DefaultWorkspace, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if !sameTime(got.CreatedAt, c.CreatedAt, time.Microsecond) || !sameTime(got.UpdatedAt, c.UpdatedAt, time.Microsecond) {
		t.Fatalf("times: got %v/%v want %v/%v", got.CreatedAt, got.UpdatedAt, c.CreatedAt, c.UpdatedAt)
	}
	got.CreatedAt, got.UpdatedAt = c.CreatedAt, c.UpdatedAt
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("got %#v\nwant %#v", got, c)
	}
	if err := s.CreateConnection(ctx, sample("analytics")); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConnections(ctx, store.DefaultWorkspace)
	if err != nil || len(list) != 2 || list[0].Name != "analytics" || list[1].Name != "shop" {
		t.Fatalf("list = %v, %v", list, err)
	}
}

func testDuplicateAndMissing(t *testing.T, s store.Store) {
	ctx := context.Background()
	_ = s.CreateConnection(ctx, sample("shop"))
	if err := s.CreateConnection(ctx, sample("shop")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate err = %v", err)
	}
	if _, err := s.GetConnection(ctx, store.DefaultWorkspace, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing err = %v", err)
	}
	if err := s.DeleteConnection(ctx, store.DefaultWorkspace, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
	if _, err := s.GetConnection(ctx, "other-workspace", "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other workspace err = %v", err)
	}
}

func testUpdateVersioning(t *testing.T, s store.Store) {
	ctx := context.Background()
	c := sample("shop")
	_ = s.CreateConnection(ctx, c)
	stale := *c
	c.MaxRows = 500
	if err := s.UpdateConnection(ctx, c); err != nil || c.Version != 2 {
		t.Fatalf("update err %v version %d", err, c.Version)
	}
	got, _ := s.GetConnection(ctx, store.DefaultWorkspace, "shop")
	if got.MaxRows != 500 || got.Version != 2 {
		t.Fatalf("stored %d v%d", got.MaxRows, got.Version)
	}
	if err := s.UpdateConnection(ctx, &stale); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	missing := sample("nope")
	missing.Version = 1
	if err := s.UpdateConnection(ctx, missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing update err = %v", err)
	}
}

func testStaleUpdateDoesNotHitRecreatedConnection(t *testing.T, s store.Store) {
	ctx := context.Background()
	old := sample("shop")
	_ = s.CreateConnection(ctx, old)
	stale := *old
	if err := s.DeleteConnection(ctx, store.DefaultWorkspace, "shop"); err != nil {
		t.Fatal(err)
	}
	fresh := sample("shop")
	fresh.MaxRows = 77
	if err := s.CreateConnection(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	stale.MaxRows = 999
	if err := s.UpdateConnection(ctx, &stale); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	got, err := s.GetConnection(ctx, store.DefaultWorkspace, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != fresh.ID || got.MaxRows != 77 || got.Version != 1 {
		t.Fatalf("recreated row changed: id %s max %d v%d", got.ID, got.MaxRows, got.Version)
	}
}

func testWorkspaces(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWorkspace(ctx, "team"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	list, err := s.ListWorkspaces(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "default" || list[1].ID != "team" || list[1].CreatedAt.IsZero() {
		t.Fatalf("list: %+v %v", list, err)
	}
	c := sample("shop")
	c.WorkspaceID = "team"
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	k := &store.APIKey{ID: store.NewID(), WorkspaceID: "team", Prefix: "abcd1234", Hash: []byte{1}, Scopes: []string{"query"}}
	if err := s.CreateKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnection(ctx, "team", "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("connection survived its workspace: %v", err)
	}
	if _, err := s.GetKeyByPrefix(ctx, "abcd1234"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("key survived its workspace: %v", err)
	}
	if err := s.DeleteWorkspace(ctx, "team"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func testKeys(t *testing.T, s store.Store) {
	ctx := context.Background()
	if n, err := s.CountKeys(ctx); err != nil || n != 0 {
		t.Fatalf("count = %d %v", n, err)
	}
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	admin := &store.APIKey{ID: store.NewID(), Prefix: "admin001", Hash: []byte{9, 9}, Scopes: []string{"admin"}, Name: "bootstrap"}
	ws := &store.APIKey{ID: store.NewID(), WorkspaceID: "default", Prefix: "work0001", Hash: []byte{1, 2}, Scopes: []string{"query", "manage"}, Name: "ci", ExpiresAt: &exp}
	for _, k := range []*store.APIKey{admin, ws} {
		if err := s.CreateKey(ctx, k); err != nil {
			t.Fatal(err)
		}
		if k.CreatedAt.IsZero() {
			t.Fatal("CreatedAt not set")
		}
	}
	dup := *ws
	dup.ID = store.NewID()
	if err := s.CreateKey(ctx, &dup); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate prefix: %v", err)
	}
	orphan := &store.APIKey{ID: store.NewID(), WorkspaceID: "nope", Prefix: "orphan01", Hash: []byte{1}, Scopes: []string{"query"}}
	if err := s.CreateKey(ctx, orphan); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
	got, err := s.GetKeyByPrefix(ctx, "work0001")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "default" || !reflect.DeepEqual(got.Scopes, []string{"query", "manage"}) || got.Name != "ci" ||
		got.ExpiresAt == nil || !sameTime(*got.ExpiresAt, exp, time.Microsecond) || got.LastUsedAt != nil || !reflect.DeepEqual(got.Hash, []byte{1, 2}) {
		t.Fatalf("got %+v", got)
	}
	used := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if err := s.TouchKey(ctx, got.ID, used); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetKeyByPrefix(ctx, "work0001")
	if got.LastUsedAt == nil || !sameTime(*got.LastUsedAt, used, time.Microsecond) {
		t.Fatalf("last used %v", got.LastUsedAt)
	}
	if l, _ := s.ListKeys(ctx, ""); len(l) != 1 || l[0].Prefix != "admin001" || l[0].WorkspaceID != "" {
		t.Fatalf("admin keys %+v", l)
	}
	if l, _ := s.ListKeys(ctx, "default"); len(l) != 1 || l[0].Prefix != "work0001" {
		t.Fatalf("workspace keys %+v", l)
	}
	if l, _ := s.ListAllKeys(ctx); len(l) != 2 {
		t.Fatalf("all keys %+v", l)
	}
	if n, _ := s.CountKeys(ctx); n != 2 {
		t.Fatalf("count %d", n)
	}
	if err := s.DeleteKey(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(ctx, admin.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func testQueryLog(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	add := func(ws, conn string, at time.Time, cat string) {
		t.Helper()
		e := &store.QueryLogEntry{WorkspaceID: ws, ConnectionID: "id-" + conn, ConnectionName: conn, KeyID: "k1", Tool: "query",
			Query: "SELECT 1", RowCount: 1, DurationMS: 3, ErrorCategory: cat, At: at}
		if err := s.AppendQueryLog(ctx, e); err != nil || e.ID == 0 {
			t.Fatalf("append: %v id %d", err, e.ID)
		}
	}
	add(store.DefaultWorkspace, "shop", base, "")
	add(store.DefaultWorkspace, "shop", base.Add(time.Hour), "read_only")
	add(store.DefaultWorkspace, "crm", base.Add(2*time.Hour), "")
	add("team", "shop", base.Add(3*time.Hour), "")

	all, err := s.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace})
	if err != nil || len(all) != 3 || all[0].ConnectionName != "crm" || !sameTime(all[2].At, base, time.Millisecond) {
		t.Fatalf("list: %v %+v", err, all)
	}
	if all[1].ErrorCategory != "read_only" || all[1].KeyID != "k1" || all[1].Tool != "query" || all[1].RowCount != 1 || all[1].DurationMS != 3 {
		t.Fatalf("fields: %+v", all[1])
	}
	shop, _ := s.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace, ConnectionName: "shop", Limit: 1})
	if len(shop) != 1 || !sameTime(shop[0].At, base.Add(time.Hour), time.Millisecond) {
		t.Fatalf("filter + limit: %+v", shop)
	}
	since, _ := s.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace, Since: base.Add(time.Hour)})
	if len(since) != 2 {
		t.Fatalf("since: %+v", since)
	}
	if n, err := s.PruneQueryLog(ctx, base.Add(90*time.Minute)); err != nil || n != 2 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if rest, _ := s.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace}); len(rest) != 1 {
		t.Fatalf("after prune: %+v", rest)
	}
	if err := s.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if team, _ := s.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: "team"}); len(team) != 0 {
		t.Fatalf("deleting a workspace kept its log: %+v", team)
	}
}

func testConnectionTLSSummary(t *testing.T, s store.Store) {
	ctx := context.Background()
	c := sample("tls")
	c.TLS = `{"mode":"verify-full","ca":true}`
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConnection(ctx, store.DefaultWorkspace, "tls")
	if got.TLS != c.TLS {
		t.Fatalf("tls %q", got.TLS)
	}
	got.TLS = ""
	if err := s.UpdateConnection(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetConnection(ctx, store.DefaultWorkspace, "tls"); again.TLS != "" {
		t.Fatalf("cleared tls %q", again.TLS)
	}
}

func testConnectionSSHAndRefsSummaries(t *testing.T, s store.Store) {
	ctx := context.Background()
	c := sample("ssh")
	c.SSH = "{\"host\":\"b\"}"
	c.Refs = "{\"dsn_ref\":\"env:X\"}"
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConnection(ctx, store.DefaultWorkspace, "ssh")
	if got.SSH != c.SSH || got.Refs != c.Refs {
		t.Fatalf("ssh %q refs %q", got.SSH, got.Refs)
	}
	plain := sample("plain")
	if err := s.CreateConnection(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetConnection(ctx, store.DefaultWorkspace, "plain"); p.SSH != "" || p.Refs != "" {
		t.Fatalf("empty values came back as %q %q", p.SSH, p.Refs)
	}
	got.SSH, got.Refs = "", ""
	if err := s.UpdateConnection(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetConnection(ctx, store.DefaultWorkspace, "ssh"); again.SSH != "" || again.Refs != "" {
		t.Fatalf("cleared %q %q", again.SSH, again.Refs)
	}
}

func testResealConnections(t *testing.T, s store.Store) {
	ctx := context.Background()
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	a, b, c := sample("a"), sample("b"), sample("c")
	c.WorkspaceID = "team"
	for _, x := range []*store.Connection{a, b, c} {
		if err := s.CreateConnection(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	n, err := s.ResealConnections(ctx, func(x *store.Connection) ([]byte, uint32, error) {
		seen++
		if x.Name == "b" {
			return nil, 0, nil // unchanged
		}
		return []byte("new-" + x.Name), 9, nil
	})
	if err != nil || n != 2 || seen != 3 {
		t.Fatalf("reseal: n=%d seen=%d %v", n, seen, err)
	}
	got, _ := s.GetConnection(ctx, "team", "c")
	if string(got.Secret) != "new-c" || got.SecretKeyID != 9 || got.Version != 1 {
		t.Fatalf("c: %q %d v%d", got.Secret, got.SecretKeyID, got.Version)
	}
	if got, _ := s.GetConnection(ctx, store.DefaultWorkspace, "b"); string(got.Secret) != "\x01\x02\x03" || got.SecretKeyID != 7 {
		t.Fatalf("b changed: %q %d", got.Secret, got.SecretKeyID)
	}

	boom := errors.New("boom")
	_, err = s.ResealConnections(ctx, func(x *store.Connection) ([]byte, uint32, error) {
		if x.Name == "c" {
			return nil, 0, boom
		}
		return []byte("second"), 10, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error: %v", err)
	}
	if got, _ := s.GetConnection(ctx, store.DefaultWorkspace, "a"); string(got.Secret) != "new-a" {
		t.Fatalf("failed reseal changed a: %q", got.Secret)
	}
}
