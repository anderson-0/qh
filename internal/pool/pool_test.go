package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

type fakeConn struct{ closed atomic.Bool }

func (f *fakeConn) Query(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return &engine.Result{}, nil
}
func (f *fakeConn) Explain(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return &engine.Result{}, nil
}
func (f *fakeConn) Introspect(context.Context) (*engine.Catalog, error) {
	return &engine.Catalog{}, nil
}
func (f *fakeConn) Ping(context.Context) (string, error) { return "fake", nil }
func (f *fakeConn) Close() error                         { f.closed.Store(true); return nil }

type opener struct {
	mu    sync.Mutex
	opens map[string]int
	fail  map[string]error
	gate  chan struct{}
}

func newOpener() *opener { return &opener{opens: map[string]int{}, fail: map[string]error{}} }

func (o *opener) open(ctx context.Context, s engine.ConnSpec) (engine.Conn, error) {
	if o.gate != nil {
		<-o.gate
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens[s.ID]++
	if err := o.fail[s.ID]; err != nil {
		return nil, err
	}
	return &fakeConn{}, nil
}

func (o *opener) count(id string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens[id]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func spec(id string, version int64, size int) engine.ConnSpec {
	return engine.ConnSpec{ID: id, Version: version, PoolSize: size}
}

func TestReuseAndVersionSwap(t *testing.T) {
	o := newOpener()
	m := New(o.open, Options{})
	ctx := context.Background()
	c1, rel1, err := m.Get(ctx, spec("a", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	c2, rel2, _ := m.Get(ctx, spec("a", 1, 2))
	if c1 != c2 || o.count("a") != 1 {
		t.Fatal("same version should reuse the pool")
	}
	rel2()
	c3, rel3, _ := m.Get(ctx, spec("a", 2, 2))
	if c3 == c1 || o.count("a") != 2 {
		t.Fatal("new version should open a new pool")
	}
	if c1.(*fakeConn).closed.Load() {
		t.Fatal("old pool closed while still in use")
	}
	rel1()
	rel1() // second call is a no-op
	if !c1.(*fakeConn).closed.Load() {
		t.Fatal("old pool not closed after last release")
	}
	rel3()
}

func TestIdleSweep(t *testing.T) {
	o := newOpener()
	clk := &clock{t: time.Unix(0, 0)}
	m := New(o.open, Options{IdleTimeout: 10 * time.Minute, Now: clk.now})
	ctx := context.Background()
	c, rel, _ := m.Get(ctx, spec("a", 1, 1))
	clk.advance(11 * time.Minute)
	m.Sweep()
	if c.(*fakeConn).closed.Load() {
		t.Fatal("swept an in-use pool")
	}
	rel()
	clk.advance(9 * time.Minute)
	m.Sweep()
	if c.(*fakeConn).closed.Load() {
		t.Fatal("swept before idle timeout")
	}
	clk.advance(2 * time.Minute)
	m.Sweep()
	if !c.(*fakeConn).closed.Load() {
		t.Fatal("idle pool not closed")
	}
	if _, rel, _ := m.Get(ctx, spec("a", 1, 1)); o.count("a") != 2 {
		t.Fatal("expected reopen after sweep")
	} else {
		rel()
	}
}

func TestBackoffAndRecovery(t *testing.T) {
	o := newOpener()
	clk := &clock{t: time.Unix(0, 0)}
	m := New(o.open, Options{Now: clk.now})
	ctx := context.Background()
	down := qerr.New(qerr.ConnectionFailed, "cannot reach the database")
	o.fail["a"] = down
	if _, _, err := m.Get(ctx, spec("a", 1, 1)); !errors.Is(err, down) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := m.Get(ctx, spec("a", 1, 1)); !errors.Is(err, down) || o.count("a") != 1 {
		t.Fatalf("within backoff: err %v, opens %d", err, o.count("a"))
	}
	clk.advance(1100 * time.Millisecond)
	m.Get(ctx, spec("a", 1, 1))
	if o.count("a") != 2 {
		t.Fatalf("opens = %d after first backoff", o.count("a"))
	}
	clk.advance(1100 * time.Millisecond) // second backoff is 2s
	m.Get(ctx, spec("a", 1, 1))
	if o.count("a") != 2 {
		t.Fatal("backoff did not double")
	}
	clk.advance(time.Second)
	delete(o.fail, "a")
	_, rel, err := m.Get(ctx, spec("a", 1, 1))
	if err != nil || o.count("a") != 3 {
		t.Fatalf("recovery: %v, opens %d", err, o.count("a"))
	}
	rel()
	if _, _, err := m.Get(ctx, spec("a", 2, 1)); err != nil {
		t.Fatalf("new version should not inherit backoff: %v", err)
	}
}

func TestFailureIsolated(t *testing.T) {
	o := newOpener()
	m := New(o.open, Options{})
	o.fail["down"] = qerr.New(qerr.ConnectionFailed, "down")
	if _, _, err := m.Get(context.Background(), spec("down", 1, 1)); err == nil {
		t.Fatal("expected failure")
	}
	if _, rel, err := m.Get(context.Background(), spec("up", 1, 1)); err != nil {
		t.Fatalf("healthy connection affected: %v", err)
	} else {
		rel()
	}
}

func TestConcurrentGetOpensOnce(t *testing.T) {
	o := newOpener()
	o.gate = make(chan struct{})
	m := New(o.open, Options{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, rel, err := m.Get(context.Background(), spec("a", 1, 1))
			if err != nil {
				t.Error(err)
				return
			}
			rel()
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(o.gate)
	wg.Wait()
	if o.count("a") != 1 {
		t.Fatalf("opened %d times", o.count("a"))
	}
}

func TestGlobalCap(t *testing.T) {
	o := newOpener()
	clk := &clock{t: time.Unix(0, 0)}
	m := New(o.open, Options{MaxTotalConns: 10, Now: clk.now})
	ctx := context.Background()
	a, relA, _ := m.Get(ctx, spec("a", 1, 5))
	clk.advance(time.Second)
	_, relB, _ := m.Get(ctx, spec("b", 1, 5))
	relA()
	_, relC, err := m.Get(ctx, spec("c", 1, 5))
	if err != nil {
		t.Fatalf("should evict idle pool a: %v", err)
	}
	if !a.(*fakeConn).closed.Load() {
		t.Fatal("least recently used idle pool not closed")
	}
	_, _, err = m.Get(ctx, spec("d", 1, 5))
	if e := qerr.From(err); err == nil || e.Category != qerr.Busy {
		t.Fatalf("all pools busy: err = %v", err)
	}
	relB()
	relC()
}

func TestDropAndClose(t *testing.T) {
	o := newOpener()
	m := New(o.open, Options{})
	c, rel, _ := m.Get(context.Background(), spec("a", 1, 1))
	m.Drop("a")
	if c.(*fakeConn).closed.Load() {
		t.Fatal("Drop closed an in-use pool")
	}
	rel()
	if !c.(*fakeConn).closed.Load() {
		t.Fatal("dropped pool not closed after release")
	}
	c2, _, _ := m.Get(context.Background(), spec("b", 1, 1))
	m.Close()
	if !c2.(*fakeConn).closed.Load() {
		t.Fatal("Close did not close pools")
	}
	if _, _, err := m.Get(context.Background(), spec("b", 1, 1)); err == nil {
		t.Fatal("Get after Close succeeded")
	}
}

func TestCapCountsOpensInProgress(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 8)
	var calls atomic.Int32
	open := func(ctx context.Context, s engine.ConnSpec) (engine.Conn, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-gate
		return &fakeConn{}, nil
	}
	m := New(open, Options{MaxTotalConns: 10})
	ctx := context.Background()
	type result struct {
		rel func()
		err error
	}
	results := make(chan result, 2)
	for _, id := range []string{"a", "b"} {
		go func() {
			_, rel, err := m.Get(ctx, spec(id, 1, 5))
			results <- result{rel, err}
		}()
	}
	<-entered
	<-entered
	_, _, err := m.Get(ctx, spec("c", 1, 5))
	if err == nil || qerr.From(err).Category != qerr.Busy {
		t.Fatalf("cap ignored opens in progress: err = %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("opener called %d times; c must not reach it", n)
	}
	close(gate)
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		r.rel()
	}
	m.Close()
}

func TestCapCountsRetiredInUse(t *testing.T) {
	o := newOpener()
	m := New(o.open, Options{MaxTotalConns: 10})
	ctx := context.Background()
	_, relOld, err := m.Get(ctx, spec("a", 1, 5))
	if err != nil {
		t.Fatal(err)
	}
	_, relNew, err := m.Get(ctx, spec("a", 2, 5)) // retires a@1 while it is in use
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Get(ctx, spec("b", 1, 5)); err == nil || qerr.From(err).Category != qerr.Busy {
		t.Fatalf("retired in-use pool not counted: err = %v", err)
	}
	relOld()
	_, relB, err := m.Get(ctx, spec("b", 1, 5))
	if err != nil {
		t.Fatalf("should fit once the retired pool is released: %v", err)
	}
	relB()
	relNew()
	m.Close()
}

// waitContext signals when Get evaluates the pending-open wait's select.
// It lets tests release an opener only after every follower has joined it.
type waitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type getResult struct {
	conn     engine.Conn
	release  func()
	err      error
	panicked bool
}

// Capture an escaped panic without printing its potentially sensitive value.
func getResultFor(m *Manager, ctx context.Context, s engine.ConnSpec) (r getResult) {
	completed := false
	defer func() {
		if !completed {
			_ = recover()
			r.panicked = true
		}
	}()
	r.conn, r.release, r.err = m.Get(ctx, s)
	completed = true
	return r
}

func startGet(m *Manager, ctx context.Context, s engine.ConnSpec) <-chan getResult {
	result := make(chan getResult, 1)
	go func() { result <- getResultFor(m, ctx, s) }()
	return result
}

func receiveGet(t *testing.T, result <-chan getResult) getResult {
	t.Helper()
	select {
	case r := <-result:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("Get did not finish")
		return getResult{}
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("expected synchronization signal")
	}
}

func requireOpenFailure(t *testing.T, r getResult) {
	t.Helper()
	if r.panicked {
		t.Fatal("opener panic escaped Get")
	}
	if r.conn != nil || r.release != nil || r.err == nil {
		t.Fatal("failed open must return only an error")
	}
	qe := qerr.From(r.err)
	if qe.Category != qerr.ConnectionFailed || r.err.Error() != "error[connection_failed]: could not open the database connection" {
		t.Fatal("opener panic did not return the safe connection failure")
	}
	if errors.Unwrap(r.err) != nil {
		t.Fatal("opener panic retained an underlying cause")
	}
}

func joinedGet(t *testing.T, m *Manager, s engine.ConnSpec) <-chan getResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	observed := &waitContext{Context: ctx, waiting: make(chan struct{})}
	result := startGet(m, observed, s)
	awaitSignal(t, observed.waiting)
	return result
}

// Missing recovery must neither strand same-version callers nor reserve the cap.
func TestOpenerPanicWakesWaiters(t *testing.T) {
	entered, gate := make(chan struct{}), make(chan struct{})
	clk := &clock{t: time.Unix(0, 0)}
	var calls atomic.Int32
	m := New(func(ctx context.Context, s engine.ConnSpec) (engine.Conn, error) {
		calls.Add(1)
		if s.ID == "a" {
			close(entered)
			<-gate
			panic("synthetic private opener detail")
		}
		return &fakeConn{}, nil
	}, Options{MaxTotalConns: 1, Now: clk.now})
	t.Cleanup(m.Close)
	leader := startGet(m, context.Background(), spec("a", 1, 1))
	awaitSignal(t, entered)
	waiters := make([]<-chan getResult, 4)
	for i := range waiters {
		waiters[i] = joinedGet(t, m, spec("a", 1, 1))
	}
	close(gate)
	r := receiveGet(t, leader)
	requireOpenFailure(t, r)
	for _, waiter := range waiters {
		w := receiveGet(t, waiter)
		requireOpenFailure(t, w)
		if w.err != r.err {
			t.Fatal("waiter did not receive the cached failure")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("waiters reopened during backoff")
	}
	_, release, err := m.Get(context.Background(), spec("b", 1, 1))
	if err != nil {
		t.Fatalf("panic left capacity reserved: %v", err)
	}
	release()
}

// Panics follow ordinary exponential backoff and permit eventual recovery.
func TestOpenerPanicBackoffAndRecovery(t *testing.T) {
	clk := &clock{t: time.Unix(0, 0)}
	var calls int
	m := New(func(context.Context, engine.ConnSpec) (engine.Conn, error) {
		calls++
		if calls <= 2 {
			panic("synthetic private opener detail")
		}
		return &fakeConn{}, nil
	}, Options{Now: clk.now, MaxTotalConns: 1})
	t.Cleanup(m.Close)
	s := spec("a", 1, 1)
	requireOpenFailure(t, getResultFor(m, context.Background(), s))
	clk.advance(999 * time.Millisecond)
	requireOpenFailure(t, getResultFor(m, context.Background(), s))
	if calls != 1 {
		t.Fatal("retried before the first backoff expired")
	}
	clk.advance(time.Millisecond)
	requireOpenFailure(t, getResultFor(m, context.Background(), s))
	clk.advance(1999 * time.Millisecond)
	requireOpenFailure(t, getResultFor(m, context.Background(), s))
	if calls != 2 {
		t.Fatal("second failure did not double backoff")
	}
	clk.advance(time.Millisecond)
	r := getResultFor(m, context.Background(), s)
	if r.panicked || r.err != nil || r.conn == nil || r.release == nil || calls != 3 {
		t.Fatal("connection did not recover after backoff")
	}
	r.release()
}

// Close retains its existing leader-error versus waiter-error precedence.
func TestOpenerPanicDuringClose(t *testing.T) {
	entered, gate := make(chan struct{}), make(chan struct{})
	m := New(func(context.Context, engine.ConnSpec) (engine.Conn, error) {
		close(entered)
		<-gate
		panic("synthetic private opener detail")
	}, Options{})
	t.Cleanup(m.Close)
	s := spec("a", 1, 1)
	leader := startGet(m, context.Background(), s)
	awaitSignal(t, entered)
	waiter := joinedGet(t, m, s)
	m.Close()
	close(gate)
	requireOpenFailure(t, receiveGet(t, leader))
	for _, r := range []getResult{receiveGet(t, waiter), getResultFor(m, context.Background(), s)} {
		if r.panicked || !errors.Is(r.err, errClosed) || r.conn != nil || r.release != nil {
			t.Fatal("closed manager accepted Get or stranded a waiter")
		}
	}
	m.mu.Lock()
	pending := len(m.opening)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatal("panic left a pending open after Close")
	}
}

// An older pending failure cannot retire a successfully opened newer version.
func TestOpenerPanicPreservesNewVersion(t *testing.T) {
	entered, gate := make(chan struct{}), make(chan struct{})
	newConn := &fakeConn{}
	m := New(func(ctx context.Context, s engine.ConnSpec) (engine.Conn, error) {
		if s.Version == 1 {
			close(entered)
			<-gate
			panic("synthetic private opener detail")
		}
		return newConn, nil
	}, Options{MaxTotalConns: 2})
	t.Cleanup(m.Close)
	old := startGet(m, context.Background(), spec("a", 1, 1))
	awaitSignal(t, entered)
	c, release, err := m.Get(context.Background(), spec("a", 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	release()
	close(gate)
	requireOpenFailure(t, receiveGet(t, old))
	c2, release2, err := m.Get(context.Background(), spec("a", 2, 1))
	if err != nil || c != c2 || newConn.closed.Load() {
		t.Fatal("older panic disrupted the newer connection")
	}
	release2()
}

// Canceled attempts must clear their reservation without adding backoff.
func TestOpenerPanicAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	m := New(func(context.Context, engine.ConnSpec) (engine.Conn, error) {
		calls++
		if calls == 1 {
			cancel()
			panic("synthetic private opener detail")
		}
		return &fakeConn{}, nil
	}, Options{MaxTotalConns: 1})
	t.Cleanup(m.Close)
	s := spec("a", 1, 1)
	requireOpenFailure(t, getResultFor(m, ctx, s))
	r := getResultFor(m, context.Background(), s)
	if r.panicked || r.err != nil || r.conn == nil || r.release == nil || calls != 2 {
		t.Fatal("canceled panic prevented an immediate retry")
	}
	r.release()
}

type privatePanic struct{ formatted atomic.Bool }

func (p *privatePanic) Error() string  { p.formatted.Store(true); return "synthetic private payload" }
func (p *privatePanic) String() string { p.formatted.Store(true); return "synthetic private payload" }

// Recovery must discard arbitrary values without invoking formatting methods.
func TestOpenerPanicDiscardsPayload(t *testing.T) {
	for _, name := range []string{"string", "error", "nil", "legacy nil"} {
		t.Run(name, func(t *testing.T) {
			if name == "legacy nil" {
				t.Setenv("GODEBUG", "panicnil=1")
			}
			payload := &privatePanic{}
			m := New(func(context.Context, engine.ConnSpec) (engine.Conn, error) {
				switch name {
				case "string":
					panic("synthetic private payload")
				case "error":
					panic(payload)
				default:
					panic(nil)
				}
			}, Options{})
			t.Cleanup(m.Close)
			requireOpenFailure(t, getResultFor(m, context.Background(), spec("a", 1, 1)))
			if payload.formatted.Load() {
				t.Fatal("panic payload was formatted")
			}
		})
	}
}
