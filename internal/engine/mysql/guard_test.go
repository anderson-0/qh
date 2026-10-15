package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

func guardFixture(t *testing.T, ctx context.Context, guard func(context.Context, string) ([][]driver.Value, error)) (*writeCall, *scriptConnector) {
	t.Helper()
	s := preparationScript(t)
	base := s.handle
	s.handle = func(ctx context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
		if strings.Contains(q, "information_schema.PROCESSLIST") || strings.HasPrefix(q, "KILL QUERY ") {
			if id != 2 {
				t.Error("guard used replacement control")
			}
			return guard(ctx, q)
		}
		return base(ctx, id, q, a)
	}
	owner := &conn{db: scriptDB(t, s)}
	w := newWriteCanceler(owner, s, 1)
	t.Cleanup(func() { w.Close() })
	call, err := w.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { call.Cleanup(false) })
	sc, err := owner.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Prepare(sc, "SET test_limits"); err != nil {
		t.Fatal(err)
	}
	return call, s
}
func activeTarget() [][]driver.Value {
	return [][]driver.Value{{"server-a", int64(41), "Query", "UPDATE users SET name='pending'"}}
}
func awaitGuard(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("guard event did not happen")
	}
}
func interrupted() error {
	return &gomysql.MySQLError{Number: 1317, Message: "Query execution was interrupted"}
}
func TestWriteGuardCancelBeforeAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	call, s := guardFixture(t, ctx, func(context.Context, string) ([][]driver.Value, error) {
		t.Error("guard ran before admission")
		return nil, nil
	})
	cancel()
	if _, err := call.Admit(); !errors.Is(err, context.Canceled) {
		t.Fatalf("admit = %v", err)
	}
	for _, e := range s.snapshot() {
		if strings.Contains(e, "KILL") {
			t.Fatal("kill before admission")
		}
	}
}
func TestWriteGuardDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	killed := make(chan struct{})
	call, _ := guardFixture(t, ctx, func(_ context.Context, q string) ([][]driver.Value, error) {
		if q == "KILL QUERY 41" {
			close(killed)
			return nil, nil
		}
		return activeTarget(), nil
	})
	qctx, err := call.Admit()
	if err != nil {
		t.Fatal(err)
	}
	awaitGuard(t, killed)
	if qctx.Err() != nil {
		t.Fatal("driver canceled before server response")
	}
	out := call.Complete(interrupted())
	if out.Reason != stopDeadline || !out.StatementDone {
		t.Fatalf("outcome=%+v", out)
	}
	if call.CommitAllowed() {
		t.Fatal("commit after deadline")
	}
}
func TestWriteGuardCompletionWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call, _ := guardFixture(t, ctx, func(context.Context, string) ([][]driver.Value, error) {
		t.Error("kill after completion")
		return nil, nil
	})
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	out := call.Complete(nil)
	cancel()
	if out.Reason != stopNone || !out.StatementDone {
		t.Fatalf("outcome=%+v", out)
	}
	if call.CommitAllowed() {
		t.Fatal("completion authorized commit after cancellation")
	}
}
func TestWriteGuardDispatchRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepSeen := make(chan struct{})
	active := make(chan struct{})
	killed := make(chan struct{})
	var checks atomic.Int32
	var call *writeCall
	call, _ = guardFixture(t, ctx, func(_ context.Context, q string) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "KILL") {
			close(killed)
			return nil, nil
		}
		if checks.Add(1) == 1 {
			close(prepSeen)
			return [][]driver.Value{{"server-a", int64(41), "Query", call.preparation}}, nil
		}
		<-active
		if checks.Load() == 2 {
			return [][]driver.Value{{"server-a", int64(41), "Sleep", nil}}, nil
		}
		return activeTarget(), nil
	})
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitGuard(t, prepSeen)
	close(active)
	awaitGuard(t, killed)
	if out := call.Complete(interrupted()); out.Reason != stopCallerCancel || !out.StatementDone {
		t.Fatalf("outcome=%+v", out)
	}
}
func TestWriteGuardLateKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checking := make(chan struct{})
	release := make(chan struct{})
	call, _ := guardFixture(t, ctx, func(_ context.Context, q string) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "KILL") {
			t.Error("late kill")
		}
		close(checking)
		<-release
		return activeTarget(), nil
	})
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitGuard(t, checking)
	completed := make(chan struct{})
	go func() { call.Complete(nil); close(completed) }()
	// Complete must publish completion before joining the still-running check.
	// A barrier observes the completion state without assuming goroutine timing.
	for {
		call.mu.Lock()
		done := call.completed
		call.mu.Unlock()
		if done {
			break
		}
		runtime.Gosched()
	}
	close(release)
	awaitGuard(t, completed)
	call.Cleanup(false)
	sc, err := call.owner.owner.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sc.Close()
}
func TestWriteGuardShutdown(t *testing.T) {
	killed := make(chan struct{})
	call, _ := guardFixture(t, context.Background(), func(_ context.Context, q string) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "KILL") {
			close(killed)
			return nil, nil
		}
		return activeTarget(), nil
	})
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { call.owner.Close(); close(closed) }()
	awaitGuard(t, killed)
	out := call.Complete(interrupted())
	if out.Reason != stopShutdown {
		t.Fatalf("reason=%v", out.Reason)
	}
	call.Cleanup(false)
	awaitGuard(t, closed)
}
func TestWriteGuardIdentityFailures(t *testing.T) {
	for _, kind := range []string{"uuid", "owner", "missing-lock", "broken", "denied"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var kills atomic.Int32
			call, _ := guardFixture(t, ctx, func(_ context.Context, q string) ([][]driver.Value, error) {
				if strings.HasPrefix(q, "KILL") {
					kills.Add(1)
					if kind != "denied" {
						t.Error("unverified kill")
					}
					return nil, errors.New("private-control-detail")
				}
				r := activeTarget()
				switch kind {
				case "uuid":
					r[0][0] = "other"
				case "owner":
					r[0][1] = int64(42)
				case "missing-lock":
					r[0][1] = nil
				case "broken":
					return nil, io.EOF
				}
				return r, nil
			})
			qctx, err := call.Admit()
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			awaitGuard(t, qctx.Done())
			out := call.Complete(qctx.Err())
			if out.StatementDone || out.Reason != stopCallerCancel {
				t.Fatalf("outcome=%+v", out)
			}
			if kills.Load() > 1 {
				t.Fatal("retried kill")
			}
		})
	}
}
func TestWriteGuardRowCap(t *testing.T) {
	killed := make(chan struct{})
	call, _ := guardFixture(t, context.Background(), func(_ context.Context, q string) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "KILL") {
			close(killed)
			return nil, nil
		}
		return activeTarget(), nil
	})
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	call.RequestTruncation()
	awaitGuard(t, killed)
	out := call.Complete(interrupted())
	if out.Reason != stopRowCap || !out.StatementDone || call.Context().Err() != nil {
		t.Fatalf("outcome=%+v", out)
	}
	if call.CommitAllowed() {
		t.Fatal("commit truncated result")
	}
}
func TestWriteGuardMissingResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call, _ := guardFixture(t, ctx, func(_ context.Context, q string) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "KILL") {
			return nil, nil
		}
		return activeTarget(), nil
	})
	qctx, err := call.Admit()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	select {
	case <-qctx.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("interrupt budget exceeded")
	}
	out := call.Complete(qctx.Err())
	if out.StatementDone || time.Since(start) < interruptWait {
		t.Fatalf("outcome=%+v wait=%v", out, time.Since(start))
	}
}
func TestWriteCleanupBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call, s := guardFixture(t, ctx, func(ctx context.Context, q string) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "KILL") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return activeTarget(), nil
	})
	base := s.handle
	s.handle = func(ctx context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
		if id == 2 && q == "ROLLBACK" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return base(ctx, id, q, a)
	}
	qctx, err := call.Admit()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	<-qctx.Done()
	out := call.Complete(qctx.Err())
	cleanup := call.Cleanup(false)
	if out.StatementDone || cleanup.RolledBack || time.Since(start) > 12*time.Second {
		t.Fatalf("out=%+v cleanup=%+v wait=%v", out, cleanup, time.Since(start))
	}
}

