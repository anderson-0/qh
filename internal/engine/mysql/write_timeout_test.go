package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"strings"
	"testing"
	"time"
)

// waitForTarget observes the owned session, allowing the actual inner statement
// text of CALL. It is never used as a cancellation target identity in production.
func waitForTarget(t *testing.T, ctx context.Context, db *sql.DB, id uint64, want string) {
	t.Helper()
	for {
		var info sql.NullString
		err := db.QueryRowContext(ctx, "SELECT INFO FROM information_schema.PROCESSLIST WHERE ID = ? AND COMMAND IN ('Query','Execute')", id).Scan(&info)
		if err == nil && info.Valid && strings.Contains(info.String, want) {
			return
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("target did not become active")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func TestWriteGuardStoredProgramIdentity(t *testing.T) { testRealGuard(t, false) }
func TestWriteGuardReleasedMarker(t *testing.T)        { testRealGuard(t, true) }
func testRealGuard(t *testing.T, release bool) {
	for _, image := range []string{"mysql:8.0", "mysql:8.4"} {
		t.Run(image, func(t *testing.T) {
			dsn := startDB(t, image)
			ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			observer, err := sql.Open("mysql", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			if _, err = observer.ExecContext(ctx, "CREATE PROCEDURE qh_cancel_inner() SELECT SLEEP(20)+1"); err != nil {
				t.Fatal(err)
			}
			_, spec := specs(dsn)
			spec.PoolSize = 1
			c := openConn(t, spec).(*conn)
			callctx, cancel := context.WithCancel(ctx)
			defer cancel()
			call, err := c.writes.Begin(callctx)
			if err != nil {
				t.Fatal(err)
			}
			defer call.Cleanup(false)
			sc, err := c.acquire(call.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = call.Prepare(sc, c.setupSQL(10, time.Second, false)); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = observer.ExecContext(cleanup, fmt.Sprintf("KILL QUERY %d", call.target))
			}()
			if release {
				if _, err = sc.ExecContext(ctx, "SELECT RELEASE_ALL_LOCKS()"); err != nil {
					t.Fatal(err)
				}
			}
			qctx, err := call.Admit()
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, _, err := collect(qctx, func() {}, sc, "CALL qh_cancel_inner()", 10, false); done <- err }()
			waitForTarget(t, ctx, observer, call.target, "SLEEP(20)")
			cancel()
			var queryErr error
			select {
			case queryErr = <-done:
			case <-ctx.Done():
				t.Fatal("guard did not return")
			}
			out := call.Complete(queryErr)
			if out.Reason != stopCallerCancel || out.StatementDone == release {
				t.Fatalf("guard outcome=%+v", out)
			}
			cleanup := call.Cleanup(false)
			if !release && !cleanup.RolledBack {
				t.Fatalf("rollback=%+v", cleanup)
			}
		})
	}
}

const writeProbe = "UPDATE users SET name = CASE WHEN id = 1 THEN 'changed' ELSE CONCAT('changed', SLEEP(20)) END WHERE id IN (1,2) ORDER BY id /* qh_write_probe */"

func TestWriteTimeoutStopsServer(t *testing.T)  { testWriteStops(t, "timeout") }
func TestWriteCancelStopsServer(t *testing.T)   { testWriteStops(t, "cancel") }
func TestWriteCallStopsServer(t *testing.T)     { testWriteStops(t, "call") }
func TestWriteCallLaterResults(t *testing.T)    { testWriteStops(t, "later-call") }
func TestWriteFunctionStopsServer(t *testing.T) { testWriteStops(t, "function") }
func testWriteStops(t *testing.T, kind string) {
	for _, image := range []string{"mysql:8.0", "mysql:8.4"} {
		t.Run(image, func(t *testing.T) {
			var options []testcontainers.ContainerCustomizer
			if kind == "function" {
				options = append(options, testcontainers.WithCmdArgs("--log-bin-trust-function-creators=1"))
			}
			dsn := startDB(t, image, options...)
			ctx, stop := context.WithTimeout(context.Background(), 35*time.Second)
			defer stop()
			observer, err := sql.Open("mysql", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			_, spec := specs(dsn)
			spec.PoolSize = 1
			c := openConn(t, spec)
			q := writeProbe
			if kind == "call" {
				if _, err = observer.ExecContext(ctx, "CREATE PROCEDURE qh_write_probe() "+writeProbe); err != nil {
					t.Fatal(err)
				}
				q = "CALL qh_write_probe()"
			}
			if kind == "later-call" {
				if _, err = observer.ExecContext(ctx, "CREATE PROCEDURE qh_write_probe() BEGIN SELECT 1; "+writeProbe+"; END"); err != nil {
					t.Fatal(err)
				}
				q = "CALL qh_write_probe()"
			}
			if kind == "function" {
				if _, err = observer.ExecContext(ctx, "CREATE FUNCTION qh_write_probe() RETURNS INT DETERMINISTIC MODIFIES SQL DATA BEGIN "+writeProbe+"; RETURN 1; END"); err != nil {
					t.Fatal(err)
				}
				q = "SELECT qh_write_probe()"
			}
			callctx, cancel := context.WithCancel(ctx)
			defer cancel()
			opts := engine.QueryOpts{Timeout: 100 * time.Millisecond, MaxRows: 10}
			want := qerr.Timeout
			if kind == "cancel" {
				opts.Timeout = 10 * time.Second
				want = qerr.QueryError
			}
			done := make(chan error, 1)
			go func() { _, err := c.Query(callctx, q, opts); done <- err }()
			var target uint64
			for {
				err = observer.QueryRowContext(ctx, "SELECT ID FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() AND DB = DATABASE() AND (INFO LIKE '%qh_write_probe%' OR INFO LIKE '%SLEEP(20)%') AND COMMAND IN ('Query','Execute') LIMIT 1").Scan(&target)
				if err == nil {
					break
				}
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					t.Fatalf("write ended before observation: %v", err)
				case <-ctx.Done():
					t.Fatal("write never observed")
				case <-time.After(10 * time.Millisecond):
				}
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = observer.ExecContext(cleanup, fmt.Sprintf("KILL QUERY %d", target))
			}()
			// A normal user can prove the row is locked with NOWAIT, without PROCESS
			// or performance_schema grants. Reserve a separate observer transaction.
			probe, err := observer.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			if _, err = probe.ExecContext(ctx, "START TRANSACTION"); err != nil {
				t.Fatal(err)
			}
			defer probe.ExecContext(context.Background(), "ROLLBACK")
			var name string
			err = probe.QueryRowContext(ctx, "SELECT name FROM users WHERE id=1 FOR UPDATE NOWAIT").Scan(&name)
			var me *gomysql.MySQLError
			if !errors.As(err, &me) || me.Number != 3572 {
				t.Fatalf("did not observe row lock: %v", err)
			}
			if kind == "cancel" {
				cancel()
			}
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("write did not return")
			}
			if err == nil || qerr.From(err).Category != want {
				t.Fatalf("query error=%v want %s", err, want)
			}
			if want == qerr.Timeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Error("deadline cause missing")
			}
			if want == qerr.QueryError && !errors.Is(err, context.Canceled) {
				t.Error("cancellation cause missing")
			}
			var running int
			if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID = ? AND COMMAND IN ('Query','Execute')", target).Scan(&running); err != nil {
				t.Fatal(err)
			}
			err = probe.QueryRowContext(ctx, "SELECT name FROM users WHERE id=1 FOR UPDATE NOWAIT").Scan(&name)
			if running != 0 || err != nil || name != "Alice" {
				t.Errorf("server work survived return: active=%d lock-error=%v original-row=%v", running, err, name == "Alice")
			}
			var name2 string
			if err = observer.QueryRowContext(ctx, "SELECT name FROM users WHERE id=2").Scan(&name2); err != nil || name2 != "Zoë 🚀" {
				t.Error("original second row not preserved")
			}
			if _, err = c.Query(ctx, "SELECT 1", engine.QueryOpts{Timeout: time.Second}); err != nil {
				t.Errorf("pool did not recover: %v", err)
			}
		})
	}
}

