package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmariadb "github.com/testcontainers/testcontainers-go/modules/mariadb"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
	"github.com/anderson-0/qh/internal/qerr"
)

// fixture holds the shared conformance data (see enginetest) plus a mixed-case table.
func fixture() []string {
	s := []string{
		"ALTER DATABASE qh CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
		`CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL UNIQUE COMMENT 'Login email',
			name VARCHAR(255), bio TEXT, avatar BLOB, created_at DATETIME) COMMENT 'App users'`,
		`CREATE TABLE orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, user_id BIGINT NOT NULL,
			status ENUM('pending','paid','shipped') NOT NULL DEFAULT 'pending', total DECIMAL(12,2), note TEXT,
			INDEX orders_user_id (user_id), FOREIGN KEY (user_id) REFERENCES users(id))`,
		`CREATE TABLE order_items (order_id BIGINT NOT NULL, sku VARCHAR(64) NOT NULL, qty INT NOT NULL,
			PRIMARY KEY (order_id, sku), FOREIGN KEY (order_id) REFERENCES orders(id))`,
		"CREATE VIEW paid_orders AS SELECT id, user_id, total FROM orders WHERE status = 'paid'",
		"CREATE TABLE AuditLog (id BIGINT PRIMARY KEY, EventName TEXT)",
		`INSERT INTO users VALUES
			(1, 'alice@example.com', 'Alice', CONCAT('line1', CHAR(10), 'line2', CHAR(9), 'x'), X'DEADBEEF', '2025-03-14 10:22:31'),
			(2, 'zoe@example.com', 'Zoë 🚀', NULL, NULL, NULL),
			(3, 'nobody@example.com', NULL, NULL, NULL, NULL)`,
	}
	var orders, items []string
	for i := 1; i <= 250; i++ {
		status := "pending"
		if i%2 == 0 {
			status = "paid"
		}
		orders = append(orders, fmt.Sprintf("(%d, %d, '%s', %d * 1.5)", i, i%3+1, status, i))
	}
	for i := 1; i <= 10; i++ {
		items = append(items, fmt.Sprintf("(%d, 'sku-1', 1)", i), fmt.Sprintf("(%d, 'sku-2', 2)", i))
	}
	return append(s,
		"INSERT INTO orders (id, user_id, status, total) VALUES "+strings.Join(orders, ", "),
		"INSERT INTO order_items VALUES "+strings.Join(items, ", "),
		"INSERT INTO AuditLog VALUES (1, 'login')",
		"ANALYZE TABLE users, orders, order_items, AuditLog",
	)
}