func TestWriteGuardTruncationBeforeWorker(t *testing.T) {
	call, _ := guardFixture(t, context.Background(), func(context.Context, string) ([][]driver.Value, error) { return activeTarget(), nil })
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	call.RequestTruncation()
	out := call.Complete(nil)
	if out.Reason != stopRowCap || call.CommitAllowed() {
		t.Fatalf("lost truncation at completion: %+v", out)
	}
}
func TestWriteGuardUnknownThread(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	killed := make(chan struct{})
	call, _ := guardFixture(t, ctx, func(_ context.Context, q string) ([][]driver.Value, error) {
		if strings.HasPrefix(q, "KILL") {
			close(killed)
			return nil, &gomysql.MySQLError{Number: 1094, Message: "Unknown thread"}
		}
		return activeTarget(), nil
	})
	qctx, err := call.Admit()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitGuard(t, killed)
	if qctx.Err() != nil {
		t.Fatal("unknown-thread reply closed main before response")
	}
	if out := call.Complete(interrupted()); !out.StatementDone {
		t.Fatal("actual main response not recognized")
	}
}
func TestWriteGuardSocketFailure(t *testing.T) {
	call, _ := guardFixture(t, context.Background(), func(context.Context, string) ([][]driver.Value, error) {
		t.Error("kill after local completion")
		return nil, nil
	})
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	if out := call.Complete(io.EOF); out.StatementDone {
		t.Fatal("socket EOF counted as server response")
	}
	if r := call.Cleanup(false); r.RolledBack {
		t.Fatal("claimed rollback on discarded socket")
	}
}

func TestWriteGuardCommitRequiresAdmission(t *testing.T) {
	call, s := guardFixture(t, context.Background(), func(context.Context, string) ([][]driver.Value, error) {
		t.Error("unexpected guard SQL")
		return nil, nil
	})
	if _, err := call.Admit(); err != nil {
		t.Fatal(err)
	}
	call.Complete(nil)
	r := call.Cleanup(true)
	if r.Committed || !r.RolledBack || r.Err == nil {
		t.Fatalf("committed without commit-start decision: %+v", r)
	}
	for _, e := range s.snapshot() {
		if e == "1:COMMIT" {
			t.Fatal("unauthorized COMMIT")
		}
	}
}
