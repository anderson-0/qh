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
