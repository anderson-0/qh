package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
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
					"SELECT 1.5INTO OUTFILE '/tmp/qh-evil-1.txt'",
					"SELECT id, 1.0INTO DUMPFILE '/tmp/qh-evil-2.txt' FROM users LIMIT 1",
					"SELECT 1e0INTO OUTFILE '/tmp/qh-evil-3.txt'",
					`SELECT 1 AS "a\" INTO OUTFILE '/tmp/qh-evil-4.txt' -- "`,
					`SELECT 'a\' INTO OUTFILE '/tmp/qh-evil-5.txt' -- '`,
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

func TestCancellationAndRecovery(t *testing.T) {
	for _, image := range []string{"mysql:8.0", "mysql:8.4", "mariadb:10.11", "mariadb:11.4"} {
		t.Run(image, func(t *testing.T) {
			dsn := startDB(t, image)
			ctx, stop := context.WithTimeout(context.Background(), 40*time.Second)
			defer stop()
			observer, err := sql.Open("mysql", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			ro, rw := specs(dsn)
			for _, spec := range []engine.ConnSpec{ro, rw} {
				t.Run(string(spec.Mode), func(t *testing.T) {
					spec.PoolSize = 1
					c := openConn(t, spec)
					for _, kind := range []string{"context cancel", "external cancel", "server timeout", "context deadline"} {
						t.Run(kind, func(t *testing.T) {
							qctx, cancel := context.WithCancel(ctx)
							defer cancel()
							opts := engine.QueryOpts{MaxRows: 1, Timeout: 5 * time.Second}
							want := qerr.QueryError
							switch kind {
							case "server timeout":
								opts.Timeout = 100 * time.Millisecond
								want = qerr.Timeout
							case "context deadline":
								var deadlineCancel context.CancelFunc
								qctx, deadlineCancel = context.WithTimeout(qctx, 100*time.Millisecond)
								defer deadlineCancel()
								want = qerr.Timeout
							}
							// Distinguish this query from any canceled work the server
							// has not yet stopped after the driver closed its socket.
							q := slowQuery + " /* qh_" + string(spec.Mode) + "_" + strings.ReplaceAll(kind, " ", "_") + " */"
							done := make(chan error, 1)
							go func() {
								_, err := c.Query(qctx, q, opts)
								done <- err
							}()
							if want == qerr.QueryError {
								var id uint64
								for {
									err := observer.QueryRowContext(ctx, `SELECT ID FROM information_schema.PROCESSLIST
WHERE ID <> CONNECTION_ID() AND DB = DATABASE() AND COMMAND = 'Query' AND INFO = ?`, q).Scan(&id)
									if err == nil {
										break
									}
									if !errors.Is(err, sql.ErrNoRows) {
										t.Fatal(err)
									}
									select {
									case err := <-done:
										t.Fatalf("query ended before cancellation: %v", err)
									case <-ctx.Done():
										t.Fatal(ctx.Err())
									case <-time.After(10 * time.Millisecond):
									}
								}
								if kind == "context cancel" {
									cancel()
								} else if _, err := observer.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", id)); err != nil {
									t.Fatal(err)
								}
							}
							var queryErr error
							select {
							case queryErr = <-done:
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							}
							var qe *qerr.Error
							if !errors.As(queryErr, &qe) || qe.Category != want {
								t.Errorf("query error = %v; want %s", queryErr, want)
							} else if want == qerr.QueryError && qe.Hint != "" {
								t.Errorf("cancellation contains timeout advice: %v", queryErr)
							} else if kind == "context cancel" && qe.Message != "query canceled" {
								t.Errorf("cancellation message = %q", qe.Message)
							}
							if kind == "external cancel" || kind == "server timeout" {
								code := uint16(1317)
								if kind == "server timeout" {
									code = 3024
									if strings.HasPrefix(image, "mariadb") {
										code = 1969
									}
								}
								var me *gomysql.MySQLError
								if !errors.As(queryErr, &me) || me.Number != code {
									t.Errorf("underlying server error = %v; want code %d", queryErr, code)
								}
							}
							res, err := c.Query(ctx, "SELECT 1", engine.QueryOpts{MaxRows: 1, Timeout: 5 * time.Second})
							if err != nil {
								t.Fatalf("query after cancellation: %v", err)
							}
							if len(res.Rows) != 1 || res.Rows[0][0].Text != "1" {
								t.Fatalf("query after cancellation = %+v", res)
							}
						})
					}
				})
			}
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
	// The reset runs after the result returns; the next call on the single
	// pooled connection waits for it.
	if _, err := c.Query(ctx, "SELECT 1", opts); err != nil {
		t.Fatal(err)
	}
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

func TestReadWriteConnectionDoesNotKeepTemporaryTables(t *testing.T) {
	_, rw := specs(startDB(t, "mysql:8.4"))
	rw.PoolSize = 1
	c := openConn(t, rw)
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, Timeout: 5 * time.Second}
	if _, err := c.Query(ctx, "CREATE TEMPORARY TABLE tmp_leak (x int)", opts); err != nil {
		t.Fatal(err)
	}
	_, err := c.Query(ctx, "SELECT * FROM tmp_leak", opts)
	if err == nil {
		t.Fatal("temporary table survived on a pooled connection")
	}
	if e := qerr.From(err); e.Category != qerr.QueryError || e.UnknownTable != "tmp_leak" {
		t.Fatalf("err = %v", err)
	}
}

func TestFailedWriteDoesNotPoolDirtyConnection(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	_, rw := specs(dsn)
	rw.PoolSize = 1
	c := openConn(t, rw)
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, Timeout: 5 * time.Second}
	// A compound statement contains semicolons, which Query refuses, so the
	// procedure is created through a plain connection.
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `CREATE PROCEDURE p_fail() BEGIN
		CREATE TEMPORARY TABLE leak_t (x int);
		SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'boom';
	END`); err != nil {
		t.Fatal(err)
	}
	_, err = c.Query(ctx, "CALL p_fail()", opts)
	if err == nil || qerr.From(err).Category != qerr.QueryError || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("CALL p_fail(): %v", err)
	}
	_, err = c.Query(ctx, "SELECT * FROM leak_t", opts)
	if err == nil || qerr.From(err).UnknownTable != "leak_t" {
		t.Fatalf("temporary table survived a failed statement: %v", err)
	}
}

