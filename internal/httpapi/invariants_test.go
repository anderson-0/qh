package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/auth"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/testutil"
)

// headerRT adds headers to every request.
type headerRT struct{ headers map[string]string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// A workspace key always acts on its own workspace: naming another one in
// X-QH-Workspace neither reveals nor reaches that workspace's connections.
func TestWorkspaceKeyIgnoresWorkspaceHeader(t *testing.T) {
	e := newEnv(t, true, nil)
	if _, err := e.svc.CreateWorkspace(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "POST", "/api/v1/connections", e.admin, map[string]any{"name": "secret", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)},
		"X-QH-Workspace", "other"); r.status != 201 {
		t.Fatalf("create secret: %d %s", r.status, r.body)
	}
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	if r := e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "mine", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)}); r.status != 201 {
		t.Fatalf("create mine: %d %s", r.status, r.body)
	}
	other := []string{"X-QH-Workspace", "other"}

	var list []connectionJSON
	r := e.do(t, "GET", "/api/v1/connections", k, nil, other...)
	r.json(t, &list)
	if r.status != 200 || len(list) != 1 || list[0].Name != "mine" {
		t.Fatalf("list with a foreign workspace header: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections/secret", k, nil, other...), 404, "unknown_source", `no connection named "secret"`)
	expectProblem(t, e.do(t, "POST", "/api/v1/connections/secret/query", k, map[string]any{"query": "SELECT 1"}, other...), 404, "unknown_source", `no connection named "secret"`)
	expectProblem(t, e.do(t, "GET", "/api/v1/connections/secret/schema", k, nil, other...), 404, "unknown_source", `no connection named "secret"`)
	if r := e.do(t, "POST", "/api/v1/connections/mine/query", k, map[string]any{"query": "SELECT count(*) AS n FROM orders"}, other...); r.status != 200 || !strings.HasPrefix(r.body, "n\n250\n") {
		t.Fatalf("own connection with a foreign workspace header: %d %q", r.status, r.body)
	}

	ctx := context.Background()
	tr := &mcp.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp", DisableStandaloneSSE: true,
		HTTPClient: &http.Client{Transport: headerRT{map[string]string{"Authorization": "Bearer " + k, "X-QH-Workspace": "other"}}}}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "sources", Arguments: map[string]any{}})
	if err != nil || res.Content[0].(*mcp.TextContent).Text != "mine sqlite ro\n" {
		t.Fatalf("mcp sources: %v %+v", err, res)
	}
	res, err = s.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"source": "secret", "query": "SELECT 1"}})
	if err != nil || !res.IsError || !strings.HasPrefix(res.Content[0].(*mcp.TextContent).Text, "error[unknown_source]") {
		t.Fatalf("mcp query secret: %v %+v", err, res)
	}
}

// syncBuffer is a bytes.Buffer safe for the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The request log names the key by its prefix and never carries the key,
// the Authorization header, a connection string or its password.
func TestLogsCarryNoSecrets(t *testing.T) {
	var logs syncBuffer
	e := newEnv(t, true, func(c *Config) { c.Logger = slog.New(slog.NewJSONHandler(&logs, nil)) })
	k := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	prefix, secret, _ := auth.ParseToken(k)
	dsn := "postgres://u:hunter22@127.0.0.1:1/db?connect_timeout=2&sslmode=disable"
	r := e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "pg", "engine": "postgres", "dsn": dsn})
	expectProblem(t, r, 502, "connection_failed", "")
	if strings.Contains(r.body, "hunter22") {
		t.Fatalf("response leaks the password: %s", r.body)
	}
	if r := e.do(t, "POST", "/api/v1/connections", k, map[string]any{"name": "shop", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)}); r.status != 201 {
		t.Fatalf("create shop: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "POST", "/api/v1/connections/shop/query", k, map[string]any{"query": "SELECT nope FROM users"}), 422, "query_error", "")

	// Each request is logged after its response is written; wait for all three.
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(logs.String(), `"msg":"request"`) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := logs.String()
	if strings.Count(out, `"msg":"request"`) < 3 || !strings.Contains(out, `"key":"`+prefix+`"`) {
		t.Fatalf("missing request lines or key prefix:\n%s", out)
	}
	for _, s := range []string{"hunter22", dsn, k, secret, "Authorization", "Bearer"} {
		if strings.Contains(out, s) {
			t.Errorf("log contains %q:\n%s", s, out)
		}
	}
}
