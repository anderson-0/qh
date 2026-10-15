package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	gomysql "github.com/go-sql-driver/mysql"
	"strings"
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
	mu                        sync.Mutex
	completed                 bool
	owner                     *writeCanceler
	ctx                       context.Context
	cancel                    context.CancelCauseFunc
	query, control            *sql.Conn
	target                    uint64
	uuid, marker, preparation string
	cleanupOnce               sync.Once
	cleanupResult             cleanupResult
	admitted, commitStarted   bool
	outcome                   stopOutcome
	driverCtx                 context.Context
	driverCancel              context.CancelFunc
	mainDone, guardDone       chan struct{}
	capRequest                chan struct{}
	queryBad, controlBad      bool
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
	ctx, cancel := context.WithCancelCause(ctx)
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
			c.cancel(errEngineClosed)
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
		c.mu.Lock()
		admitted := c.admitted
		c.mu.Unlock()
		if admitted {
			c.Complete(context.Canceled)
		}
		commitRejected := commit && !c.commitStarted
		if commitRejected {
			commit = false
		}
		ctx, cancel := context.WithTimeout(context.Background(), cleanupWait)
		defer cancel()
		if c.query != nil && c.queryBad {
			discard(c.query)
			c.query.Close()
		} else if c.query != nil {
			c.cleanupResult = c.owner.owner.finishContext(ctx, c.query, commit, true)
			c.query.Close()
		}
		if c.control != nil {
			r := c.owner.owner.finishContext(ctx, c.control, false, false)
			c.cleanupResult.Err = errors.Join(c.cleanupResult.Err, r.Err)
			if c.controlBad {
				discard(c.control)
			}
			c.control.Close()
		}
		if commitRejected {
			c.cleanupResult.Err = errors.Join(c.cleanupResult.Err, errors.New("commit was not admitted"))
		}
		c.cancel(nil)
		c.owner.mu.Lock()
		delete(c.owner.calls, c)
		c.owner.mu.Unlock()
		c.owner.wg.Done()
	})
	return c.cleanupResult
}

var errEngineClosed = errors.New("engine closed")

type stopReason uint8

const (
	stopNone stopReason = iota
	stopCallerCancel
	stopDeadline
	stopShutdown
	stopRowCap
)

type stopOutcome struct {
	Reason                  stopReason
	Admitted, StatementDone bool
	Err                     error
}

func (c *writeCall) reason() stopReason {
	if errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
		return stopDeadline
	}
	if errors.Is(context.Cause(c.ctx), errEngineClosed) {
		return stopShutdown
	}
	return stopCallerCancel
}
func (c *writeCall) Admit() (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return nil, classify(c.ctx, err)
	}
	if c.admitted {
		return nil, errors.New("query already admitted")
	}
	c.admitted = true
	c.outcome.Admitted = true
	c.driverCtx, c.driverCancel = context.WithCancel(context.WithoutCancel(c.ctx))
	c.mainDone = make(chan struct{})
	c.guardDone = make(chan struct{})
	c.capRequest = make(chan struct{}, 1)
	go c.guard()
	return c.driverCtx, nil
}
func (c *writeCall) RequestTruncation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.admitted && !c.completed {
		c.outcome.Reason = stopRowCap
		select {
		case c.capRequest <- struct{}{}:
		default:
		}
	}
}
func serverResponse(err error) bool {
	var me *gomysql.MySQLError
	return err == nil || errors.As(err, &me)
}
func (c *writeCall) Complete(err error) stopOutcome {
	c.mu.Lock()
	if !c.admitted {
		c.mu.Unlock()
		return stopOutcome{}
	}
	if !c.completed {
		c.completed = true
		if c.ctx.Err() != nil {
			c.outcome.Reason = c.reason()
		}
		c.outcome.StatementDone = serverResponse(err)
		if !c.outcome.StatementDone {
			c.queryBad = true
		}
		close(c.mainDone)
	}
	done := c.guardDone
	c.mu.Unlock()
	<-done
	c.driverCancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outcome
}
func (c *writeCall) CommitAllowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if !c.completed || !c.outcome.StatementDone || c.outcome.Reason != stopNone || c.ctx.Err() != nil || c.owner.closed {
		return false
	}
	select {
	case <-c.guardDone:
	default:
		return false
	}
	c.commitStarted = true
	return true
}
func (c *writeCall) guard() {
	defer close(c.guardDone)
	reason := stopNone
	select {
	case <-c.mainDone:
		return
	case <-c.ctx.Done():
		reason = c.reason()
	case <-c.capRequest:
		reason = stopRowCap
	}
	c.mu.Lock()
	if c.completed {
		c.mu.Unlock()
		return
	}
	if c.ctx.Err() != nil {
		reason = c.reason()
	}
	c.outcome.Reason = reason
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), interruptWait)
	defer cancel()
	// Do not cancel the control operation just because main completed: its
	// response must join before either connection can be cleaned or reused.
	fail := func(err error) {
		c.mu.Lock()
		c.outcome.Err = err
		c.controlBad = true
		c.mu.Unlock()
		c.driverCancel()
	}
	for {
		select {
		case <-c.mainDone:
			return
		case <-ctx.Done():
			fail(ctx.Err())
			return
		default:
		}
		var uuid string
		var owner sql.NullInt64
		var command, info sql.NullString
		err := c.control.QueryRowContext(ctx, "SELECT @@server_uuid, IS_USED_LOCK(?), (SELECT COMMAND FROM information_schema.PROCESSLIST WHERE ID = ?), (SELECT INFO FROM information_schema.PROCESSLIST WHERE ID = ?)", c.marker, c.target, c.target).Scan(&uuid, &owner, &command, &info)
		if err != nil {
			fail(err)
			return
		}
		if uuid != c.uuid || !owner.Valid || owner.Int64 < 1 || uint64(owner.Int64) != c.target {
			fail(errors.New("session identity unavailable"))
			return
		}
		c.mu.Lock()
		if c.completed {
			c.mu.Unlock()
			return
		}
		active := command.Valid && (command.String == "Query" || command.String == "Execute") && info.Valid && !strings.Contains(info.String, "/* "+c.marker+" */")
		c.mu.Unlock()
		if active {
			_, err = c.control.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", c.target))
			if err != nil {
				var me *gomysql.MySQLError
				if !errors.As(err, &me) || me.Number != 1094 {
					fail(err)
					return
				}
			}
			select {
			case <-c.mainDone:
				return
			case <-ctx.Done():
				fail(ctx.Err())
				return
			}
		}
		timer := time.NewTimer(cancelPoll)
		select {
		case <-c.mainDone:
			timer.Stop()
			return
		case <-ctx.Done():
			timer.Stop()
			fail(ctx.Err())
			return
		case <-timer.C:
		}
	}
}