func TestExplainOfWrite(t *testing.T) {
	ro, rw := specs(startDB(t, "mysql:8.4"))
	rw.PoolSize = 1
	rwc := openConn(t, rw)
	roc := openConn(t, ro)
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, Timeout: 5 * time.Second}
	const upd = "UPDATE orders SET note = 'x' WHERE id = 1"

	r, err := rwc.Explain(ctx, upd, opts)
	if err != nil || len(r.Rows) == 0 {
		t.Fatalf("rw explain: %+v %v", r, err)
	}
	r, err = rwc.Query(ctx, "SELECT COUNT(*) FROM orders WHERE note = 'x'", opts)
	if err != nil || r.Rows[0][0].Text != "0" {
		t.Fatalf("EXPLAIN changed data: %+v %v", r, err)
	}
	// The connection is usable afterwards.
	if _, err := rwc.Explain(ctx, "SELECT * FROM orders", opts); err != nil {
		t.Fatalf("read explain after write explain: %v", err)
	}

	_, err = roc.Explain(ctx, upd, opts)
	if err == nil || qerr.From(err).Category != qerr.Invalid || !strings.Contains(err.Error(), "needs a read-write connection") {
		t.Fatalf("ro explain: %v", err)
	}
	_, err = rwc.Explain(ctx, upd, engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 5 * time.Second})
	if err == nil || qerr.From(err).Category != qerr.Invalid {
		t.Fatalf("per-call read-only explain on rw conn: %v", err)
	}
}

func TestPasswordOverride(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	// The DSN is the driver's user:password@tcp(host:port)/db form.
	wrong := strings.Replace(dsn, "qh:secret-pw-123@", "qh:wrong@", 1)
	if wrong == dsn {
		t.Fatalf("unexpected dsn format")
	}
	ro, _ := specs(wrong)
	ro.Password = "secret-pw-123"
	c := openConn(t, ro)
	if r, err := c.Query(context.Background(), "SELECT 1", engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 5 * time.Second}); err != nil || r.Rows[0][0].Text != "1" {
		t.Fatalf("%+v %v", r, err)
	}
}
