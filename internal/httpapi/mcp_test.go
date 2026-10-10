package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/testutil"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpCall(t *testing.T, e *env, token, tool string, args map[string]any) (string, bool) {
	t.Helper()
	ctx := context.Background()
	tr := &mcp.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}, DisableStandaloneSSE: true}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestMCPOverHTTPIsWorkspaceScoped(t *testing.T) {
	e := newEnv(t, true, nil)
	if _, err := e.svc.CreateWorkspace(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	mine := e.key(t, "default", auth.ScopeQuery, auth.ScopeManage)
	theirs := e.key(t, "other", auth.ScopeQuery)
	if r := e.do(t, "POST", "/api/v1/connections", mine, map[string]any{"name": "shop", "engine": "sqlite", "dsn": testutil.SQLiteFixture(t)}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	if out, isErr := mcpCall(t, e, mine, "sources", map[string]any{}); isErr || out != "shop sqlite ro\n" {
		t.Fatalf("sources: %q", out)
	}
	if out, isErr := mcpCall(t, e, mine, "query", map[string]any{"source": "shop", "query": "SELECT count(*) AS n FROM orders"}); isErr || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q", out)
	}
	if out, _ := mcpCall(t, e, theirs, "sources", map[string]any{}); !strings.HasPrefix(out, "-- no connections") {
		t.Fatalf("other workspace sees: %q", out)
	}
	if out, isErr := mcpCall(t, e, theirs, "query", map[string]any{"source": "shop", "query": "SELECT 1"}); !isErr || !strings.HasPrefix(out, "error[unknown_source]") {
		t.Fatalf("other workspace queried shop: %q", out)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections/shop", theirs, nil), 404, "unknown_source", `no connection named "shop"`)
	if r := e.do(t, "POST", "/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); r.status != 401 {
		t.Fatalf("mcp without a key: %d", r.status)
	}
}
