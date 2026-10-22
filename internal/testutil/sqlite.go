// Package testutil holds fixtures shared by tests in several packages.
package testutil

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// SQLiteFixtureStatements build the shared fixture schema and rows.
var SQLiteFixtureStatements = []string{
	`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT, bio TEXT, avatar BLOB, created_at TEXT)`,
	`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id),
		status TEXT NOT NULL CHECK (status IN ('pending', 'paid', 'shipped')), total NUMERIC(12,2), note TEXT)`,
	`CREATE INDEX orders_user_id ON orders (user_id)`,
	`CREATE TABLE order_items (order_id INTEGER NOT NULL REFERENCES orders(id), sku TEXT NOT NULL,
		qty INTEGER NOT NULL, PRIMARY KEY (order_id, sku))`,
	`CREATE VIEW paid_orders AS SELECT id, user_id, total FROM orders WHERE status = 'paid'`,
	`INSERT INTO users VALUES
		(1, 'alice@example.com', 'Alice', 'line1' || char(10) || 'line2' || char(9) || 'x', x'DEADBEEF', '2025-03-14T10:22:31'),
		(2, 'zoe@example.com', 'Zoë 🚀', NULL, NULL, NULL),
		(3, 'nobody@example.com', NULL, NULL, NULL, NULL)`,
	`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 250)
		INSERT INTO orders (id, user_id, status, total)
		SELECT i, (i % 3) + 1, CASE WHEN i % 2 = 0 THEN 'paid' ELSE 'pending' END, i * 1.5 FROM n`,
	`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 10)
		INSERT INTO order_items SELECT i, 'sku-1', 1 FROM n UNION ALL SELECT i, 'sku-2', 2 FROM n`,
}

// SQLiteFixture creates a fixture database in a directory whose name contains
// a space, and returns the database path.
func SQLiteFixture(t testing.TB) string {
	t.Helper()
	return SQLiteFixtureIn(t, filepath.Join(t.TempDir(), "with space"))
}

// SQLiteFixtureIn creates the fixture database shop.db in dir, creating dir
// if needed, and returns the database path. The path must not contain ?: the
// driver opens it as a plain file name.
func SQLiteFixtureIn(t testing.TB, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "shop.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range SQLiteFixtureStatements {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("fixture statement failed: %v\n%s", err, s)
		}
	}
	return path
}
