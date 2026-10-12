package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/store"
)

type invStore struct {
	*store.SQLite
	mu   sync.Mutex
	sent []string
	ch   chan string
}

func (s *invStore) Invalidate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, id)
	return nil
}

func (s *invStore) Invalidations(ctx context.Context) (<-chan string, error) {
	out := make(chan string)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-s.ch:
				out <- id
			}
		}
	}()
	return out, nil
}

func TestServiceSharesInvalidations(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRW)
	inv := &invStore{SQLite: h.st, ch: make(chan string)}
	cache := catalog.New(time.Hour)
	svc := New(inv, h.svc.keys, pool.New(OpenConn, pool.Options{}), cache)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	if _, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); err != nil { // fills the cache
		t.Fatal(err)
	}
	// A write through this instance tells the others.
	if _, err := svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "CREATE TABLE t1 (x int)"}); err != nil {
		t.Fatal(err)
	}
	inv.mu.Lock()
	sent := append([]string(nil), inv.sent...)
	inv.mu.Unlock()
	if len(sent) == 0 {
		t.Fatal("no invalidation sent")
	}
	// Another instance's DDL: the notice drops our cached schema.
	if _, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	other := New(h.st, h.svc.keys, pool.New(OpenConn, pool.Options{}), catalog.New(time.Hour))
	if _, err := other.Query(ctx, ws, QueryInput{Source: "shop", Query: "CREATE TABLE t2 (x int)"}); err != nil {
		t.Fatal(err)
	}
	rec, _ := h.st.GetConnection(ctx, ws, "shop")
	inv.ch <- rec.ID
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop", Object: "t2"})
		if err == nil && out != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("t2 never appeared: %q %v", out, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	inv.ch <- "" // drop everything: must not panic or block
}
