package store

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPermanentPGError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "28P01"}, true},  // invalid_password
		{&pgconn.PgError{Code: "28000"}, true},  // invalid_authorization_specification
		{&pgconn.PgError{Code: "3D000"}, true},  // invalid_catalog_name
		{&pgconn.PgError{Code: "42501"}, true},  // insufficient_privilege
		{&pgconn.PgError{Code: "57P04"}, true},  // database_dropped
		{&pgconn.PgError{Code: "57P03"}, false}, // cannot_connect_now: starting up
		{&pgconn.PgError{Code: "53300"}, false}, // too_many_connections
		{&pgconn.PgError{Code: "57P01"}, false}, // admin_shutdown
		{&pgconn.PgError{Code: "57P02"}, false}, // crash_shutdown
		{errors.New("dial tcp: connection refused"), false},
		{nil, false},
	} {
		for _, err := range []error{tc.err, fmt.Errorf("wrapped: %w", tc.err)} {
			if tc.err == nil && err != nil {
				continue
			}
			if got := permanent(err); got != tc.want {
				t.Errorf("permanent(%v) = %v, want %v", err, got, tc.want)
			}
		}
	}
}

func TestConnDied(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "57P01"}, true}, // admin_shutdown
		{&pgconn.PgError{Code: "57P02"}, true}, // crash_shutdown
		{&pgconn.PgError{Code: "57P03"}, true}, // cannot_connect_now
		{&pgconn.PgError{Code: "08006"}, true}, // connection_failure
		{&pgconn.PgError{Code: "08000"}, true}, // connection_exception
		{driver.ErrBadConn, true},
		{io.EOF, true},
		{io.ErrUnexpectedEOF, true},
		{&net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}, true},
		{&pgconn.PgError{Code: "23505"}, false}, // unique_violation
		{&pgconn.PgError{Code: "57014"}, false}, // query_canceled
		{&pgconn.PgError{Code: "53300"}, false}, // too_many_connections: not a dead connection
		{sql.ErrNoRows, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{errors.New("boom"), false},
		{nil, false},
	} {
		for _, err := range []error{tc.err, fmt.Errorf("wrapped: %w", tc.err)} {
			if tc.err == nil && err != nil {
				continue
			}
			if got := connDied(err); got != tc.want {
				t.Errorf("connDied(%v) = %v, want %v", err, got, tc.want)
			}
		}
	}
}

func TestReadRetriesOnceOnlyWhenTheConnectionDied(t *testing.T) {
	p := &Postgres{}
	ctx := context.Background()
	calls := 0
	err := p.read(ctx, func() error {
		calls++
		if calls == 1 {
			return &pgconn.PgError{Code: "57P01"}
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("dead connection: err %v after %d calls, want nil after 2", err, calls)
	}
	calls = 0
	err = p.read(ctx, func() error { calls++; return io.EOF })
	if !errors.Is(err, io.EOF) || calls != 2 {
		t.Fatalf("still dead: err %v after %d calls, want EOF after 2", err, calls)
	}
	calls = 0
	err = p.read(ctx, func() error { calls++; return sql.ErrNoRows })
	if !errors.Is(err, sql.ErrNoRows) || calls != 1 {
		t.Fatalf("other error: err %v after %d calls, want ErrNoRows after 1", err, calls)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	calls = 0
	err = p.read(cctx, func() error { calls++; return io.EOF })
	if err == nil || calls != 1 {
		t.Fatalf("ended context: err %v after %d calls, want an error after 1", err, calls)
	}
}

type logRecord struct {
	Level    string
	Msg      string
	Attempt  int // a failed reconnect
	Attempts int // a successful one
	Error    string
}

func logRecords(t *testing.T, b *bytes.Buffer) []logRecord {
	t.Helper()
	var out []logRecord
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var r logRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

func TestListenLogLimitsRepeatedFailures(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := &listenLog{log: func() *slog.Logger { return logger }, password: "pw-secret-9", every: time.Minute,
		now: func() time.Time { return now }}
	fail := errors.New("dial postgres://qh:pw-secret-9@db: connection refused")

	l.lost(fail)
	l.failed(1, fail)
	now = now.Add(10 * time.Second)
	l.failed(2, fail) // within a minute of the last report: quiet
	now = now.Add(30 * time.Second)
	l.failed(3, fail) // still quiet
	now = now.Add(25 * time.Second)
	l.failed(4, fail) // over a minute since attempt 1
	l.reconnected(5)
	// A new outage reports its first failure at once.
	l.lost(fail)
	l.failed(1, fail)

	if strings.Contains(buf.String(), "pw-secret-9") {
		t.Fatalf("the password was logged: %s", buf.String())
	}
	var got []string
	for _, r := range logRecords(t, &buf) {
		got = append(got, fmt.Sprintf("%s %d", r.Level, r.Attempt+r.Attempts))
	}
	want := []string{"WARN 0", "WARN 1", "WARN 4", "INFO 5", "WARN 0", "WARN 1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("logged %v, want %v\n%s", got, want, buf.String())
	}
}

func TestListenLogWithoutLogger(t *testing.T) {
	l := &listenLog{log: func() *slog.Logger { return nil }, every: time.Minute, now: time.Now}
	l.lost(io.EOF) // must not panic
	l.failed(1, io.EOF)
	l.reconnected(2)
}

func TestKeepAliveDialKeepsTheDialHook(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	called := false
	hook := func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	c, err := keepAliveDial(hook, 15*time.Second)(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if !called {
		t.Fatal("the configured dial function was not used")
	}
}
