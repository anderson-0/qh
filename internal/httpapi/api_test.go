package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/testutil"
)

type problemBody struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

func expectProblem(t *testing.T, r reply, status int, typ, titlePrefix string) {
	t.Helper()
	var p problemBody
	r.json(t, &p)
	if r.status != status || p.Status != status || p.Type != typ || !strings.HasPrefix(p.Title, titlePrefix) ||
		r.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %+v (%s), want %d %s %q", r.status, p, r.header.Get("Content-Type"), status, typ, titlePrefix)
	}
}

func TestHealthAndUnknownPaths(t *testing.T) {
	e := newEnv(t, true, nil)
	if r := e.do(t, "GET", "/healthz", "", nil); r.status != 200 || r.body != "ok\n" {
		t.Fatalf("healthz %d %q", r.status, r.body)
	}
	if r := e.do(t, "GET", "/readyz", "", nil); r.status != 200 {
		t.Fatalf("readyz %d", r.status)
	}
	expectProblem(t, e.do(t, "GET", "/nope", "", nil), 404, "not_found", "no such endpoint")
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t, true, nil)
	r := e.do(t, "GET", "/api/v1/connections", "", nil)
	expectProblem(t, r, 401, "auth", "missing or malformed Authorization header")
	if r.header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("WWW-Authenticate %q", r.header.Get("WWW-Authenticate"))
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", "qh_aaaaaaaa_nope", nil), 401, "auth", "invalid API key")
}

func TestAdminNeedsWorkspaceHeader(t *testing.T) {
	e := newEnv(t, true, nil)
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", e.admin, nil), 400, "invalid", "admin keys must name a workspace")
	if r := e.do(t, "GET", "/api/v1/connections", e.admin, nil, "X-QH-Workspace", "default"); r.status != 200 || strings.TrimSpace(r.body) != "[]" {
		t.Fatalf("with header: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", e.admin, nil, "X-QH-Workspace", "ghost"), 404, "unknown_source", `no workspace named "ghost"`)
}

func TestScopes(t *testing.T) {
	e := newEnv(t, true, nil)
	q := e.key(t, "default", auth.ScopeQuery)
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", q, map[string]any{"name": "x", "engine": "sqlite", "dsn": "/tmp/x"}),
		403, "forbidden", "this API key lacks the manage scope")
	expectProblem(t, e.do(t, "GET", "/api/v1/workspaces", q, nil), 403, "forbidden", "this API key lacks the admin scope")
}

func TestLocalModeNeedsNoKey(t *testing.T) {
	e := newEnv(t, false, nil)
	if r := e.do(t, "GET", "/api/v1/connections", "", nil); r.status != 200 {
		t.Fatalf("local mode: %d %s", r.status, r.body)
	}
}

func TestConnectionLifecycle(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	db := testutil.SQLiteFixture(t)

	r := e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db, "description": "Shop DB"})
	var c connectionJSON
	r.json(t, &c)
	if r.status != 201 || c.Name != "shop" || c.Mode != "ro" || c.DSN != db || !strings.HasPrefix(c.ServerVersion, "SQLite ") || c.ConnectMS == nil || c.Version != 1 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db}),
		400, "invalid", `a connection named "shop" already exists`)

	var list []connectionJSON
	e.do(t, "GET", "/api/v1/connections", k, nil).json(t, &list)
	if len(list) != 1 || list[0].Name != "shop" {
		t.Fatalf("list: %+v", list)
	}

	r = e.do(t, "POST", "/api/v1/connections/shop/query", k, map[string]any{"query": "SELECT count(*) AS n FROM orders"})
	if r.status != 200 || !strings.HasPrefix(r.body, "n\n250\n-- 1 row · ") || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
		t.Fatalf("query text: %d %q", r.status, r.body)
	}
	r = e.do(t, "POST", "/api/v1/connections/shop/query", k, map[string]any{"query": "SELECT id, avatar, name FROM users WHERE id IN (1, 3) ORDER BY id"}, "Accept", "application/json")
	var q struct {
		Columns   []struct{ Name string } `json:"columns"`
		Rows      [][]any                 `json:"rows"`
		Truncated bool                    `json:"truncated"`
	}
	r.json(t, &q)
	if r.status != 200 || len(q.Columns) != 3 || q.Columns[1].Name != "avatar" || len(q.Rows) != 2 || q.Rows[0][0] != "1" ||
		q.Rows[0][1].(map[string]any)["bytes"] != float64(4) || q.Rows[1][2] != nil {
		t.Fatalf("query json: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "POST", "/api/v1/connections/shop/query", k, map[string]any{"query": "DELETE FROM users"}),
		403, "read_only", `connection "shop" is read-only`)

	if r := e.do(t, "GET", "/api/v1/connections/shop/schema?object=users", k, nil); r.status != 200 || !strings.Contains(r.body, "main.users") {
		t.Fatalf("schema text: %d %s", r.status, r.body)
	}
	var s struct {
		Objects []struct {
			Name   string                  `json:"name"`
			Fields []struct{ Name string } `json:"fields"`
		} `json:"objects"`
	}
	e.do(t, "GET", "/api/v1/connections/shop/schema?pattern=user*", k, nil, "Accept", "application/json").json(t, &s)
	if len(s.Objects) != 1 || s.Objects[0].Name != "users" || len(s.Objects[0].Fields) != 6 {
		t.Fatalf("schema json: %+v", s)
	}

	r = e.do(t, "PATCH", "/api/v1/connections/shop", k, map[string]any{"mode": "rw"})
	r.json(t, &c)
	if r.status != 200 || c.Mode != "rw" || c.Version != 2 {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections/shop/schema/refresh", k, nil); r.status != 200 || !strings.Contains(r.body, `"objects":4`) {
		t.Fatalf("refresh: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections/shop/test", k, nil); r.status != 200 || !strings.Contains(r.body, `"server_version":"SQLite `) {
		t.Fatalf("test stored: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections/test", k, map[string]any{"engine": "sqlite", "dsn": db}); r.status != 200 {
		t.Fatalf("test input: %d %s", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/v1/connections/shop", k, nil); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections/shop", k, nil), 404, "unknown_source", `no connection named "shop"`)
}

func TestRequestBodies(t *testing.T) {
	e := newEnv(t, true, nil)
	k := e.key(t, "default", auth.ScopeManage)
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, "{not json"), 400, "invalid", "invalid JSON body")
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, `{"name":"x","password":"p"}`), 400, "invalid", "invalid JSON body")
	big := `{"name":"` + strings.Repeat("x", 2<<20) + `"}`
	expectProblem(t, e.do(t, "POST", "/api/v1/connections", k, big), 400, "invalid", "request body is larger than 1 MiB")
}

