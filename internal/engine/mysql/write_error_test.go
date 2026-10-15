package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/qerr"
	gomysql "github.com/go-sql-driver/mysql"
)

func TestWriteUnconfirmed(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		reason                    stopReason
		done, rollback, committed bool
		statement                 error
		category                  qerr.Category
		hint                      bool
	}{
		{"denied kill", stopCallerCancel, false, false, false, context.Canceled, qerr.QueryError, true},
		{"missing response", stopDeadline, false, false, false, context.Canceled, qerr.Timeout, true},
		{"raw socket", stopNone, false, false, false, io.EOF, qerr.ConnectionFailed, true},
		{"failed rollback", stopCallerCancel, true, false, false, interrupted(), qerr.QueryError, true},
		{"confirmed cancellation", stopCallerCancel, true, true, false, interrupted(), qerr.QueryError, false},
		{"deadline", stopDeadline, true, true, false, interrupted(), qerr.Timeout, false},
		{"shutdown", stopShutdown, true, true, false, interrupted(), qerr.QueryError, false},
		{"row cap uncertain", stopRowCap, false, false, false, context.Canceled, qerr.QueryError, true},
		{"native timeout wins", stopCallerCancel, true, true, false, &gomysql.MySQLError{Number: 3024, Message: "server timeout"}, qerr.Timeout, false},
		{"known commit", stopDeadline, true, false, true, nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hidden := errors.New("private-control-detail")
			got := interruptionError(context.Background(), stopOutcome{Reason: tc.reason, Admitted: true, StatementDone: tc.done, Err: hidden}, cleanupResult{RolledBack: tc.rollback, Committed: tc.committed, Err: hidden}, tc.statement)
			if tc.committed {
				if got != nil {
					t.Fatal("known commit changed outcome")
				}
				return
			}
			if got == nil {
				t.Fatal("missing error")
			}
			qe := qerr.From(got)
			if qe.Category != tc.category || (qe.Hint == unconfirmedHint) != tc.hint || strings.Contains(got.Error(), hidden.Error()) {
				t.Fatalf("error=%v", got)
			}
			if !errors.Is(got, hidden) || !errors.Is(got, tc.statement) {
				t.Fatal("lost internal causes")
			}
		})
	}
}
func TestWriteCommitOrdering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call, s := guardFixture(t, ctx, func(context.Context, string) ([][]driver.Value, error) { t.Error("unexpected kill"); return nil, nil })
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	call.Complete(nil)
	cancel()
	if call.CommitAllowed() {
		t.Fatal("committed after cancellation")
	}
	r := call.Cleanup(false)
	if !r.RolledBack {
		t.Fatal("did not rollback")
	}
	for _, e := range s.snapshot() {
		if e == "1:COMMIT" {
			t.Fatal("late COMMIT")
		}
	}
}
func TestWriteCommitSurvivesCleanupFailure(t *testing.T) {
	call, s := guardFixture(t, context.Background(), func(context.Context, string) ([][]driver.Value, error) { return nil, nil })
	base := s.handle
	s.handle = func(ctx context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "SET @qh_released") {
			return nil, errors.New("reset failed")
		}
		return base(ctx, id, q, a)
	}
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	out := call.Complete(nil)
	if !call.CommitAllowed() {
		t.Fatal("normal commit denied")
	}
	r := call.Cleanup(true)
	if !r.Committed || r.Err == nil || interruptionError(context.Background(), out, r, nil) != nil {
		t.Fatalf("commit outcome=%+v", r)
	}
	if call.owner.owner.db.Stats().OpenConnections != 0 || call.owner.control.Stats().OpenConnections != 0 {
		t.Fatal("reset failure pooled")
	}
}

type laterErrorRows struct{ scriptRows }

func (*laterErrorRows) HasNextResultSet() bool { return true }
func (*laterErrorRows) NextResultSet() error   { return interrupted() }
func (*laterErrorRows) Close() error           { return interrupted() }
func TestWriteCallTerminalResult(t *testing.T) {
	s := &scriptConnector{handle: func(context.Context, int, string, []driver.NamedValue) ([][]driver.Value, error) {
		return [][]driver.Value{{int64(1)}}, nil
	}}
	s.rowFactory = func(_ string, v [][]driver.Value) driver.Rows { return &laterErrorRows{scriptRows{values: v}} }
	db := scriptDB(t, s)
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	_, _, err = collectWithStop(context.Background(), func() {}, sc, "CALL later_result()", 10, false, true)
	var me *gomysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1317 {
		t.Fatalf("lost terminal result error: %v", err)
	}
}
