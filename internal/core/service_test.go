package core

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

const ws = store.DefaultWorkspace

type harness struct {
	svc *Service
	st  *store.SQLite
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, err := secret.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	return &harness{svc: New(st, keys, pools, catalog.New(10*time.Minute)), st: st}
}

func (h *harness) addShop(t *testing.T, mode engine.Mode) {
	t.Helper()
	_, _, err := h.svc.CreateConnection(context.Background(), ws, ConnInput{
		Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), Description: "Shop DB", Mode: mode,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
}

func (h *harness) query(t *testing.T, in QueryInput) (string, error) {
	t.Helper()
	if in.Source == "" {
		in.Source = "shop"
	}
	return h.svc.Query(context.Background(), ws, in)
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestSources(t *testing.T) {
	h := newHarness(t)
	out, err := h.svc.Sources(context.Background(), ws)
	if err != nil || out != "-- no connections. Add one with `qh conn add`\n" {
		t.Fatalf("empty: %q, %v", out, err)
	}
	h.addShop(t, engine.ModeRO)
	out, _ = h.svc.Sources(context.Background(), ws)
	if out != "shop sqlite ro — Shop DB\n" {
		t.Fatalf("got %q", out)
	}
}

func TestQuery(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	out, err := h.query(t, QueryInput{Query: "SELECT id, email FROM users ORDER BY id"})
	if err != nil || !strings.HasPrefix(out, "id\temail\n1\talice@example.com\n2\tzoe@example.com\n3\tnobody@example.com\n-- 3 rows · ") {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestReadOnlyAndInvalid(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	cases := map[string]string{
		"DELETE FROM users":  `error[read_only]: connection "shop" is read-only; only statements that read data are allowed. Use a read-write connection to change data`,
		"SELECT 1; SELECT 2": "error[invalid]: only one statement per call is supported. Send each statement in its own query call",
		"   ":                "error[invalid]: query is empty",
		"BEGIN":              "error[invalid]: BEGIN statements are not supported. Each query call runs in its own transaction",
	}
	for q, want := range cases {
		if _, err := h.query(t, QueryInput{Query: q}); errText(err) != want {
			t.Errorf("%q: got %q", q, errText(err))
		}
	}
}

func TestUnknownSource(t *testing.T) {
	h := newHarness(t)
	_, err := h.query(t, QueryInput{Source: "nope", Query: "SELECT 1"})
	if want := "error[unknown_source]: no connection named \"nope\". No connections exist yet. Add one with `qh conn add`"; errText(err) != want {
		t.Fatalf("got %q", errText(err))
	}
	h.addShop(t, engine.ModeRO)
	_, err = h.query(t, QueryInput{Source: "nope", Query: "SELECT 1"})
	if want := `error[unknown_source]: no connection named "nope". Available: shop`; errText(err) != want {
		t.Fatalf("got %q", errText(err))
	}
}

func TestSuggestions(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	_, err := h.query(t, QueryInput{Query: "SELECT emial FROM users"})
	if !strings.Contains(errText(err), "Did you mean users.email?") {
		t.Errorf("column: %q", errText(err))
	}
	_, err = h.query(t, QueryInput{Query: "SELECT * FROM user"})
	if !strings.Contains(errText(err), "Did you mean users?") {
		t.Errorf("table: %q", errText(err))
	}
}

func TestSchema(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := context.Background()
	out, err := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"})
	if err != nil || !strings.HasPrefix(out, "main: 3 tables, 1 view\n") {
		t.Fatalf("overview %q, %v", out, err)
	}
	out, _ = h.svc.Schema(ctx, ws, SchemaInput{Source: "shop", Object: "orders"})
	if !strings.Contains(out, "  user_id integer NN → users.id\n") {
		t.Fatalf("detail %q", out)
	}
	out, _ = h.svc.Schema(ctx, ws, SchemaInput{Source: "shop", Pattern: "ordr"})
	if out != "-- no objects match \"ordr\". Did you mean orders?\n" {
		t.Fatalf("no match %q", out)
	}
	n, _, err := h.svc.RefreshSchema(ctx, ws, "shop")
	if err != nil || n != 4 {
		t.Fatalf("refresh = %d, %v", n, err)
	}
}

func TestMaxRows(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	out, _ := h.query(t, QueryInput{Query: "SELECT id FROM orders ORDER BY id", MaxRows: 2})
	if !strings.HasPrefix(out, "id\n1\n2\n-- 2 rows shown, more exist (truncated) · ") {
		t.Fatalf("got %q", out)
	}
	out, _ = h.query(t, QueryInput{Query: "SELECT id FROM orders ORDER BY id", MaxRows: 5000})
	if !strings.Contains(out, "-- 100 rows shown, more exist (truncated)") {
		t.Fatalf("request above the connection cap: %q", out[len(out)-60:])
	}
}

func TestDDLInvalidatesSchemaCache(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRW)
	ctx := context.Background()
	if out, _ := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); strings.Contains(out, "extra") {
		t.Fatal("extra exists before CREATE")
	}
	if _, err := h.query(t, QueryInput{Query: "CREATE TABLE extra (x int)"}); err != nil {
		t.Fatal(err)
	}
	if out, _ := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); !strings.Contains(out, "extra") {
		t.Fatalf("cache not invalidated: %q", out)
	}
}

func TestExplain(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	out, err := h.query(t, QueryInput{Query: "DELETE FROM users WHERE id = 1", Explain: true})
	if err != nil || !strings.HasPrefix(out, "plan\n") {
		t.Fatalf("explain of a write on ro: %q, %v", out, err)
	}
}
