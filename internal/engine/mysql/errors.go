package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/anderson-0/qh/internal/qerr"
)

var (
	unknownColumn = regexp.MustCompile(`Unknown column '([^']+)'`)
	noSuchTable   = regexp.MustCompile(`Table '([^']+)' doesn't exist`)
)

func readOnlyErr() *qerr.Error {
	return qerr.New(qerr.ReadOnly, "connection is read-only; only statements that read data are allowed").
		WithHint("Use a read-write connection to change data")
}

func timeoutErr(err error) *qerr.Error {
	return qerr.Wrap(qerr.Timeout, "query exceeded the connection timeout", err).
		WithHint("Add WHERE/LIMIT or narrow the range")
}

// classify maps an error from a running statement to a qerr category.
func classify(ctx context.Context, err error) error {
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 3024, 1969: // max_execution_time, max_statement_time
			// The server's known timeout takes precedence over a later
			// context cancellation during transaction cleanup.
			return timeoutErr(err)
		case 1317: // KILL QUERY also reports interrupted, without a timeout.
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return qerr.Wrap(qerr.QueryError, "query canceled", err)
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return timeoutErr(err)
			}
		case 1792: // read-only transaction
			return readOnlyErr()
		case 1290: // "running with the --read-only option"; other options (e.g. --secure-file-priv) share the code
			if m := strings.ToLower(me.Message); strings.Contains(m, "read-only") || strings.Contains(m, "read only") {
				return readOnlyErr()
			}
			return qerr.Wrap(qerr.QueryError, me.Message, err)
		case 1054:
			e := qerr.Wrap(qerr.QueryError, me.Message, err)
			if m := unknownColumn.FindStringSubmatch(me.Message); m != nil {
				e.UnknownColumn = m[1]
			}
			return e
		case 1146:
			e := qerr.Wrap(qerr.QueryError, me.Message, err)
			if m := noSuchTable.FindStringSubmatch(me.Message); m != nil {
				name := m[1]
				if i := strings.LastIndex(name, "."); i >= 0 {
					name = name[i+1:]
				}
				e.UnknownTable = name
			}
			return e
		case 1045: // 1044 and 1049 can also come from a statement (a table in another database), so they stay query errors here
			return connectErr(err)
		}
		return qerr.Wrap(qerr.QueryError, me.Message, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return qerr.Wrap(qerr.QueryError, "query canceled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeoutErr(err)
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, gomysql.ErrInvalidConn) || errors.Is(err, io.EOF) {
		return qerr.Wrap(qerr.ConnectionFailed, "lost the connection to the database: "+err.Error(), err)
	}
	return qerr.Wrap(qerr.QueryError, err.Error(), err)
}

// connectErr maps an error raised while connecting. Everything here is
// connection_failed; the message says why.
func connectErr(err error) error {
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		if me.Number == 1045 || me.Number == 1044 {
			return qerr.Wrap(qerr.ConnectionFailed, "authentication failed: "+me.Message, err)
		}
		return qerr.Wrap(qerr.ConnectionFailed, me.Message, err)
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return qerr.Wrap(qerr.ConnectionFailed, "timed out connecting to the database", err)
	}
	return qerr.Wrap(qerr.ConnectionFailed, "cannot reach the database: "+err.Error(), err)
}

// Internal cleanup/control errors remain causes only. The original request and
// explicit guard reason determine the agent category, never driverCtx.Err().
func interruptionError(ctx context.Context, stop stopOutcome, cleanup cleanupResult, statementErr error) error {
	if cleanup.Committed {
		return nil
	}
	cause := errors.Join(statementErr, stop.Err, cleanup.Err, ctx.Err())
	reason := stop.Reason
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		reason = stopDeadline
	} else if errors.Is(ctx.Err(), context.Canceled) {
		reason = stopCallerCancel
	}
	unconfirmed := stop.Admitted && (!stop.StatementDone || !cleanup.RolledBack)
	var qe *qerr.Error
	var me *gomysql.MySQLError
	switch {
	case errors.As(statementErr, &me) && (me.Number == 3024 || me.Number == 1969):
		qe = timeoutErr(cause)
	case reason == stopDeadline:
		qe = timeoutErr(errors.Join(cause, context.DeadlineExceeded))
	case reason == stopCallerCancel || reason == stopShutdown:
		qe = qerr.Wrap(qerr.QueryError, "query canceled", errors.Join(cause, context.Canceled))
	case reason == stopRowCap:
		if !unconfirmed && (statementErr == nil || errors.As(statementErr, &me) && me.Number == 1317) {
			return nil
		}
		qe = qerr.Wrap(qerr.QueryError, "query interrupted", cause)
	case statementErr != nil:
		classified := qerr.From(classify(ctx, statementErr))
		copy := *classified
		copy.Err = cause
		qe = &copy
	case cleanup.Err != nil:
		category := qerr.From(classify(ctx, cleanup.Err)).Category
		qe = qerr.Wrap(category, "could not finish query transaction", cause)
	default:
		return nil
	}
	if unconfirmed {
		qe.WithHint(unconfirmedHint)
	}
	return qe
}
