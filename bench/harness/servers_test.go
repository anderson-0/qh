package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func chinook(t *testing.T) *Dataset {
	t.Helper()
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	ds, stop, err := OpenChinook(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ds
}

func TestQHServer(t *testing.T) {
	ds := chinook(t)
	ctx := context.Background()
	bin, cleanup, err := BuildQH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	srv, err := StartQH(ctx, bin, []Conn{ConnFor(ds)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var names []string
	for _, tl := range srv.Tools() {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "query,schema" || srv.Name() != "qh" {
		t.Fatalf("tools %v", names)
	}
	if in := srv.Instructions(); !strings.HasPrefix(in, "Query directly if you know the names; else call schema first.") || !strings.Contains(in, "Connection: chinook (") {
		t.Fatalf("instructions %q", srv.Instructions())
	}
	out, isErr := srv.Call(ctx, "query", json.RawMessage(`{"source":"chinook","query":"SELECT count(*) AS n FROM Track"}`))
	if isErr || !strings.HasPrefix(out, "n\n3503\n") {
		t.Fatalf("query: %q %v", out, isErr)
	}
	if out, isErr := srv.Call(ctx, "nope", json.RawMessage(`{}`)); !isErr || out == "" {
		t.Fatalf("unknown tool: %q %v", out, isErr)
	}
}

func TestDBHubServer(t *testing.T) {
	ds := chinook(t)
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not installed")
	}
	ctx := context.Background()
	srv, err := StartDBHub(ctx, DefaultDBHubVersion, []Conn{ConnFor(ds)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	exec := FindTool(srv.Tools(), "execute_sql")
	if exec == "" || srv.Name() != "dbhub" {
		t.Fatalf("tools: %v", srv.Tools())
	}
	out, isErr := srv.Call(ctx, exec, json.RawMessage(`{"sql":"SELECT count(*) AS n FROM Track"}`))
	if isErr || !strings.Contains(out, "3503") {
		t.Fatalf("execute_sql: %q %v", out, isErr)
	}
	if out, isErr := srv.Call(ctx, exec, json.RawMessage(`{"sql":"DELETE FROM Track"}`)); !isErr && !strings.Contains(strings.ToLower(out), "read") {
		t.Fatalf("dbhub accepted a write in read-only mode: %q", out)
	}
}

func TestNodeEnvIsAnAllowList(t *testing.T) {
	in := []string{"PATH=/usr/bin", "HOME=/h", "USER=u", "LANG=en_US.UTF-8", "LC_ALL=C", "TMPDIR=/t", "TEMP=/t", "TMP=/t",
		"NODE_OPTIONS=--x", "npm_config_cache=/c", "NVM_DIR=/n", "SHELL=/bin/zsh",
		"HTTP_PROXY=p", "https_proxy=p", "NO_PROXY=n", "no_proxy=n", "ALL_PROXY=a", "all_proxy=a", "HTTPS_PROXY=p", "http_proxy=p",
		"NPM_CONFIG_REGISTRY=r", "SSL_CERT_FILE=/f", "SSL_CERT_DIR=/d", "NODE_EXTRA_CA_CERTS=/c", "XDG_CONFIG_HOME=/x",
		"DSN=postgres://secret", "DBHUB_DSN=x", "DB_TYPE=mysql", "TRANSPORT=http", "PORT=1", "READONLY=false", "MDB_MCP_CONNECTION_STRING=mongodb://secret", "MDB_MCP_READ_ONLY=false", "ANTHROPIC_API_KEY=sk", "QH_KEY=k", "malformed"}
	got := strings.Join(nodeEnv(in), "\n")
	for _, keep := range []string{"PATH=", "HOME=", "USER=", "LANG=", "LC_ALL=", "TMPDIR=", "TEMP=", "TMP=", "NODE_OPTIONS=", "npm_config_cache=", "NVM_DIR=", "SHELL=",
		"HTTP_PROXY=", "https_proxy=", "NO_PROXY=", "no_proxy=", "ALL_PROXY=", "all_proxy=", "HTTPS_PROXY=", "http_proxy=",
		"NPM_CONFIG_REGISTRY=", "SSL_CERT_FILE=", "SSL_CERT_DIR=", "NODE_EXTRA_CA_CERTS=", "XDG_CONFIG_HOME="} {
		if !strings.Contains(got, keep) {
			t.Errorf("dropped %s", keep)
		}
	}
	for _, drop := range []string{"DSN", "DBHUB_DSN", "DB_TYPE", "TRANSPORT", "PORT", "READONLY", "MDB_MCP_CONNECTION_STRING", "MDB_MCP_READ_ONLY", "ANTHROPIC_API_KEY", "QH_KEY", "malformed"} {
		if strings.Contains(got, drop) {
			t.Errorf("kept %s", drop)
		}
	}
}

func TestStartMongoMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker, npx and the npm registry")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not installed")
	}
	ctx := context.Background()
	gen, stop, err := StartGeneratedMongo(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	srv, err := StartMongoMCP(ctx, DefaultMongoMCPVersion, []Conn{ConnFor(gen)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	for _, prefix := range []string{"list-collections", "find", "aggregate"} {
		if FindTool(srv.Tools(), prefix) == "" {
			t.Errorf("no %s tool in %v", prefix, ToolDefs(srv.Tools()))
		}
	}
	for _, tl := range srv.Tools() {
		for _, w := range []string{"insert", "update", "delete", "drop", "create", "rename"} {
			if strings.Contains(tl.Name, w) {
				t.Errorf("write tool %s registered despite --readOnly", tl.Name)
			}
		}
	}
	name, args := SchemaCall("mongodb-mcp", gen)
	out, isErr := srv.Call(ctx, FindTool(srv.Tools(), name), args)
	if isErr || !strings.Contains(out, "t0001") {
		t.Fatalf("list-collections: %v %s", isErr, out)
	}
	if _, err := StartMongoMCP(ctx, DefaultMongoMCPVersion, []Conn{ConnFor(gen), ConnFor(gen)}); err == nil {
		t.Error("two connections accepted")
	}
}

func TestFindToolPrefersAnExactName(t *testing.T) {
	tools := []*mcp.Tool{{Name: "aggregate-db"}, {Name: "aggregate"}, {Name: "search_objects"}}
	if got := FindTool(tools, "aggregate"); got != "aggregate" {
		t.Errorf("got %q", got)
	}
	if got := FindTool(tools, "search"); got != "search_objects" {
		t.Errorf("prefix: got %q", got)
	}
	if got := FindTool(tools, "find"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestToolboxEnv(t *testing.T) {
	for _, tc := range []struct {
		c    Conn
		want string
	}{
		{Conn{Name: "pagila", Engine: "postgres", DSN: "postgres://postgres:bench-pw@localhost:55001/pagila?sslmode=disable"},
			"POSTGRES_HOST=localhost POSTGRES_PORT=55001 POSTGRES_DATABASE=pagila POSTGRES_USER=postgres POSTGRES_PASSWORD=bench-pw"},
		{Conn{Name: "pg", Engine: "postgres", DSN: "postgres://u@db/x"},
			"POSTGRES_HOST=db POSTGRES_PORT=5432 POSTGRES_DATABASE=x POSTGRES_USER=u POSTGRES_PASSWORD="},
		{Conn{Name: "sakila", Engine: "mysql", DSN: "mysql://bench:bench-pw@127.0.0.1:55002/sakila"},
			"MYSQL_HOST=127.0.0.1 MYSQL_PORT=55002 MYSQL_DATABASE=sakila MYSQL_USER=bench MYSQL_PASSWORD=bench-pw"},
		{Conn{Name: "chinook", Engine: "sqlite", DSN: "/tmp/x/chinook.sqlite"}, "SQLITE_DATABASE=/tmp/x/chinook.sqlite"},
	} {
		env, err := toolboxEnv(tc.c)
		if got := strings.Join(env, " "); err != nil || got != tc.want {
			t.Errorf("%s: %q %v", tc.c.Name, got, err)
		}
	}
	if _, err := toolboxEnv(Conn{Name: "m", Engine: "mongodb", DSN: "mongodb://x"}); err == nil {
		t.Error("mongodb accepted")
	}
}

func TestToolboxServer(t *testing.T) {
	ds := chinook(t)
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not installed")
	}
	ctx := context.Background()
	for _, toolset := range []string{"", "data"} {
		srv, err := StartToolbox(ctx, DefaultToolboxVersion, toolset, []Conn{ConnFor(ds)})
		if err != nil {
			t.Fatal(err)
		}
		defer srv.Close()
		var names []string
		for _, tl := range srv.Tools() {
			names = append(names, tl.Name)
		}
		sort.Strings(names)
		want := map[string]string{"": "toolbox", "data": "toolbox-data"}[toolset]
		if strings.Join(names, ",") != "execute_sql,list_tables" || srv.Name() != want {
			t.Fatalf("%s: tools %v", srv.Name(), names)
		}
		prefix, args := SchemaCall(srv.Name(), ds)
		if out, isErr := srv.Call(ctx, prefix, args); isErr || !strings.Contains(out, "Track") {
			t.Fatalf("list_tables: %q %v", out, isErr)
		}
		prefix, argsFor := QueryCall(srv.Name(), ds)
		if out, isErr := srv.Call(ctx, prefix, argsFor("SELECT count(*) AS n FROM Track")); isErr || !strings.Contains(out, "3503") {
			t.Fatalf("execute_sql: %q %v", out, isErr)
		}
	}
	if _, err := StartToolbox(ctx, DefaultToolboxVersion, "", []Conn{ConnFor(ds), ConnFor(ds)}); err == nil {
		t.Error("two connections accepted")
	}
}
