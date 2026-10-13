package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	_ "github.com/anderson-0/qh/internal/engine/mongodb"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

var updateSurface = flag.Bool("update-surface", false, "rewrite testdata/sql_surface.golden")

func connect(t *testing.T) *mcp.ClientSession {
	s, _ := connectSvc(t)
	return s
}

func connectSvc(t *testing.T) (*mcp.ClientSession, *core.Service) {
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
	return session, svc
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
	if strings.Join(names, ",") != "query,schema" && strings.Join(names, ",") != "schema,query" {
		t.Fatalf("tools = %v", names)
	}
	b, _ := json.Marshal(res.Tools)
	if len(b) > 2400 {
		t.Fatalf("tool definitions are %d bytes, budget 2400 (~800 tokens):\n%s", len(b), b)
	}
}

func TestTools(t *testing.T) {
	s := connect(t)
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
	list[1].Description = strings.Repeat("x", 80)
	if got := connectionsLine(list[:2]); !strings.Contains(got, "db1 (postgres, rw): "+strings.Repeat("x", maxDescription-1)+"…") {
		t.Errorf("clipped: %q", got)
	}
	if got := connectionsLine(list); !strings.HasSuffix(got, "db19 (postgres, rw); and 2 more (schema lists all).") {
		t.Errorf("capped: %q", got)
	}
}

func TestClipDescription(t *testing.T) {
	long := "Pagila: a DVD rental store (films, actors, customers, rentals, payments) on PostgreSQL"
	if got := clipDescription(long); got != "Pagila: a DVD rental store (films, actors, customers, renta…" || utf8.RuneCountInString(got) != maxDescription {
		t.Errorf("long: %q", got)
	}
	if got := clipDescription("Billing\n\treplica"); got != "Billing replica" {
		t.Errorf("whitespace: %q", got)
	}
	multi := strings.Repeat("é", 70)
	if got := clipDescription(multi); !utf8.ValidString(got) || utf8.RuneCountInString(got) != maxDescription {
		t.Errorf("multi-byte: %q", got)
	}
	exact := strings.Repeat("a", maxDescription)
	if got := clipDescription(exact); got != exact {
		t.Errorf("exactly %d runes: %q", maxDescription, got)
	}
	if got := clipDescription(exact + "a"); got != strings.Repeat("a", maxDescription-1)+"…" {
		t.Errorf("%d runes: %q", maxDescription+1, got)
	}
	if got := clipDescription("short"); got != "short" {
		t.Errorf("short: %q", got)
	}
}

func TestSchemaWithoutArgumentsListsConnectionsWhenSeveral(t *testing.T) {
	s, svc := connectSvc(t)
	ctx := context.Background()
	// One connection: the overview, as before.
	if out, isErr := call(t, s, "schema", map[string]any{}); isErr || !strings.HasPrefix(out, "main: 3 tables, 1 view\n") {
		t.Fatalf("one connection: %q", out)
	}
	// A second connection appears after the session started.
	if _, _, err := svc.CreateConnection(ctx, store.DefaultWorkspace, core.ConnInput{
		Name: "crm", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), Description: "CRM",
	}, false); err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(t, s, "schema", map[string]any{}); isErr || out != severalHeader+"crm sqlite ro — CRM\nshop sqlite ro\n" {
		t.Fatalf("two connections: %q", out)
	}
	// A filter without a source is not silently dropped.
	if out, isErr := call(t, s, "schema", map[string]any{"pattern": "*order*"}); !isErr || !strings.HasPrefix(out, "error[unknown_source]") {
		t.Fatalf("pattern without source: %q (isError %v)", out, isErr)
	}
	if out, isErr := call(t, s, "schema", map[string]any{"source": "crm", "object": "orders"}); isErr || !strings.Contains(out, "→ users.id") {
		t.Fatalf("explicit source: %q", out)
	}
}

func TestSchemaWithoutArgumentsAndNoConnections(t *testing.T) {
	s, svc := connectSvc(t)
	if err := svc.DeleteConnection(context.Background(), store.DefaultWorkspace, "shop"); err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(t, s, "schema", map[string]any{}); isErr || !strings.HasPrefix(out, "-- no connections") {
		t.Fatalf("no connections: %q (isError %v)", out, isErr)
	}
}

// TestSQLSurfaceUnchanged pins the tool definitions and instructions a
// workspace with only SQL connections sees: every byte is paid on every
// model turn, and the benchmark margin over dbhub depends on them.
func TestSQLSurfaceUnchanged(t *testing.T) {
	s := connect(t)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(struct {
		Instructions string
		Tools        any
	}{s.InitializeResult().Instructions, tools.Tools}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "sql_surface.golden")
	if *updateSurface {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Fatalf("SQL-only tool surface changed:\n%s", b)
	}
}

func TestInstructionsMentionMongoshOnlyWithMongo(t *testing.T) {
	s, svc := connectSvc(t)
	if strings.Contains(s.InitializeResult().Instructions, "mongosh") {
		t.Fatal("SQL-only workspace got the mongosh sentence")
	}
	for _, name := range []string{"events", "logs"} {
		if _, _, err := svc.CreateConnection(context.Background(), store.DefaultWorkspace, core.ConnInput{
			Name: name, Engine: "mongodb", DSN: "mongodb://127.0.0.1:1/" + name,
		}, false); err != nil {
			t.Fatal(err)
		}
	}
	server := New(svc, func(context.Context) string { return store.DefaultWorkspace }, "test")
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	s2, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	in := s2.InitializeResult().Instructions
	if strings.Count(in, mongoHint) != 1 || !strings.HasSuffix(in, mongoHint) {
		t.Fatalf("instructions: %q", in)
	}
}
