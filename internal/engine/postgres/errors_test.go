package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anderson-0/qh/internal/qerr"
)

func TestClassifyCancellation(t *testing.T) {
	active := context.Background()
	canceled, cancel := context.WithCancel(active)
	cancel()
	expired, stop := context.WithDeadline(active, time.Unix(1, 0))
	defer stop()
	userCancel := &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}
	statementTimeout := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	unknownCancel := &pgconn.PgError{Code: "57014", Message: "custom cancellation", Detail: "server detail", Hint: "server hint"}
	for _, tt := range []struct {
		name     string
		ctx      context.Context
		err      error
		category qerr.Category
		message  string
		hint     string
	}{
		{"client cancel response", canceled, userCancel, qerr.QueryError, "query canceled", ""},
		{"wrapped client response", canceled, fmt.Errorf("wrapped: %w", userCancel), qerr.QueryError, "query canceled", ""},
		{"context canceled", active, context.Canceled, qerr.QueryError, "query canceled", ""},
		{"network wrapper", canceled, &net.OpError{Op: "read", Net: "tcp", Err: context.Canceled}, qerr.QueryError, "query canceled", ""},
		{"closed socket after cancel", canceled, io.EOF, qerr.QueryError, "query canceled", ""},
		{"external cancel", active, userCancel, qerr.QueryError, userCancel.Message, ""},
		{"unknown cancellation", active, unknownCancel, qerr.QueryError, "custom cancellation (server detail)", "server hint"},
		{"localized cancellation", active, &pgconn.PgError{Code: "57014", Message: "consulta cancelada"}, qerr.QueryError, "consulta cancelada", ""},
		{"statement timeout", active, statementTimeout, qerr.Timeout, "query exceeded the connection timeout", "Add WHERE/LIMIT or narrow the range"},
		{"statement timeout before late cancel", canceled, statementTimeout, qerr.Timeout, "query exceeded the connection timeout", "Add WHERE/LIMIT or narrow the range"},
		{"deadline response", expired, userCancel, qerr.Timeout, "query exceeded the connection timeout", "Add WHERE/LIMIT or narrow the range"},
		{"context deadline", active, context.DeadlineExceeded, qerr.Timeout, "query exceeded the connection timeout", "Add WHERE/LIMIT or narrow the range"},
		{"deadline socket", expired, io.EOF, qerr.Timeout, "query exceeded the connection timeout", "Add WHERE/LIMIT or narrow the range"},
		{"unrelated server error after cancel", canceled, &pgconn.PgError{Code: "42601", Message: "syntax error"}, qerr.QueryError, "syntax error", ""},
		{"closed socket", active, io.EOF, qerr.ConnectionFailed, "cannot reach the database: EOF", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := (&conn{}).classify(tt.ctx, tt.err)
			var got *qerr.Error
			if !errors.As(err, &got) || got.Category != tt.category || got.Message != tt.message || got.Hint != tt.hint {
				t.Fatalf("classified = %#v; want %s, %q, hint %q", got, tt.category, tt.message, tt.hint)
			}
			if !errors.Is(err, tt.err) {
				t.Fatal("underlying error was lost")
			}
		})
	}
	// Already classified service errors keep their identity even after cancel.
	prior := qerr.New(qerr.Busy, "all connections are in use")
	if got := (&conn{}).classify(canceled, prior); got != prior {
		t.Fatalf("classified error changed: %v", got)
	}
}
