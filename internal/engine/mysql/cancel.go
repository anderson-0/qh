package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
)

const interruptWait = 5 * time.Second
const cleanupWait = 5 * time.Second
const cancelPoll = 10 * time.Millisecond
const unconfirmedHint = "Server cancellation or rollback could not be confirmed; check the database before retrying a write"

type cleanupResult struct {
	Committed, RolledBack bool
	Err                   error
}

// Registration and the closed-state check share mu. Close cannot miss a call
// which is about to wait for either pool, or race a WaitGroup.Add.
type writeCanceler struct {
	owner     *conn
	control   *sql.DB
	mu        sync.Mutex
	calls     map[*writeCall]struct{}
	closed    bool
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}
type writeCall struct {
	owner                     *writeCanceler
	ctx                       context.Context
	cancel                    context.CancelFunc
	query, control            *sql.Conn
	target                    uint64
	uuid, marker, preparation string
	cleanupOnce               sync.Once
	cleanupResult             cleanupResult
}

func newWriteCanceler(owner *conn, connector driver.Connector, size int) *writeCanceler {
	db := sql.OpenDB(connector) // OpenDB performs no I/O; first protected call dials.
	db.SetMaxOpenConns(max(size, 1))
	db.SetMaxIdleConns(max(size, 1))
	db.SetConnMaxIdleTime(10 * time.Minute)
	return &writeCanceler{owner: owner, control: db, calls: make(map[*writeCall]struct{})}
}
func (w *writeCanceler) Begin(ctx context.Context) (*writeCall, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	if w.closed {
		return nil, qerr.Wrap(qerr.QueryError, "query canceled", context.Canceled)
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &writeCall{owner: w, ctx: ctx, cancel: cancel}
	w.calls[c] = struct{}{}
	w.wg.Add(1)
	return c, nil
}
func (w *writeCanceler) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		for c := range w.calls {
			c.cancel()
		}
		w.mu.Unlock()
		w.wg.Wait()
		w.closeErr = w.control.Close()
	})
	return w.closeErr
}
func (c *writeCall) Context() context.Context { return c.ctx }

// Preparation errors preserve causes without exposing internal SQL, identity or
// control-driver diagnostics (which may include connection details).
func preparationError(ctx context.Context, err error) error {
	category := qerr.ConnectionFailed
	if ctx.Err() != nil {
		category = qerr.From(classify(ctx, ctx.Err())).Category
	}
	return qerr.Wrap(category, "could not prepare cancellable query", errors.Join(err, ctx.Err()))
}
func (c *writeCall) Prepare(query *sql.Conn, setupSQL string) error {
	c.query = query
	fail := func(err error) error { return preparationError(c.ctx, err) }
	if err := c.ctx.Err(); err != nil {
		return fail(err)
	}
	var err error
	if c.control, err = c.owner.control.Conn(c.ctx); err != nil {
		return fail(err)
	}
	if _, err = c.control.ExecContext(c.ctx, "START TRANSACTION"); err != nil {
		return fail(err)
	}
	if _, err = query.ExecContext(c.ctx, setupSQL); err != nil {
		return fail(err)
	}
	if _, err = query.ExecContext(c.ctx, "START TRANSACTION"); err != nil {
		return fail(err)
	}
	var entropy [16]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return fail(err)
	}
	c.marker = "qh_cancel_" + hex.EncodeToString(entropy[:])
	c.preparation = "SELECT /* " + c.marker + " */ CONNECTION_ID(), @@server_uuid, GET_LOCK(?, 0)"
	var acquired sql.NullInt64
	if err = query.QueryRowContext(c.ctx, c.preparation, c.marker).Scan(&c.target, &c.uuid, &acquired); err != nil {
		return fail(err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fail(errors.New("session identity unavailable"))
	}
	var server string
	var owner sql.NullInt64
	if err = c.control.QueryRowContext(c.ctx, "SELECT @@server_uuid, IS_USED_LOCK(?)", c.marker).Scan(&server, &owner); err != nil {
		return fail(err)
	}
	if server != c.uuid || !owner.Valid || owner.Int64 < 1 || uint64(owner.Int64) != c.target {
		return fail(errors.New("session identity mismatch"))
	}
	if err = c.ctx.Err(); err != nil {
		return fail(err)
	}
	return nil
}
func (c *writeCall) Cleanup(commit bool) cleanupResult {
	c.cleanupOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupWait)
		defer cancel()
		if c.query != nil {
			c.cleanupResult = c.owner.owner.finishContext(ctx, c.query, commit, true)
			c.query.Close()
		}
		if c.control != nil {
			r := c.owner.owner.finishContext(ctx, c.control, false, false)
			c.cleanupResult.Err = errors.Join(c.cleanupResult.Err, r.Err)
			c.control.Close()
		}
		c.cancel()
		c.owner.mu.Lock()
		delete(c.owner.calls, c)
		c.owner.mu.Unlock()
		c.owner.wg.Done()
	})
	return c.cleanupResult
}
