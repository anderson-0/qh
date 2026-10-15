package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
