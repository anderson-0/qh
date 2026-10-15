package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func preparationScript(t *testing.T) *scriptConnector {
	t.Helper()
	return &scriptConnector{handle: func(_ context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
		switch {
		case q == "SET test_limits", q == "START TRANSACTION", q == "UNLOCK TABLES", q == "ROLLBACK", q == "COMMIT", strings.HasPrefix(q, "SET @qh_released"):
			return nil, nil
		case strings.Contains(q, "GET_LOCK(?, 0)"):
			if id != 1 || len(a) != 1 {
				t.Error("marker not acquired on query")
			}
			name, _ := a[0].Value.(string)
			if len(name) != 42 || !strings.HasPrefix(name, "qh_cancel_") {
				t.Error("invalid marker")
			}
			return [][]driver.Value{{int64(41), "server-a", int64(1)}}, nil
		case strings.Contains(q, "IS_USED_LOCK(?)"):
			if id != 2 {
				t.Error("identity check not on held control")
			}
			return [][]driver.Value{{"server-a", int64(41)}}, nil
		default:
			t.Errorf("unexpected SQL: %q", q)
			return nil, errors.New("unexpected SQL")
		}
	}}
}
func TestWriteControlPreparation(t *testing.T) {
	s := preparationScript(t)
	owner := &conn{db: scriptDB(t, s)}
	w := newWriteCanceler(owner, s, 1)
	defer w.Close()
	call, err := w.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer call.Cleanup(false)
	sc, err := owner.db.Conn(call.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Prepare(sc, "SET test_limits"); err != nil {
		t.Fatal(err)
	}
	r := call.Cleanup(false)
	if !r.RolledBack || r.Err != nil {
		t.Fatalf("cleanup: %+v", r)
	}
	if owner.db.Stats().InUse != 0 {
		t.Fatal("query still reserved")
	}
	call.Cleanup(false)
	s.mu.Lock()
	opened := s.next
	s.mu.Unlock()
	if opened != 2 {
		t.Fatalf("opened %d sessions, want exactly two", opened)
	}
	events := s.snapshot()
	if len(events) != 10 || !strings.HasPrefix(events[0], "2:START TRANSACTION") || !strings.HasPrefix(events[1], "1:SET test_limits") || !strings.HasPrefix(events[2], "1:START TRANSACTION") || !strings.Contains(events[3], "1:SELECT /* qh_cancel_") || !strings.HasPrefix(events[4], "2:SELECT @@server_uuid, IS_USED_LOCK") || events[5] != "1:UNLOCK TABLES" || events[6] != "1:ROLLBACK" || !strings.HasPrefix(events[7], "1:SET @qh_released") || events[8] != "2:ROLLBACK" || !strings.HasPrefix(events[9], "2:SET @qh_released") {
		t.Fatalf("unexpected session/order: %v", events)
	}
	for _, e := range s.snapshot() {
		if strings.Contains(e, "KILL") {
			t.Fatal("normal call issued kill")
		}
	}
}
func TestWritePreparationFailure(t *testing.T) {
	for _, failure := range []string{"setup", "begin", "main-begin", "marker-error", "uuid", "lock-zero", "lock-null", "control"} {
		t.Run(failure, func(t *testing.T) {
			s := preparationScript(t)
			base := s.handle
			s.handle = func(ctx context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
				switch {
				case failure == "setup" && q == "SET test_limits", failure == "begin" && q == "START TRANSACTION", failure == "control" && id == 2, failure == "main-begin" && id == 1 && q == "START TRANSACTION", failure == "marker-error" && strings.Contains(q, "GET_LOCK"):
					return nil, errors.New("private-control-detail")
				case failure == "uuid" && strings.Contains(q, "IS_USED_LOCK"):
					return [][]driver.Value{{"wrong-server", int64(41)}}, nil
				case strings.HasPrefix(failure, "lock-") && strings.Contains(q, "GET_LOCK"):
					var lock driver.Value = int64(0)
					if failure == "lock-null" {
						lock = nil
					}
					return [][]driver.Value{{int64(41), "server-a", lock}}, nil
				}
				return base(ctx, id, q, a)
			}
			owner := &conn{db: scriptDB(t, s)}
			w := newWriteCanceler(owner, s, 1)
			defer w.Close()
			call, err := w.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer call.Cleanup(false)
			sc, err := owner.db.Conn(call.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = call.Prepare(sc, "SET test_limits")
			if err == nil {
				t.Fatal("admitted failed preparation")
			}
			if strings.Contains(err.Error(), "private-control-detail") {
				t.Fatal("raw control error leaked")
			}
			call.Cleanup(false)
			if owner.db.Stats().InUse != 0 {
				t.Fatal("query leaked")
			}
		})
	}
}
func TestWriteCloseDuringPreparation(t *testing.T) {
	s := preparationScript(t)
	entered := make(chan struct{})
	s.connect = func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
	owner := &conn{db: scriptDB(t, s)}
	w := newWriteCanceler(owner, s, 1)
	call, err := w.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = owner.db.Conn(call.Context()); call.Cleanup(false) }()
	<-entered
	closed := make(chan struct{})
	go func() { w.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel/join preparation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("preparing call not canceled")
	}
	if _, err := w.Begin(context.Background()); err == nil {
		t.Fatal("registered after Close")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestWriteControlNoEscalation(t *testing.T) {
	s := preparationScript(t)
	owner := &conn{db: scriptDB(t, s)}
	w := newWriteCanceler(owner, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if call, err := w.Begin(ctx); err == nil {
		call.Cleanup(false)
		t.Fatal("registered canceled call")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call, err := w.Begin(context.Background()); err == nil {
				call.Cleanup(false)
			}
		}()
	}
	w.Close()
	wg.Wait()
	if len(s.snapshot()) != 0 {
		t.Fatal("preparation-free lifecycle issued SQL")
	}
}
func TestWriteFinishAcknowledgement(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			s := preparationScript(t)
			base := s.handle
			s.handle = func(ctx context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
				if strings.HasPrefix(q, "SET @qh_released") {
					return nil, errors.New("reset failed")
				}
				return base(ctx, id, q, a)
			}
			owner := &conn{db: scriptDB(t, s)}
			sc, _ := owner.db.Conn(context.Background())
			r := owner.finishContext(context.Background(), sc, commit, true)
			if r.Committed != commit || r.RolledBack == commit || r.Err == nil {
				t.Fatalf("acknowledgement lost: %+v", r)
			}
			sc.Close()
			if owner.db.Stats().OpenConnections != 0 {
				t.Fatal("uncertain reset pooled")
			}
			sc, _ = owner.db.Conn(context.Background())
			err := owner.finish(sc, commit, true)
			sc.Close()
			if (err == nil) != commit {
				t.Fatalf("legacy result: %v", err)
			}
		})
	}
}

func TestWriteControlCapacity(t *testing.T) {
	s := preparationScript(t)
	owner := &conn{db: scriptDB(t, s)}
	w := newWriteCanceler(owner, s, 1)
	defer w.Close()
	held, err := w.control.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	call, err := w.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer call.Cleanup(false)
	sc, err := owner.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = call.Prepare(sc, "SET test_limits"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capacity error = %v", err)
	}
	call.Cleanup(false)
	for _, e := range s.snapshot() {
		if strings.Contains(e, "SET test_limits") || strings.Contains(e, "GET_LOCK") {
			t.Fatal("prepared without control capacity")
		}
	}
}
