package core

import (
	"context"
	"slices"
	"strings"
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
	rec, err := h.st.GetConnection(ctx, ws, "shop")
	if err != nil {
		t.Fatal(err)
	}
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
	if !slices.Contains(sent, rec.ID) {
		t.Fatalf("sent = %q, want it to contain %q", sent, rec.ID)
	}

	other := New(h.st, h.svc.keys, pool.New(OpenConn, pool.Options{}), catalog.New(time.Hour))
	t.Cleanup(other.Close)
	// otherCreates has another instance create table, checks that our cached
	// schema does not show it yet, delivers notice, and waits for it to appear.
	otherCreates := func(table, notice string) {
		t.Helper()
		if _, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); err != nil { // warm the cache
			t.Fatal(err)
		}
		if _, err := other.Query(ctx, ws, QueryInput{Source: "shop", Query: "CREATE TABLE " + table + " (x int)"}); err != nil {
			t.Fatal(err)
		}
		out, err := svc.Schema(ctx, ws, SchemaInput{Source: "shop", Object: table})
		if err != nil || !strings.Contains(out, "no objects match") {
			t.Fatalf("cached schema already shows %s before the notice: %q %v", table, out, err)
		}
		inv.ch <- notice
		deadline := time.Now().Add(5 * time.Second)
		for {
			out, err = svc.Schema(ctx, ws, SchemaInput{Source: "shop", Object: table})
			if err == nil && strings.Contains(out, table) && !strings.Contains(out, "no objects match") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never appeared after notice %q: %q %v", table, notice, out, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	otherCreates("t2", rec.ID) // another instance's DDL on this connection
	otherCreates("t3", "")     // a reconnect: drop everything
}

func TestPasswordPatchSharesInvalidationOnlyAfterSave(t *testing.T) {
	h, e := passwordFixture(t)
	ctx := context.Background()
	inv := &invStore{SQLite: h.st, ch: make(chan string)}
	svc := passwordSharedService(t, h, inv)
	before, _ := passwordRecord(t, h)
	failed := "rejected-password"
	if _, _, err := svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: &failed}, true); err == nil {
		t.Fatal("failed password test was accepted")
	}
	inv.mu.Lock()
	n := len(inv.sent)
	inv.mu.Unlock()
	if n != 0 {
		t.Fatal("failed password edit notified shared instances")
	}
	passwordUnchanged(t, h, before)
	next := "replacement-password"
	if _, _, err := svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: &next}, true); err != nil {
		t.Fatal("password edit failed")
	}
	inv.mu.Lock()
	sent := append([]string(nil), inv.sent...)
	inv.mu.Unlock()
	if len(sent) != 1 || sent[0] != before.ID {
		t.Fatal("successful password edit did not notify exactly once")
	}
	_, in := passwordRecord(t, h)
	passwordWant(t, e, in.DSN, next)
}
