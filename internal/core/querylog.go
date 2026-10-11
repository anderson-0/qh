package core

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const (
	DefaultLogLimit = 100
	MaxLogLimit     = 1000
	// maxLoggedQuery caps the statement text kept per entry.
	maxLoggedQuery = 8 << 10
)

type keyIDKey struct{}

// WithKeyID records which API key makes the calls in ctx, for the query log.
func WithKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyIDKey{}, id)
}

func keyIDOf(ctx context.Context) string {
	id, _ := ctx.Value(keyIDKey{}).(string)
	return id
}

// QueryLogEntry is one logged query call.
type QueryLogEntry struct {
	ID            int64
	Connection    string
	KeyID         string // "" for local calls
	Tool          string // "query" or "explain"
	Query         string
	Rows          int64 // rows returned, or rows changed by a write
	Duration      time.Duration
	ErrorCategory string // "" when the call succeeded
	At            time.Time
}

// QueryLogFilter selects entries. Connection "" and a zero Since mean no
// filter; Limit 0 means DefaultLogLimit.
type QueryLogFilter struct {
	Connection string
	Since      time.Time
	Limit      int
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// logQueueSize bounds the entries waiting to be written. When it is full,
// new entries are dropped rather than slowing queries.
const logQueueSize = 1024

// logWriter appends query-log entries in order, off the query path.
type logWriter struct {
	once sync.Once
	ch   chan logItem
	done chan struct{}
}

// logItem is an entry to append, or (e nil) a flush marker closed once every
// earlier entry is written.
type logItem struct {
	e     *store.QueryLogEntry
	flush chan struct{}
}

func (s *Service) logQueue() chan logItem {
	w := &s.log
	w.once.Do(func() {
		w.ch, w.done = make(chan logItem, logQueueSize), make(chan struct{})
		go func() {
			defer close(w.done)
			for it := range w.ch {
				if it.e == nil {
					close(it.flush)
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = s.store.AppendQueryLog(ctx, it.e)
				cancel()
			}
		}()
	})
	return w.ch
}

// flushLog waits until every entry logged so far is written.
func (s *Service) flushLog() {
	if s.opts.NoQueryLog {
		return
	}
	s.logMu.RLock()
	defer s.logMu.RUnlock()
	if s.logClosed {
		return
	}
	f := make(chan struct{})
	s.logQueue() <- logItem{flush: f}
	<-f
}

// Close writes the pending query-log entries. The Service must not be used
// afterwards.
func (s *Service) Close() {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logClosed {
		return
	}
	s.logClosed = true
	if s.log.ch != nil {
		close(s.log.ch)
		<-s.log.done
	}
}

// logQuery records one query call. The entry is written in the background;
// a failed write, or an entry that finds the queue full, is dropped: the log
// must never fail or slow a query.
func (s *Service) logQuery(ctx context.Context, ws string, t *target, in QueryInput, res *engine.Result, err error, d time.Duration) {
	if s.opts.NoQueryLog {
		return
	}
	e := &store.QueryLogEntry{
		WorkspaceID: ws, ConnectionID: t.rec.ID, ConnectionName: t.rec.Name, KeyID: keyIDOf(ctx),
		Tool: "query", Query: truncateUTF8(in.Query, maxLoggedQuery), DurationMS: d.Milliseconds(),
	}
	if in.Explain {
		e.Tool = "explain"
	}
	switch {
	case err != nil:
		e.ErrorCategory = string(qerr.From(err).Category)
	case res.RowsAffected >= 0:
		e.RowCount = res.RowsAffected
	default:
		e.RowCount = int64(len(res.Rows))
	}
	s.logMu.RLock()
	defer s.logMu.RUnlock()
	if s.logClosed {
		return
	}
	select {
	case s.logQueue() <- logItem{e: e}:
	default:
	}
}

// QueryLog returns the workspace's logged query calls, newest first.
func (s *Service) QueryLog(ctx context.Context, ws string, f QueryLogFilter) ([]*QueryLogEntry, error) {
	if f.Limit == 0 {
		f.Limit = DefaultLogLimit
	}
	if f.Limit < 0 || f.Limit > MaxLogLimit {
		return nil, qerr.New(qerr.Invalid, "limit must be between 1 and 1000")
	}
	s.flushLog()
	rows, err := s.store.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: ws, ConnectionName: f.Connection, Since: f.Since, Limit: f.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]*QueryLogEntry, len(rows))
	for i, r := range rows {
		out[i] = &QueryLogEntry{ID: r.ID, Connection: r.ConnectionName, KeyID: r.KeyID, Tool: r.Tool, Query: r.Query,
			Rows: r.RowCount, Duration: time.Duration(r.DurationMS) * time.Millisecond, ErrorCategory: r.ErrorCategory, At: r.At}
	}
	return out, nil
}

// PruneQueryLog deletes entries older than retention, in every workspace.
func (s *Service) PruneQueryLog(ctx context.Context, retention time.Duration) (int64, error) {
	return s.store.PruneQueryLog(ctx, time.Now().Add(-retention))
}

// ParseSince reads a query-log lower bound: an RFC 3339 time, or a duration
// back from now such as 90m, 24h or 7d.
func ParseSince(v string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if d, err := auth.ParseExpiry(v); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, qerr.New(qerr.Invalid, "since must be an RFC 3339 time or a duration such as 24h or 7d")
}