// startDB starts image, loads the fixture and returns a go-sql-driver DSN.
func startDB(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	var dsn string
	if strings.HasPrefix(image, "mariadb") {
		ctr, err := tcmariadb.Run(ctx, image, tcmariadb.WithDatabase("qh"), tcmariadb.WithUsername("qh"), tcmariadb.WithPassword("secret-pw-123"))
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		if err != nil {
			t.Fatal(err)
		}
		if dsn, err = ctr.ConnectionString(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		ctr, err := tcmysql.Run(ctx, image, tcmysql.WithDatabase("qh"), tcmysql.WithUsername("qh"), tcmysql.WithPassword("secret-pw-123"))
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		if err != nil {
			t.Fatal(err)
		}
		if dsn, err = ctr.ConnectionString(ctx); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range fixture() {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("fixture: %v\n%s", err, s)
		}
	}
	return dsn
}

func specs(dsn string) (engine.ConnSpec, engine.ConnSpec) {
	return engine.ConnSpec{ID: "ro", Version: 1, Engine: "mysql", DSN: dsn, Mode: engine.ModeRO, PoolSize: 3},
		engine.ConnSpec{ID: "rw", Version: 1, Engine: "mysql", DSN: dsn, Mode: engine.ModeRW, PoolSize: 3}
}

func openConn(t *testing.T, spec engine.ConnSpec) engine.Conn {
	t.Helper()
	c, err := Engine{}.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

const slowQuery = "SELECT COUNT(*) FROM orders a, orders b, orders c, orders d WHERE a.total + b.total + c.total + d.total > 0"

func TestConformance(t *testing.T) {
	images := []struct{ image, golden string }{
		{"mysql:8.0", "mysql"}, {"mysql:8.4", "mysql"}, {"mariadb:10.11", "mariadb"}, {"mariadb:11.4", "mariadb"},
	}
	for _, im := range images {
		t.Run(im.image, func(t *testing.T) {
			enginetest.Run(t, enginetest.Harness{
				Engine:    Engine{},
				Setup:     func(t *testing.T) (engine.ConnSpec, engine.ConnSpec) { return specs(startDB(t, im.image)) },
				SlowQuery: slowQuery,
				ReadOnlyEscapes: []string{
					"CREATE TEMPORARY TABLE evil_tmp (x int)",
					"SELECT * FROM users INTO OUTFILE '/tmp/qh-evil.txt'",
					"LOAD DATA LOCAL INFILE '/etc/hosts' INTO TABLE users",
					"HANDLER users OPEN",
					"SET SESSION TRANSACTION READ WRITE",
					"SELECT 1; COMMIT; DELETE FROM order_items",
					"/*! DELETE FROM order_items */ SELECT 1",
					"REPLACE INTO users (id, email) VALUES (1, 'x@example.com')",
				},
				GoldenPrefix: im.golden,
			})
		})
	}
}

func TestEnginesRegistered(t *testing.T) {
	for _, name := range []string{"mysql", "mariadb"} {
		e, err := engine.Get(name)
		if err != nil || e.Name() != name {
			t.Fatalf("Get(%q) = %v, %v", name, e, err)
		}
	}
}

func TestSessionStateDoesNotLeak(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	ro, _ := specs(dsn)
	ro.PoolSize = 1
	c := openConn(t, ro)
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second}

	r, err := c.Query(ctx, "SELECT GET_LOCK('qh_leak', 0)", opts)
	if err != nil || r.Rows[0][0].Text != "1" {
		t.Fatalf("get_lock: %+v %v", r, err)
	}
	other, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var free int
	if err := other.QueryRowContext(ctx, "SELECT IS_FREE_LOCK('qh_leak')").Scan(&free); err != nil || free != 1 {
		t.Fatalf("named lock leaked: free=%d err=%v", free, err)
	}

	capped := opts
	capped.MaxRows = 2
	if r, err := c.Query(ctx, "SELECT id FROM orders ORDER BY id", capped); err != nil || !r.Truncated {
		t.Fatalf("capped: %+v %v", r, err)
	}
	cat, err := c.Introspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if orders := cat.Lookup("orders"); len(orders) != 1 || len(orders[0].Fields) != 5 {
		t.Fatalf("sql_select_limit leaked into introspection: %+v", orders)
	}
}

func TestExplicitLimitAboveCap(t *testing.T) {
	ro, _ := specs(startDB(t, "mariadb:11.4"))
	ro.PoolSize = 1
	c := openConn(t, ro)
	ctx := context.Background()
	start := time.Now()
	r, err := c.Query(ctx, "SELECT a.id FROM orders a, orders b ORDER BY a.id, b.id LIMIT 60000",
		engine.QueryOpts{MaxRows: 5, ReadOnly: true, Timeout: 20 * time.Second})
	if err != nil || len(r.Rows) != 5 || !r.Truncated {
		t.Fatalf("explicit limit: %+v %v", r, err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	if r, err := c.Query(ctx, "SELECT 1 AS one", engine.QueryOpts{MaxRows: 5, ReadOnly: true, Timeout: 5 * time.Second}); err != nil || r.Rows[0][0].Text != "1" {
		t.Fatalf("next query after an early stop: %+v %v", r, err)
	}
}

func TestBusy(t *testing.T) {
	ro, _ := specs(startDB(t, "mysql:8.4"))
	ro.PoolSize = 1
	c := openConn(t, ro)
	old := slotWait
	slotWait = 200 * time.Millisecond
	defer func() { slotWait = old }()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Query(context.Background(), slowQuery, engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 2 * time.Second})
	}()
	time.Sleep(300 * time.Millisecond)
	_, err := c.Query(context.Background(), "SELECT 1", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second})
	if e := qerr.From(err); err == nil || e.Category != qerr.Busy {
		t.Fatalf("err = %v", err)
	}
	wg.Wait()
}

func TestAuthFailed(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	bad := strings.Replace(dsn, "secret-pw-123", "wrong-pw", 1)
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: bad, Mode: engine.ModeRO, PoolSize: 1})
	if err == nil || !strings.HasPrefix(err.Error(), "error[connection_failed]: authentication failed") || strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionFailed(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost"} {
		_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x",
			DSN: "mysql://u:hunter22@" + host + ":1/db?timeout=2s", Mode: engine.ModeRO, PoolSize: 1})
		if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
			t.Fatalf("%s: %v", host, err)
		}
		if msg := err.Error(); strings.Contains(msg, "\n") || strings.Contains(msg, "hunter22") {
			t.Fatalf("%s: %q", host, msg)
		}
	}
}
