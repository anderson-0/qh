// Package pool keeps one open engine.Conn per connection version, closes idle
// ones, caps the total number of database connections, and backs off from
// databases that cannot be reached.
package pool

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// Opener opens a connection.
type Opener func(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error)

// Options configures a Manager. Zero values select the defaults.
type Options struct {
	IdleTimeout   time.Duration // default 10 minutes
	MaxTotalConns int           // default 500
	Now           func() time.Time
}

type handle struct {
	conn     engine.Conn
	version  int64
	size     int
	inflight int
	lastUsed time.Time
	retired  bool
}

type failure struct {
	version int64
	count   int
	retryAt time.Time
	err     error
}

// pendingOpen is an open in progress. Its size counts toward the cap.
type pendingOpen struct {
	size int
	done chan struct{}
}

// Manager hands out pooled connections.
type Manager struct {
	open Opener
	opt  Options

	mu           sync.Mutex
	closed       bool
	handles      map[string]*handle      // by connection id
	failures     map[string]*failure     // by connection id
	opening      map[string]*pendingOpen // by id@version
	retiredInUse int                     // sizes of retired pools still in use
}

var errClosed = errors.New("pool: manager closed")

// New returns a Manager.
func New(open Opener, opt Options) *Manager {
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 10 * time.Minute
	}
	if opt.MaxTotalConns <= 0 {
		opt.MaxTotalConns = 500
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Manager{
		open: open, opt: opt,
		handles: map[string]*handle{}, failures: map[string]*failure{}, opening: map[string]*pendingOpen{},
	}
}

// Get returns an open connection for spec and a release function.
func (m *Manager) Get(ctx context.Context, spec engine.ConnSpec) (engine.Conn, func(), error) {
	key := spec.ID + "@" + strconv.FormatInt(spec.Version, 10)
	size := max(spec.PoolSize, 1)
	for {
		var toClose []engine.Conn
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, nil, errClosed
		}
		now := m.opt.Now()
		if h := m.handles[spec.ID]; h != nil {
			if h.version == spec.Version {
				h.inflight++
				h.lastUsed = now
				m.mu.Unlock()
				return h.conn, m.releaser(h), nil
			}
			delete(m.handles, spec.ID)
			toClose = append(toClose, m.retire(h)...)
		}
		if f := m.failures[spec.ID]; f != nil && f.version == spec.Version && now.Before(f.retryAt) {
			err := f.err
			m.mu.Unlock()
			closeAll(toClose)
			return nil, nil, err
		}
		if wait, ok := m.opening[key]; ok {
			m.mu.Unlock()
			closeAll(toClose)
			select {
			case <-wait.done:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		evicted, err := m.makeRoom(size)
		toClose = append(toClose, evicted...)
		if err != nil {
			m.mu.Unlock()
			closeAll(toClose)
			return nil, nil, err
		}
		done := make(chan struct{})
		m.opening[key] = &pendingOpen{size: size, done: done}
		m.mu.Unlock()
		closeAll(toClose)

		conn, err := m.open(ctx, spec)

		m.mu.Lock()
		delete(m.opening, key)
		close(done)
		if err != nil {
			if ctx.Err() == nil {
				m.recordFailure(spec, err)
			}
			m.mu.Unlock()
			return nil, nil, err
		}
		if m.closed {
			m.mu.Unlock()
			conn.Close()
			return nil, nil, errClosed
		}
		delete(m.failures, spec.ID)
		var late []engine.Conn
		if old := m.handles[spec.ID]; old != nil {
			late = m.retire(old)
		}
		h := &handle{conn: conn, version: spec.Version, size: size, inflight: 1, lastUsed: m.opt.Now()}
		m.handles[spec.ID] = h
		m.mu.Unlock()
		closeAll(late)
		return conn, m.releaser(h), nil
	}
}

// retire marks h as replaced and returns its conn if nobody is using it. A
// pool still in use keeps counting toward the cap until its last release. It
// must be called with m.mu held.
func (m *Manager) retire(h *handle) []engine.Conn {
	h.retired = true
	if h.inflight == 0 {
		return []engine.Conn{h.conn}
	}
	m.retiredInUse += h.size
	return nil
}

func (m *Manager) releaser(h *handle) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			h.inflight--
			h.lastUsed = m.opt.Now()
			closeIt := h.retired && h.inflight == 0
			if closeIt {
				m.retiredInUse -= h.size
			}
			m.mu.Unlock()
			if closeIt {
				h.conn.Close()
			}
		})
	}
}

// makeRoom evicts least-recently-used idle pools until size more connections
// fit under the cap. Live pools, opens in progress and retired pools still in
// use all count toward the cap. It must be called with m.mu held.
func (m *Manager) makeRoom(size int) ([]engine.Conn, error) {
	total := m.retiredInUse
	for _, h := range m.handles {
		total += h.size
	}
	for _, p := range m.opening {
		total += p.size
	}
	var out []engine.Conn
	for total+size > m.opt.MaxTotalConns {
		var lruID string
		var lru *handle
		for id, h := range m.handles {
			if h.inflight == 0 && (lru == nil || h.lastUsed.Before(lru.lastUsed)) {
				lruID, lru = id, h
			}
		}
		if lru == nil {
			return out, qerr.New(qerr.Busy, "the server's database connection limit is reached").WithHint("Retry shortly")
		}
		delete(m.handles, lruID)
		total -= lru.size
		out = append(out, lru.conn)
	}
	return out, nil
}

func (m *Manager) recordFailure(spec engine.ConnSpec, err error) {
	f := m.failures[spec.ID]
	if f == nil || f.version != spec.Version {
		f = &failure{version: spec.Version}
		m.failures[spec.ID] = f
	}
	f.count++
	f.err = err
	f.retryAt = m.opt.Now().Add(backoff(f.count))
}

// backoff is 1s, 2s, 4s, 8s, 16s, then 30s.
func backoff(n int) time.Duration {
	if n > 5 {
		return 30 * time.Second
	}
	return time.Second << (n - 1)
}

// Drop retires the pool for id; it closes once in-flight work finishes.
func (m *Manager) Drop(id string) {
	var toClose []engine.Conn
	m.mu.Lock()
	if h := m.handles[id]; h != nil {
		delete(m.handles, id)
		toClose = m.retire(h)
	}
	delete(m.failures, id)
	m.mu.Unlock()
	closeAll(toClose)
}

// Sweep closes pools idle for longer than IdleTimeout.
func (m *Manager) Sweep() {
	var toClose []engine.Conn
	m.mu.Lock()
	now := m.opt.Now()
	for id, h := range m.handles {
		if h.inflight == 0 && now.Sub(h.lastUsed) >= m.opt.IdleTimeout {
			delete(m.handles, id)
			toClose = append(toClose, h.conn)
		}
	}
	for id, f := range m.failures {
		if now.Sub(f.retryAt) > m.opt.IdleTimeout {
			delete(m.failures, id)
		}
	}
	m.mu.Unlock()
	closeAll(toClose)
}

// Start runs Sweep periodically until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	interval := min(m.opt.IdleTimeout/2, time.Minute)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.Sweep()
			}
		}
	}()
}

// Close closes every pool. Later Gets fail.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	toClose := make([]engine.Conn, 0, len(m.handles))
	for id, h := range m.handles {
		toClose = append(toClose, h.conn)
		delete(m.handles, id)
	}
	m.mu.Unlock()
	closeAll(toClose)
}

func closeAll(cs []engine.Conn) {
	for _, c := range cs {
		_ = c.Close()
	}
}
