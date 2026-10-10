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
		case 3024, 1969, 1317: // max_execution_time, max_statement_time, interrupted
			return timeoutErr(err)
		case 1792, 1290: // read-only transaction, --read-only server
			return readOnlyErr()
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
		case 1045, 1044, 1049:
			return connectErr(err)
		}
		return qerr.Wrap(qerr.QueryError, me.Message, err)
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
