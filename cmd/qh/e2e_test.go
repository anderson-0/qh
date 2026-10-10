package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestStdioEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	qh := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(bin, append([]string{"--home", home}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("qh %v: %v\n%s", args, err, out)
		}
	}
	qh("conn", "add", "shop", "--engine", "sqlite", "--dsn", db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Stdout must carry only protocol frames, so the session working at all
	// proves it; stderr is captured to check a successful run logs no errors.
	var stderr bytes.Buffer
	cmd := exec.Command(bin, "--home", home, "mcp")
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			session.Close()
		}
	}()

	text := func(name string, args map[string]any) string {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	if out := text("query", map[string]any{"source": "shop", "query": "SELECT count(*) AS n FROM orders"}); !strings.HasPrefix(out, "n\n250\n-- 1 row · ") {
		t.Fatalf("query: %q", out)
	}
	// A connection added while the server runs is visible on the next call.
	qh("conn", "add", "shop2", "--engine", "sqlite", "--dsn", db)
	if out := text("sources", map[string]any{}); !strings.Contains(out, "shop2 sqlite ro") {
		t.Fatalf("sources: %q", out)
	}

	// Closing the client must shut the server down cleanly.
	closed = true
	if err := session.Close(); err != nil {
		t.Logf("close: %v", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("qh mcp process was not reaped by Close")
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("qh mcp exit code %d; stderr:\n%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "error[") {
		t.Fatalf("stderr has errors on a successful run:\n%s", stderr.String())
	}
}
