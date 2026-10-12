package catalog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
)

func counting(calls *atomic.Int32) Builder {
	return func(context.Context) (*engine.Catalog, error) {
		calls.Add(1)
		return &engine.Catalog{}, nil
	}
}

func TestBuildsOnceForConcurrentCallers(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	build := func(ctx context.Context) (*engine.Catalog, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return &engine.Catalog{}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Get(context.Background(), "a", 1, build); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("built %d times", n)
	}
}

func TestTTLVersionAndInvalidate(t *testing.T) {
	c := New(10 * time.Minute)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }
	var calls atomic.Int32
	b := counting(&calls)
	ctx := context.Background()
	step := func(version int64, want int32) {
		t.Helper()
		if _, err := c.Get(ctx, "a", version, b); err != nil {
			t.Fatal(err)
		}
		if got := calls.Load(); got != want {
			t.Fatalf("builds = %d, want %d", got, want)
		}
	}
	step(1, 1)
	step(1, 1)
	now = now.Add(11 * time.Minute)
	step(1, 2)
	step(2, 3)
	c.Invalidate("a")
	step(2, 4)
}

func TestErrorsAreNotCached(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	fail := func(context.Context) (*engine.Catalog, error) { calls.Add(1); return nil, errors.New("boom") }
	for i := 0; i < 2; i++ {
		if _, err := c.Get(context.Background(), "a", 1, fail); err == nil {
			t.Fatal("expected error")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestCallerCancelDoesNotFailOthers(t *testing.T) {
	c := New(time.Minute)
	release := make(chan struct{})
	build := func(ctx context.Context) (*engine.Catalog, error) {
		<-release
		return &engine.Catalog{}, ctx.Err()
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	errs := make(chan error, 2)
	go func() { _, err := c.Get(ctx1, "a", 1, build); errs <- err }()
	time.Sleep(20 * time.Millisecond)
	go func() { _, err := c.Get(context.Background(), "a", 1, build); errs <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel1()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller err = %v", err)
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("second caller err = %v", err)
	}
}

func TestInvalidateDuringBuildDiscardsResult(t *testing.T) {
	c := New(time.Minute)
	var calls atomic.Int32
	release := make(chan struct{})
	slow := func(context.Context) (*engine.Catalog, error) {
		calls.Add(1)
		<-release
		return &engine.Catalog{}, nil
	}
	done := make(chan struct{})
	go func() { c.Get(context.Background(), "a", 1, slow); close(done) }()
	time.Sleep(20 * time.Millisecond)
	c.Invalidate("a")
	close(release)
	<-done
	if _, err := c.Get(context.Background(), "a", 1, counting(&calls)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("stale build was cached; calls = %d", calls.Load())
	}
}

func TestInvalidateAll(t *testing.T) {
	c := New(10 * time.Minute)
	var calls atomic.Int32
	b := counting(&calls)
	ctx := context.Background()
	get := func(id string, want int32) {
		t.Helper()
		if _, err := c.Get(ctx, id, 1, b); err != nil {
			t.Fatal(err)
		}
		if got := calls.Load(); got != want {
			t.Fatalf("builds = %d, want %d", got, want)
		}
	}
	get("a", 1)
	get("b", 2)
	get("a", 2)
	get("b", 2)
	c.InvalidateAll()
	get("a", 3)
	get("b", 4)
}
