package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/auth"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestConnectionFieldsAndTLS(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	f := testutil.NewTLSFixture(t)
	r := e.do(t, "POST", "/api/v1/connections?skip_test=true", k, map[string]any{
		"name": "pg", "engine": "postgres", "host": "db.example.com", "port": 5433, "database": "app",
		"username": "reader", "password": "hunter22-pw", "params": map[string]string{"application_name": "x"},
		"tls": map[string]any{"mode": "verify-full", "ca": f.CA, "cert": f.ClientCert, "key": f.ClientKey},
	})
	var c connectionJSON
	r.json(t, &c)
	if r.status != 201 || c.Username != "reader" || c.Host != "db.example.com" || c.Port != 5433 || c.Database != "app" ||
		c.TLS == nil || c.TLS.Mode != "verify-full" || !c.TLS.CA || !c.TLS.ClientCert || strings.Contains(r.body, "hunter22-pw") {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	r = e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, `{"tls": null}`)
	c = connectionJSON{} // a decode leaves fields the response omits as they were
	r.json(t, &c)
	if r.status != 200 || c.TLS != nil || c.Version != 2 {
		t.Fatalf("clear tls: %d %s", r.status, r.body)
	}
	r = e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, map[string]any{"tls": map[string]any{"mode": "require"}})
	r.json(t, &c)
	if r.status != 200 || c.TLS == nil || c.TLS.Mode != "require" || c.TLS.ClientCert {
		t.Fatalf("set tls: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "PATCH", "/api/v1/connections/pg", k, map[string]any{"tls": map[string]any{"mode": "bogus"}}),
		400, "invalid", `unknown tls.mode "bogus"`)
	expectProblem(t, e.do(t, "PATCH", "/api/v1/connections/pg", k, `{"tls": {"mode": "require", "pin": "x"}}`),
		400, "invalid", "invalid JSON body")
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "x", "engine": "postgres", "dsn": "postgres://h/db", "host": "h"}),
		400, "invalid", "give either dsn or host")
	if r := e.do(t, "POST", "/api/v1/connections/test", k, map[string]any{"engine": "sqlite", "database": testutil.SQLiteFixture(t)}); r.status != 200 {
		t.Fatalf("test with fields: %d %s", r.status, r.body)
	}
}

func TestTLSKeyNeverReturned(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	f := testutil.NewTLSFixture(t)
	body := map[string]any{"name": "pg", "engine": "postgres", "dsn": "postgres://u@db.example.com/app",
		"tls": map[string]any{"mode": "require", "cert": f.ClientCert, "key": f.ClientKey}}
	mismatched := map[string]any{"engine": "postgres", "dsn": "postgres://u@db.example.com/app",
		"tls": map[string]any{"mode": "require", "cert": f.ClientCert, "key": f.ServerKey}}
	bodies := []string{
		e.do(t, "POST", "/api/v1/connections?skip_test=true", k, body).body,
		e.do(t, "GET", "/api/v1/connections", k, nil).body,
		e.do(t, "GET", "/api/v1/connections/pg", k, nil).body,
		e.do(t, "POST", "/api/v1/connections?skip_test=true", k, body).body, // duplicate: an error response
		e.do(t, "POST", "/api/v1/connections/test", k, mismatched).body,     // invalid key pair: an error response
	}
	for i, b := range bodies {
		if strings.Contains(b, "PRIVATE KEY") || strings.Contains(b, "BEGIN CERTIFICATE") {
			t.Fatalf("response %d returns TLS material: %s", i, b)
		}
	}
}

func keyID(t *testing.T, e *env, token string) string {
	t.Helper()
	prefix, _, ok := auth.ParseToken(token)
	if !ok {
		t.Fatalf("bad token %q", token)
	}
	k, err := e.st.GetKeyByPrefix(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	return k.ID
}

func TestQueryLogEndpoint(t *testing.T) {
	e := newEnv(t, true, nil)
	m := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	q := e.key(t, "default", auth.ScopeQuery)
	if r := e.do(t, "POST", "/api/v1/connections", m, map[string]any{"name": "shop", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	e.do(t, "POST", "/api/v1/connections/shop/query", q, map[string]any{"query": "SELECT id FROM users"})
	e.do(t, "POST", "/api/v1/connections/shop/query", q, map[string]any{"query": "DELETE FROM users"})
	if out, isErr := mcpCall(t, e, q, "query", map[string]any{"source": "shop", "query": "SELECT 1 AS one"}); isErr {
		t.Fatalf("mcp query: %s", out)
	}
	type entry struct {
		Connection    string `json:"connection"`
		KeyID         string `json:"key_id"`
		Tool          string `json:"tool"`
		Query         string `json:"query"`
		Rows          int64  `json:"rows"`
		ErrorCategory string `json:"error_category"`
	}
	var log []entry
	r := e.do(t, "GET", "/api/v1/query-log", m, nil)
	r.json(t, &log)
	qid := keyID(t, e, q)
	if r.status != 200 || len(log) != 3 || log[0].Query != "SELECT 1 AS one" || log[0].KeyID != qid ||
		log[1].ErrorCategory != "read_only" || log[2].Rows != 3 || log[2].KeyID != qid || log[2].Connection != "shop" || log[2].Tool != "query" {
		t.Fatalf("log: %d %s", r.status, r.body)
	}
	for path, n := range map[string]int{"?limit=1": 1, "?connection=crm": 0, "?since=1h": 3, "?since=2000-01-01T00:00:00Z": 3} {
		var got []entry
		e.do(t, "GET", "/api/v1/query-log"+path, m, nil).json(t, &got)
		if len(got) != n {
			t.Errorf("%s: %d entries", path, len(got))
		}
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log?limit=x", m, nil), 400, "invalid", "limit must be a number")
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log?limit=5000", m, nil), 400, "invalid", "limit must be between 1 and 1000")
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log?since=yesterday", m, nil), 400, "invalid", "since must be an RFC 3339 time")
	expectProblem(t, e.do(t, "GET", "/api/v1/query-log", q, nil), 403, "forbidden", "this API key lacks the manage scope")
}

// A manage key of another workspace never sees this workspace's query log,
// even when its request names the other workspace in X-QH-Workspace.
func TestQueryLogStaysInItsWorkspace(t *testing.T) {
	e := newEnv(t, true, nil)
	m := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	if r := e.do(t, "POST", "/api/v1/connections", m, map[string]any{"name": "shop", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections/shop/query", m, map[string]any{"query": "SELECT id FROM users"}); r.status != 200 {
		t.Fatalf("query: %d %s", r.status, r.body)
	}
	var own []map[string]any
	e.do(t, "GET", "/api/v1/query-log", m, nil).json(t, &own)
	if len(own) == 0 {
		t.Fatal("the default workspace logged nothing")
	}
	if _, err := e.svc.CreateWorkspace(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	o := e.key(t, "other", auth.ScopeManage)
	for _, headers := range [][]string{nil, {"X-QH-Workspace", "default"}} {
		r := e.do(t, "GET", "/api/v1/query-log", o, nil, headers...)
		if r.status != 200 || strings.TrimSpace(r.body) != "[]" {
			t.Errorf("headers %v: %d %s", headers, r.status, r.body)
		}
	}
}