func findActive(t *testing.T, ctx context.Context, db *sql.DB, text string) uint64 {
	t.Helper()
	for {
		var id uint64
		err := db.QueryRowContext(ctx, "SELECT ID FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() AND INFO = ? AND COMMAND IN ('Query','Execute') LIMIT 1", text).Scan(&id)
		if err == nil {
			return id
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("active query not observed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func TestWritePoolSaturatedCancellation(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hold, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if _, err = hold.ExecContext(ctx, "SELECT GET_LOCK('qh_hold_other',0)"); err != nil {
		t.Fatal(err)
	}
	defer hold.ExecContext(context.Background(), "SELECT RELEASE_LOCK('qh_hold_other')")
	_, spec := specs(dsn)
	spec.PoolSize = 2
	c := openConn(t, spec)
	otherSQL := "UPDATE users SET name=CONCAT('independent',GET_LOCK('qh_hold_other',20)) WHERE id=3"
	otherDone := make(chan error, 1)
	go func() {
		_, err := c.Query(ctx, otherSQL, engine.QueryOpts{Timeout: 25 * time.Second})
		otherDone <- err
	}()
	otherID := findActive(t, ctx, db, otherSQL)
	callctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Query(callctx, writeProbe, engine.QueryOpts{Timeout: 25 * time.Second})
		done <- err
	}()
	id := findActive(t, ctx, db, writeProbe)
	defer db.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", id))
	defer db.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", otherID))
	cancel()
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("saturated call did not stop")
	}
	if err == nil || qerr.From(err).Category != qerr.QueryError || qerr.From(err).Hint != "" {
		t.Fatalf("cancel=%v", err)
	}
	var original string
	if err = db.QueryRowContext(ctx, "SELECT name FROM users WHERE id=1 FOR UPDATE NOWAIT").Scan(&original); err != nil || original != "Alice" {
		t.Errorf("saturated cancellation left lock/effect: %v", err)
	}
	select {
	case err = <-otherDone:
		t.Fatalf("other request stopped: %v", err)
	default:
	}
	if _, err = hold.ExecContext(ctx, "SELECT RELEASE_LOCK('qh_hold_other')"); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-otherDone:
	case <-ctx.Done():
		t.Fatal("other did not finish")
	}
	if err != nil {
		t.Fatal(err)
	}
	var a, b string
	if err = db.QueryRowContext(ctx, "SELECT a.name,b.name FROM users a,users b WHERE a.id=1 AND b.id=3").Scan(&a, &b); err != nil || a != "Alice" || b != "independent1" {
		t.Fatal("wrong request/data affected")
	}
}
func TestWriteRowCap(t *testing.T) {
	dsn := startDB(t, "mysql:8.4", testcontainers.WithCmdArgs("--log-bin-trust-function-creators=1"))
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"CREATE TABLE rowcap_counter (n INT)", "INSERT INTO rowcap_counter VALUES (0)", "CREATE FUNCTION qh_rowcap_touch() RETURNS INT DETERMINISTIC MODIFIES SQL DATA BEGIN UPDATE rowcap_counter SET n=n+1; RETURN 1; END"} {
		if _, err = db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_, spec := specs(dsn)
	spec.PoolSize = 1
	c := openConn(t, spec)
	res, err := c.Query(ctx, "SELECT qh_rowcap_touch(),REPEAT('x',100) FROM orders a,orders b LIMIT 5000", engine.QueryOpts{MaxRows: 1, Timeout: 20 * time.Second})
	if err != nil || res == nil || !res.Truncated || len(res.Rows) != 1 {
		t.Fatalf("row cap=%+v %v", res, err)
	}
	var n int
	if err = db.QueryRowContext(ctx, "SELECT n FROM rowcap_counter FOR UPDATE NOWAIT").Scan(&n); err != nil || n != 0 {
		t.Fatalf("row-cap effects not rolled back: n=%d err=%v", n, err)
	}
	if _, err = c.Query(ctx, "SELECT 1", engine.QueryOpts{}); err != nil {
		t.Fatal(err)
	}
}
func TestWriteCloseDuringExecution(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, spec := specs(dsn)
	spec.PoolSize = 1
	c := openConn(t, spec)
	done := make(chan error, 1)
	go func() { _, err := c.Query(ctx, writeProbe, engine.QueryOpts{Timeout: 25 * time.Second}); done <- err }()
	id := findActive(t, ctx, db, writeProbe)
	defer db.ExecContext(context.Background(), fmt.Sprintf("KILL QUERY %d", id))
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("Close missed executing call")
	}
	if err == nil || qerr.From(err).Category != qerr.QueryError || qerr.From(err).Hint != "" {
		t.Fatalf("shutdown=%v", err)
	}
	var name string
	if err = db.QueryRowContext(ctx, "SELECT name FROM users WHERE id=1 FOR UPDATE NOWAIT").Scan(&name); err != nil || name != "Alice" {
		t.Fatal("Close returned without rollback/lock release")
	}
}
