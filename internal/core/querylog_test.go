package core

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestQueryLogRecordsCalls(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := WithKeyID(context.Background(), "key-1")
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT id FROM users"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "DELETE FROM users"}); err == nil {
		t.Fatal("write on ro")
	}
	if _, err := h.svc.Query(context.Background(), ws, QueryInput{Source: "shop", Query: "SELECT 1", Explain: true}); err != nil {
		t.Fatal(err)
	}
	h.svc.Query(ctx, ws, QueryInput{Source: "nope", Query: "SELECT 1"}) // unknown connections are not logged
	long := "SELECT '" + strings.Repeat("é", 5000) + "'"
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: long}); err != nil {
		t.Fatal(err)
	}

	got, err := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{})
	if err != nil || len(got) != 4 {
		t.Fatalf("log: %v %+v", err, got)
	}
	if len(got[0].Query) > 8192 || !utf8.ValidString(got[0].Query) || !strings.HasPrefix(got[0].Query, "SELECT 'é") {
		t.Fatalf("long query kept as %d bytes", len(got[0].Query))
	}
	if got[1].Tool != "explain" || got[1].KeyID != "" {
		t.Fatalf("explain: %+v", got[1])
	}
	if got[2].ErrorCategory != "read_only" || got[2].Rows != 0 {
		t.Fatalf("read_only: %+v", got[2])
	}
	if e := got[3]; e.Tool != "query" || e.Rows != 3 || e.KeyID != "key-1" || e.Connection != "shop" || e.ErrorCategory != "" ||
		e.Query != "SELECT id FROM users" || time.Since(e.At) > time.Minute {
		t.Fatalf("query: %+v", e)
	}
	if one, _ := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{Limit: 1}); len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}
	if none, _ := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{Connection: "crm"}); len(none) != 0 {
		t.Fatalf("filter: %+v", none)
	}
	if _, err := h.svc.QueryLog(context.Background(), ws, QueryLogFilter{Limit: 1001}); !strings.HasPrefix(errText(err), "error[invalid]: limit must be between 1 and 1000") {
		t.Fatalf("limit check: %q", errText(err))
	}
	if _, err := h.svc.CreateWorkspace(context.Background(), "team"); err != nil {
		t.Fatal(err)
	}
	if other, _ := h.svc.QueryLog(context.Background(), "team", QueryLogFilter{}); len(other) != 0 {
		t.Fatalf("another workspace sees: %+v", other)
	}
}

func TestQueryLogDisabledAndPruned(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	off := newServiceWith(t, st, Options{NoQueryLog: true})
	if _, _, err := off.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t)}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := off.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := off.QueryLog(ctx, ws, QueryLogFilter{}); len(got) != 0 {
		t.Fatalf("logged while disabled: %+v", got)
	}
	old := &store.QueryLogEntry{WorkspaceID: ws, ConnectionID: "c", ConnectionName: "shop", Tool: "query", Query: "SELECT 1", At: time.Now().Add(-48 * time.Hour)}
	recent := &store.QueryLogEntry{WorkspaceID: ws, ConnectionID: "c", ConnectionName: "shop", Tool: "query", Query: "SELECT 2", At: time.Now()}
	for _, e := range []*store.QueryLogEntry{old, recent} {
		if err := st.AppendQueryLog(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := off.PruneQueryLog(ctx, 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

type failingLog struct{ *store.SQLite }

func (failingLog) AppendQueryLog(context.Context, *store.QueryLogEntry) error {
	return errors.New("disk full")
}

func TestQueryLogFailureDoesNotFailQuery(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	svc := New(failingLog{h.st}, h.svc.keys, pool.New(OpenConn, pool.Options{}), catalog.New(time.Minute))
	if out, err := svc.Query(context.Background(), ws, QueryInput{Source: "shop", Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("%q %v", out, err)
	}
}

// blockingLog holds every log write until release is closed.
type blockingLog struct {
	*store.SQLite
	release chan struct{}
}

func (b blockingLog) AppendQueryLog(ctx context.Context, e *store.QueryLogEntry) error {
	<-b.release
	return b.SQLite.AppendQueryLog(ctx, e)
}

func TestSlowQueryLogDoesNotDelayQueries(t *testing.T) {
	// Writing a full queue (logQueueSize entries) can take longer than
	// closeWait under -race on a busy CI runner; the test checks what is
	// written, not how fast.
	defer func(d time.Duration) { closeWait = d }(closeWait)
	closeWait = 2 * time.Minute
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	bl := blockingLog{h.st, make(chan struct{})}
	var logged bytes.Buffer
	svc := New(bl, h.svc.keys, pool.New(OpenConn, pool.Options{}), catalog.New(time.Minute),
		Options{Logger: slog.New(slog.NewTextHandler(&logged, nil))})
	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		// More calls than the queue holds: the surplus entries are dropped.
		for range logQueueSize + 10 {
			if _, err := svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT 1"}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("queries waited for the query log")
	}
	close(bl.release)
	svc.Close() // writes what was queued
	got, err := h.st.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: ws, Limit: 2000})
	if err != nil || len(got) < logQueueSize || len(got) > logQueueSize+1 {
		t.Fatalf("logged %d entries, %v", len(got), err)
	}
	if !strings.Contains(logged.String(), "entries were not recorded") {
		t.Fatalf("no warning about dropped entries: %q", logged.String())
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2026-10-01T00:00:00Z": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"90m":                  now.Add(-90 * time.Minute),
		"7d":                   now.Add(-7 * 24 * time.Hour),
	} {
		if got, err := ParseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ParseSince("yesterday", now); !strings.HasPrefix(errText(err), "error[invalid]: since must be an RFC 3339 time or a duration such as 24h or 7d") {
		t.Fatalf("%q", errText(err))
	}
}
