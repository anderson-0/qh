package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/testutil"
)

// echoEngine is a fake engine whose connection strings are
// echo://user:password@host. It extracts the userinfo password, opens
// without connecting, and fails every query quoting the connection string,
// the resolved password and the userinfo password, with a hint quoting the
// resolved password.
type echoEngine struct{}

var registerEcho sync.Once

func (echoEngine) Name() string                           { return "qhtest-echo" }
func (echoEngine) Classify(string) (engine.Access, error) { return engine.Read, nil }
func (echoEngine) ReferencedObjects(string) []string      { return nil }

func (echoEngine) Inspect(dsn string) (engine.DSNInfo, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "echo" {
		return engine.DSNInfo{}, qerr.New(qerr.Invalid, "bad echo connection string")
	}
	return engine.DSNInfo{Host: u.Host, User: u.User.Username(), Redacted: "echo://" + u.User.Username() + ":***@" + u.Host}, nil
}

func (echoEngine) Credentials(dsn string) ([]string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, errors.New("echo: cannot locate the password")
	}
	if pw, ok := u.User.Password(); ok {
		return []string{pw}, nil
	}
	return nil, nil
}

func (echoEngine) Open(_ context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	u, err := url.Parse(spec.DSN)
	if err != nil {
		return nil, qerr.New(qerr.ConnectionFailed, "bad echo connection string")
	}
	pw, _ := u.User.Password()
	return &echoConn{dsn: spec.DSN, password: spec.Password, userinfo: pw}, nil
}

type echoConn struct{ dsn, password, userinfo string }

func (c *echoConn) Query(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return nil, qerr.New(qerr.QueryError, fmt.Sprintf("dsn %q, password %q, userinfo password %q", c.dsn, c.password, c.userinfo)).
		WithHint(fmt.Sprintf("the server expected %q", c.password))
}

func (c *echoConn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	return c.Query(ctx, q, o)
}

func (c *echoConn) Introspect(context.Context) (*engine.Catalog, error) {
	return &engine.Catalog{}, nil
}
func (c *echoConn) Ping(context.Context) (string, error) { return "echo 1", nil }
func (c *echoConn) Close() error                         { return nil }

// A query error that quotes the connection string, a resolved password_ref
// and the connection string's password reaches API clients redacted, in
// both the problem's title (the message) and detail (the hint), for text
// and JSON requests.
func TestServedQueryErrorRedacted(t *testing.T) {
	registerEcho.Do(func() { engine.Register(echoEngine{}) })
	const dsn, inline, resolved = "echo://u:q7z!@h", "q7z!", "k9w!"
	t.Setenv("QH_TEST_ECHO_PW", resolved)
	e := newEnv(t, false, nil)
	r := e.do(t, "POST", "/api/v1/connections", "", map[string]any{
		"name": "echo", "engine": "qhtest-echo", "dsn": dsn, "password_ref": "env:QH_TEST_ECHO_PW"})
	if r.status != 201 {
		t.Fatalf("create: status %d", r.status)
	}
	testutil.RequireNoSecret(t, "create body", r.body, dsn, inline, resolved)
	for _, c := range []struct{ name, accept string }{{"text", ""}, {"json", "application/json"}} {
		t.Run(c.name, func(t *testing.T) {
			r := e.do(t, "POST", "/api/v1/connections/echo/query", "", map[string]any{"query": "SELECT 1"}, "Accept", c.accept)
			var p problemBody
			if err := json.Unmarshal([]byte(r.body), &p); err != nil {
				t.Fatal("the body is not a problem")
			}
			if r.status != 422 || p.Status != 422 || p.Type != "query_error" || r.header.Get("Content-Type") != "application/problem+json" {
				t.Errorf("status %d, type %q", r.status, p.Type)
			}
			if p.Title != `dsn "***", password "***", userinfo password "***"` {
				t.Error("title is not the expected redacted message")
			}
			if p.Detail != `the server expected "***"` {
				t.Error("detail is not the expected redacted hint")
			}
			testutil.RequireNoSecret(t, "query body", r.body, dsn, inline, resolved)
		})
	}
}
