package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/testutil"
)

func openConn(t *testing.T, mode engine.Mode) *conn {
	t.Helper()
	c, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: testutil.SQLiteFixture(t), Mode: mode, PoolSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.(*conn)
}

// TestReadOnlyLayers calls run directly, bypassing the classifier, so each
// protection below the classifier is tested on its own.
func TestReadOnlyLayers(t *testing.T) {
	ctx := context.Background()
	c := openConn(t, engine.ModeRO)
	opts := engine.QueryOpts{MaxRows: 10, Timeout: 10 * time.Second}
	dir := t.TempDir()

	for name, q := range map[string]string{
		"attach": "ATTACH DATABASE '" + filepath.Join(dir, "a.db") + "' AS evil",
		"vacuum": "VACUUM INTO '" + filepath.Join(dir, "v.db") + "'",
	} {
		if _, err := c.run(ctx, q, opts); err == nil {
			t.Errorf("%s succeeded without the classifier", name)
		}
	}
	for _, f := range []string{"a.db", "v.db"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s was created", f)
		}
	}

	_, err := c.run(ctx, "DELETE FROM order_items", opts)
	if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
		t.Errorf("DELETE without the classifier: %v", err)
	}

	// Escape attempts through the classifier must leave query_only on.
	for _, q := range []string{"PRAGMA query_only = 0", "EXPLAIN PRAGMA query_only = 0"} {
		if _, err := c.Query(ctx, q, engine.QueryOpts{MaxRows: 10, ReadOnly: true}); err == nil {
			t.Errorf("Query(%q) succeeded", q)
		}
	}
	if _, err := c.Explain(ctx, "PRAGMA query_only = 0", engine.QueryOpts{MaxRows: 10}); err == nil {
		t.Error("Explain of a pragma assignment succeeded")
	}
	r, err := c.run(ctx, "PRAGMA query_only", opts)
	if err != nil || len(r.Rows) != 1 || r.Rows[0][0].Text != "1" {
		t.Errorf("query_only = %+v, %v", r, err)
	}
}

func TestRowsAffectedAfterDDL(t *testing.T) {
	ctx := context.Background()
	c := openConn(t, engine.ModeRW)
	opts := engine.QueryOpts{MaxRows: 10, Timeout: 10 * time.Second}
	r, err := c.Query(ctx, "DELETE FROM order_items WHERE order_id <= 5", opts)
	if err != nil || r.RowsAffected != 10 {
		t.Fatalf("delete: %+v, %v", r, err)
	}
	r, err = c.Query(ctx, "CREATE TABLE t2 (x int)", opts)
	if err != nil || r.RowsAffected != 0 {
		t.Fatalf("create: %+v, %v", r, err)
	}
}