func TestPerKeyLimit(t *testing.T) {
	e := newEnv(t, true, func(c *Config) { c.PerKeyLimit = 1; c.SlotWait = 200 * time.Millisecond })
	a := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	b := e.key(t, "default", auth.ScopeQuery)
	db := testutil.SQLiteFixture(t)
	if r := e.do(t, "POST", "/api/v1/connections", a, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db, "timeout_ms": 2000}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	slow := map[string]any{"query": "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"}
	body, _ := json.Marshal(slow)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/connections/shop/query", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // holds a's only slot until the 2s timeout; no t calls off the test goroutine
		defer wg.Done()
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	r := e.do(t, "POST", "/api/v1/connections/shop/query", a, map[string]any{"query": "SELECT 1"})
	expectProblem(t, r, 429, "busy", "this API key already has 1 requests running")
	if r.header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After %q", r.header.Get("Retry-After"))
	}
	if r := e.do(t, "POST", "/api/v1/connections/shop/query", b, map[string]any{"query": "SELECT 1 AS one"}); r.status != 200 {
		t.Fatalf("another key was blocked: %d %s", r.status, r.body)
	}
	wg.Wait()
}

func TestLocalModeRejectsForeignHostAndOrigin(t *testing.T) {
	local := newEnv(t, false, nil)
	get := func(e *env, token, host, origin string) reply {
		t.Helper()
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/connections", nil)
		if host != "" {
			req.Host = host
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return reply{status: res.StatusCode, header: res.Header, body: string(b)}
	}
	const prefix = "local mode only accepts requests addressed to localhost"
	expectProblem(t, get(local, "", "evil.example", ""), 403, "forbidden", prefix)
	expectProblem(t, get(local, "", "evil.example:8765", ""), 403, "forbidden", prefix)
	expectProblem(t, get(local, "", "", "https://evil.example"), 403, "forbidden", "local mode does not accept cross-origin browser requests")
	expectProblem(t, get(local, "", "", "null"), 403, "forbidden", "local mode does not accept cross-origin browser requests")
	expectProblem(t, get(local, "", "", "ftp://localhost"), 403, "forbidden", "local mode does not accept cross-origin browser requests")
	for _, c := range []struct{ host, origin string }{
		{"", ""},
		{"", "http://localhost:3000"},
		{"LocalHost:8765", ""},
		{"[::1]:8765", ""},
		{"[::1]", "http://[::1]:3000"},
		{"127.0.0.1:8765", "https://127.0.0.1"},
	} {
		if r := get(local, "", c.host, c.origin); r.status != 200 {
			t.Fatalf("host %q origin %q: %d %s", c.host, c.origin, r.status, r.body)
		}
	}
	// Server mode relies on keys, so the guard does not apply.
	server := newEnv(t, true, nil)
	if r := get(server, server.key(t, "default", "query"), "evil.example", "https://evil.example"); r.status != 200 {
		t.Fatalf("server mode: %d %s", r.status, r.body)
	}
}
