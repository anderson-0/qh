package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

// TestReadOnlyTransactionLayer calls run directly, bypassing the engine's own
// classification, to prove the database's read-only transaction holds too.
func TestReadOnlyTransactionLayer(t *testing.T) {
	ro, _ := specs(startDB(t, "mysql:8.4"))
	c := openConn(t, ro).(*conn)
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second}
	for _, q := range []string{
		"INSERT INTO users (id, email) VALUES (98, 'layer@example.com')",
		"CREATE TEMPORARY TABLE layer_tmp (x int)",
	} {
		_, err := c.run(ctx, q, opts, sqlx.Write, true)
		if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
			t.Errorf("%q: %v", q, err)
		}
	}
	r, err := c.run(ctx, "SELECT COUNT(*) FROM users", opts, sqlx.Read, true)
	if err != nil || r.Rows[0][0].Text != "3" {
		t.Fatalf("data changed: %+v %v", r, err)
	}
}
