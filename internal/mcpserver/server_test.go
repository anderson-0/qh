package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, _ := secret.NewKeyring(key)
	pools := pool.New(core.OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	svc := core.New(st, keys, pools, catalog.New(10*time.Minute))
	if _, _, err := svc.CreateConnection(ctx, store.DefaultWorkspace, core.ConnInput{
		Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t),
	}, true); err != nil {
		t.Fatal(err)
	}
	server := New(svc, func(context.Context) string { return store.DefaultWorkspace }, "test")
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestToolList(t *testing.T) {
	s := connect(t)
	res, err := s.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "query,schema,sources" && strings.Join(names, ",") != "sources,schema,query" {
		t.Fatalf("tools = %v", names)
	}
	b, _ := json.Marshal(res.Tools)
	if len(b) > 2400 {
		t.Fatalf("tool definitions are %d bytes, budget 2400 (~800 tokens):\n%s", len(b), b)
	}
}

func TestTools(t *testing.T) {
	s := connect(t)
	if out, isErr := call(t, s, "sources", map[string]any{}); isErr || out != "shop sqlite ro\n" {
		t.Fatalf("sources = %q", out)
	}
	if out, isErr := call(t, s, "query", map[string]any{"source": "shop", "query": "SELECT count(*) AS n FROM orders"}); isErr || !strings.HasPrefix(out, "n\n250\n-- 1 row · ") {
		t.Fatalf("query = %q", out)
	}
	if out, isErr := call(t, s, "query", map[string]any{"source": "shop", "query": "DELETE FROM users"}); !isErr || !strings.HasPrefix(out, "error[read_only]") {
		t.Fatalf("write = %q (isError %v)", out, isErr)
	}
	if out, isErr := call(t, s, "schema", map[string]any{"source": "shop", "object": "orders"}); isErr || !strings.Contains(out, "→ users.id") {
		t.Fatalf("schema = %q", out)
	}
	if out, isErr := call(t, s, "query", map[string]any{"source": "shop", "query": "SELECT id FROM orders", "max_rows": 2}); isErr || !strings.Contains(out, "-- 2 rows shown") {
		t.Fatalf("max_rows = %q", out)
	}
}

func TestInstructionsNameConnections(t *testing.T) {
	s := connect(t)
	in := s.InitializeResult().Instructions
	if !strings.HasPrefix(in, instructions+" ") || !strings.HasSuffix(in, "Connection: shop (sqlite, ro). source may be omitted.") {
		t.Fatalf("instructions = %q", in)
	}
	if out, isErr := call(t, s, "query", map[string]any{"query": "SELECT count(*) AS n FROM orders"}); isErr || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query without source = %q", out)
	}
	if out, isErr := call(t, s, "schema", map[string]any{"object": "orders"}); isErr || !strings.Contains(out, "→ users.id") {
		t.Fatalf("schema without source = %q", out)
	}
}

func TestConnectionsLine(t *testing.T) {
	if got := connectionsLine(nil); got != "No connections exist yet." {
		t.Errorf("none: %q", got)
	}
	var list []*core.ConnInfo
	for i := range maxListed + 2 {
		list = append(list, &core.ConnInfo{Name: fmt.Sprintf("db%d", i), Engine: "postgres", Mode: "rw"})
	}
	list[0].Description = "Billing"
	if got := connectionsLine(list[:2]); got != "Connections (pass one as source): db0 (postgres, rw): Billing; db1 (postgres, rw)." {
		t.Errorf("two: %q", got)
	}
	if got := connectionsLine(list); !strings.HasSuffix(got, "db19 (postgres, rw); and 2 more (sources lists all).") {
		t.Errorf("capped: %q", got)
	}
}
