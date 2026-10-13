// Package enginetest is the conformance suite every engine must pass.
package enginetest

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/qerr"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Harness adapts an engine to the suite.
type Harness struct {
	Engine engine.Engine
	// Setup returns specs for one database loaded with the engine's fixture:
	// ro is opened read-only and rw read-write.
	Setup func(t *testing.T) (ro, rw engine.ConnSpec)
	// SlowQuery runs for several seconds.
	SlowQuery string
	// ReadOnlyEscapes are engine-specific statements that must fail on the
	// read-only connection without changing data.
	ReadOnlyEscapes []string
	// GoldenPrefix names this engine's golden files in testdata/.
	GoldenPrefix string
}

var commonEscapes = []string{
	"INSERT INTO users (id, email) VALUES (99, 'x@example.com')",
	"UPDATE users SET name = 'x'",
	"DELETE FROM order_items",
	"CREATE TABLE evil (x int)",
	"DROP TABLE order_items",
	"COMMIT; DELETE FROM order_items",
}

// Run runs the suite.
func Run(t *testing.T, h Harness) {
	ctx := context.Background()
	roSpec, rwSpec := h.Setup(t)
	ro := open(t, h.Engine, roSpec)
	rw := open(t, h.Engine, rwSpec)
	opts := engine.QueryOpts{MaxRows: 100, ReadOnly: true, Timeout: 10 * time.Second}

	t.Run("select", func(t *testing.T) {
		expect(t, run(t, ro, "SELECT id, email, name FROM users ORDER BY id", opts),
			"id\temail\tname\n1\talice@example.com\tAlice\n2\tzoe@example.com\tZoë 🚀\n3\tnobody@example.com\tNULL\n-- 3 rows")
	})

	t.Run("truncation", func(t *testing.T) {
		o := opts
		o.MaxRows = 3
		expect(t, run(t, ro, "SELECT id FROM orders ORDER BY id", o),
			"id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
		expect(t, run(t, ro, "SELECT id FROM orders WHERE id <= 3 ORDER BY id", o),
			"id\n1\n2\n3\n-- 3 rows")
		expect(t, run(t, ro, "SELECT id FROM orders ORDER BY id LIMIT 50", o),
			"id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
	})

	t.Run("trailing_semicolon", func(t *testing.T) {
		expect(t, run(t, ro, "SELECT count(*) AS n FROM orders; -- total", opts), "n\n250\n-- 1 row")
		expect(t, run(t, ro, "SELECT count(*) AS n FROM orders -- total", opts), "n\n250\n-- 1 row")
	})

	t.Run("values", func(t *testing.T) {
		expect(t, run(t, ro, "SELECT bio, avatar, created_at FROM users WHERE id = 1", opts),
			"bio\tavatar\tcreated_at\nline1\\nline2\\tx\t<bytes 4B>\t2025-03-14T10:22:31\n-- 1 row")
	})

	t.Run("read_only", func(t *testing.T) {
		_, err := ro.Query(ctx, commonEscapes[0], opts)
		if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
			t.Errorf("INSERT on read-only: %v", err)
		}
		for _, q := range append(append([]string{}, commonEscapes...), h.ReadOnlyEscapes...) {
			if _, err := ro.Query(ctx, q, opts); err == nil {
				t.Errorf("%q succeeded on a read-only connection", q)
			}
		}
		expect(t, run(t, rw, "SELECT (SELECT count(*) FROM users) AS u, (SELECT count(*) FROM orders) AS o, (SELECT count(*) FROM order_items) AS i", opts),
			"u\to\ti\n3\t250\t20\n-- 1 row")
		if _, err := rw.Query(ctx, "SELECT * FROM evil", opts); err == nil {
			t.Error("table evil exists after read-only escapes")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := ro.Query(ctx, h.SlowQuery, engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 300 * time.Millisecond})
		if e := qerr.From(err); err == nil || e.Category != qerr.Timeout {
			t.Errorf("slow query: %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("timeout took %v", d)
		}
	})

	t.Run("unknown_identifiers", func(t *testing.T) {
		_, err := ro.Query(ctx, "SELECT email_addr FROM users", opts)
		if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || e.UnknownColumn != "email_addr" {
			t.Errorf("unknown column: %#v", e)
		}
		_, err = ro.Query(ctx, "SELECT * FROM userz", opts)
		if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || e.UnknownTable != "userz" {
			t.Errorf("unknown table: %#v", e)
		}
	})

	t.Run("writes", func(t *testing.T) {
		w := engine.QueryOpts{MaxRows: 100, Timeout: 10 * time.Second}
		r, err := rw.Query(ctx, "INSERT INTO users (id, email) VALUES (99, 'new@example.com')", w)
		if err != nil || r.RowsAffected != 1 || len(r.Columns) != 0 {
			t.Fatalf("insert: %+v, %v", r, err)
		}
		r, err = rw.Query(ctx, "DELETE FROM users WHERE id = 99", w)
		if err != nil || r.RowsAffected != 1 {
			t.Fatalf("delete: %+v, %v", r, err)
		}
	})

	t.Run("explain", func(t *testing.T) {
		r, err := ro.Explain(ctx, "SELECT * FROM orders WHERE id = 1", opts)
		if err != nil || len(r.Columns) != 1 || len(r.Rows) == 0 {
			t.Fatalf("explain: %+v, %v", r, err)
		}
	})

	t.Run("ping", func(t *testing.T) {
		if v, err := ro.Ping(ctx); err != nil || v == "" {
			t.Fatalf("ping = %q, %v", v, err)
		}
	})

	t.Run("introspect", func(t *testing.T) {
		RunCatalog(t, CatalogHarness{Engine: h.Engine, Spec: roSpec, IDColumn: "id", GoldenPrefix: h.GoldenPrefix})
		cat, err := ro.Introspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if items := one(t, cat, "order_items"); !reflect.DeepEqual(items.PrimaryKey, []string{"order_id", "sku"}) {
			t.Errorf("order_items PK: %v", items.PrimaryKey)
		}
	})
}

// CatalogHarness adapts an engine to the catalog checks, which document
// engines pass too.
type CatalogHarness struct {
	Engine engine.Engine
	// Spec opens a connection to a database loaded with the engine's fixture
	// (users, orders referencing users, order_items, the paid_orders view).
	Spec engine.ConnSpec
	// IDColumn is the key that orders.user_id references: "id" in SQL,
	// "_id" in MongoDB.
	IDColumn     string
	GoldenPrefix string
}

// RunCatalog checks Introspect against the fixture and the golden files.
func RunCatalog(t *testing.T, h CatalogHarness) {
	ctx := context.Background()
	c := open(t, h.Engine, h.Spec)
	cat, err := c.Introspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orders, users, paid := one(t, cat, "orders"), one(t, cat, "users"), one(t, cat, "paid_orders")
	if !hasFK(orders, "user_id", "users", h.IDColumn) {
		t.Errorf("orders FKs: %+v", orders.ForeignKeys)
	}
	if len(users.ReferencedBy) == 0 || users.ReferencedBy[0].Object != "orders" {
		t.Errorf("users.ReferencedBy: %+v", users.ReferencedBy)
	}
	if paid.Kind != engine.KindView {
		t.Errorf("paid_orders kind: %v", paid.Kind)
	}
	if !hasUniqueIndex(users, "email") {
		t.Errorf("users indexes: %+v", users.Indexes)
	}
	overview, _ := format.Schema(cat, format.SchemaOpts{})
	golden(t, h.GoldenPrefix+"_overview.golden", overview)
	ordersOut, _ := format.Schema(cat, format.SchemaOpts{Object: "orders"})
	golden(t, h.GoldenPrefix+"_orders.golden", ordersOut)
	usersOut, _ := format.Schema(cat, format.SchemaOpts{Object: "users"})
	golden(t, h.GoldenPrefix+"_users.golden", usersOut)
}

func open(t *testing.T, e engine.Engine, spec engine.ConnSpec) engine.Conn {
	t.Helper()
	c, err := e.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("open %s: %v", spec.ID, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// run executes q and renders it without the timing part of the footer.
func run(t *testing.T, c engine.Conn, q string, o engine.QueryOpts) string {
	t.Helper()
	r, err := c.Query(context.Background(), q, o)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	s := strings.TrimSuffix(format.Result(r), "\n")
	if i := strings.LastIndex(s, " · "); i >= 0 {
		s = s[:i]
	}
	return s
}

func expect(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func one(t *testing.T, c *engine.Catalog, name string) *engine.Object {
	t.Helper()
	objs := c.Lookup(name)
	if len(objs) != 1 {
		t.Fatalf("lookup %q found %d objects", name, len(objs))
	}
	return objs[0]
}

func hasFK(o *engine.Object, col, refObject, refCol string) bool {
	for _, fk := range o.ForeignKeys {
		if reflect.DeepEqual(fk.Columns, []string{col}) && fk.RefObject == refObject && reflect.DeepEqual(fk.RefColumns, []string{refCol}) {
			return true
		}
	}
	return false
}

func hasUniqueIndex(o *engine.Object, col string) bool {
	for _, ix := range o.Indexes {
		if ix.Unique && reflect.DeepEqual(ix.Columns, []string{col}) {
			return true
		}
	}
	return false
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run the test with -update): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}
