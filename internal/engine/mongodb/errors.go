package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/anderson-0/qh/internal/qerr"
)

// Server error codes qh treats specially.
const (
	codeUnauthorized      = 13
	codeAuthFailed        = 18
	codeNamespaceNotFound = 26
	codeMaxTimeExpired    = 50
)

// mapErr classifies a driver error. Callers scrub passwords from the
// message (conn.err).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	// v2 returns CommandError and WriteException as values.
	var ce mongo.CommandError
	isCmd := errors.As(err, &ce)
	if isCmd && ce.Code == codeAuthFailed {
		return qerr.Wrap(qerr.ConnectionFailed, "authentication failed", err).WithHint("Check the user name, password and authSource")
	}
	if errors.Is(err, context.DeadlineExceeded) || mongo.IsTimeout(err) || isCmd && ce.Code == codeMaxTimeExpired {
		// Server selection times out when no server answers.
		if strings.Contains(err.Error(), "server selection error") {
			return qerr.Wrap(qerr.ConnectionFailed, "cannot reach MongoDB: "+firstLine(err.Error()), err)
		}
		return qerr.Wrap(qerr.Timeout, "the query timed out", err).WithHint("Narrow the filter, add an index, or raise the connection's timeout")
	}
	if isCmd {
		return qerr.Wrap(qerr.QueryError, fmt.Sprintf("%s (%s, code %d)", ce.Message, ce.Name, ce.Code), err)
	}
	var we mongo.WriteException
	if errors.As(err, &we) && len(we.WriteErrors) > 0 {
		w := we.WriteErrors[0]
		return qerr.Wrap(qerr.QueryError, fmt.Sprintf("%s (code %d)", w.Message, w.Code), err)
	}
	if isConnErr(err) {
		return qerr.Wrap(qerr.ConnectionFailed, "cannot reach MongoDB: "+firstLine(err.Error()), err)
	}
	return qerr.Wrap(qerr.QueryError, firstLine(err.Error()), err)
}

// isConnErr reports whether err means qh could not reach or log in to the
// server: a network error, a failed server selection or a failed handshake.
func isConnErr(err error) bool {
	if mongo.IsNetworkError(err) || errors.Is(err, mongo.ErrClientDisconnected) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "server selection") || strings.Contains(msg, "connection(") ||
		strings.Contains(msg, "auth error") || strings.Contains(msg, "unable to authenticate")
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}
