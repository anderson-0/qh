package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"testing"
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
	if strings.Join(names, ",") != "query,schema,sources" || srv.Name() != "qh" {
		t.Fatalf("tools %v", names)
	}
	if !strings.Contains(srv.Instructions(), "Use sources to pick a connection") {
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

func TestDBHubEnvIsAnAllowList(t *testing.T) {
	in := []string{"PATH=/usr/bin", "HOME=/h", "USER=u", "LANG=en_US.UTF-8", "LC_ALL=C", "TMPDIR=/t", "TEMP=/t", "TMP=/t",
		"NODE_OPTIONS=--x", "npm_config_cache=/c", "NVM_DIR=/n", "SHELL=/bin/zsh",
		"DSN=postgres://secret", "DBHUB_DSN=x", "DB_TYPE=mysql", "TRANSPORT=http", "PORT=1", "READONLY=false", "ANTHROPIC_API_KEY=sk", "QH_KEY=k", "malformed"}
	got := strings.Join(dbhubEnv(in), "\n")
	for _, keep := range []string{"PATH=", "HOME=", "USER=", "LANG=", "LC_ALL=", "TMPDIR=", "TEMP=", "TMP=", "NODE_OPTIONS=", "npm_config_cache=", "NVM_DIR=", "SHELL="} {
		if !strings.Contains(got, keep) {
			t.Errorf("dropped %s", keep)
		}
	}
	for _, drop := range []string{"DSN", "DBHUB_DSN", "DB_TYPE", "TRANSPORT", "PORT", "READONLY", "ANTHROPIC_API_KEY", "QH_KEY", "malformed"} {
		if strings.Contains(got, drop) {
			t.Errorf("kept %s", drop)
		}
	}
}
