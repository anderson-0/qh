package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anderson-0/qh/internal/testutil"
)

// buildBinary builds cmd/qh once per test into a temp dir.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "qh")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestServeEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	// Each workspace's SQLite files live under <sqlite-root>/<workspace>/.
	root := t.TempDir()
	fixture, err := os.ReadFile(testutil.SQLiteFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(root, "team", "shop.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	cmd := exec.Command(bin, "serve", "--http", "127.0.0.1:0", "--data-dir", t.TempDir(), "--sqlite-root", root)
	cmd.Env = append(os.Environ(), "QH_MASTER_KEY="+base64.StdEncoding.EncodeToString(key), "QH_BOOTSTRAP_ADMIN_KEY=")
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	var base, admin string
	lines := bufio.NewScanner(stderr)
	deadline := time.After(20 * time.Second)
	found := make(chan struct{})
	go func() {
		for lines.Scan() {
			l := lines.Text()
			if v, ok := strings.CutPrefix(l, "admin API key (shown once): "); ok {
				admin = v
			}
			if v, ok := strings.CutPrefix(l, "qh listening on "); ok {
				base = strings.Fields(v)[0]
				close(found)
				break
			}
		}
		io.Copy(io.Discard, stderr)
	}()
	select {
	case <-found:
	case <-deadline:
		t.Fatal("server did not start")
	}
	if admin == "" {
		t.Fatal("no admin key printed")
	}

	call := func(method, path, token string, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, _ := call("GET", "/api/v1/connections", "", nil); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code, body := call("POST", "/api/v1/workspaces", admin, map[string]any{"name": "team"}); code != 201 {
		t.Fatalf("workspace: %d %s", code, body)
	}
	code, body := call("POST", "/api/v1/keys", admin, map[string]any{"workspace": "team", "scopes": []string{"query", "manage"}})
	var k struct{ Key string }
	json.Unmarshal([]byte(body), &k)
	if code != 201 || k.Key == "" {
		t.Fatalf("key: %d %s", code, body)
	}
	if code, body := call("POST", "/api/v1/connections", k.Key, map[string]any{"name": "shop", "engine": "sqlite", "dsn": db}); code != 201 {
		t.Fatalf("sqlite inside root: %d %s", code, body)
	}
	if code, body := call("POST", "/api/v1/connections", k.Key, map[string]any{"name": "evil", "engine": "postgres", "dsn": "postgres://u:p@127.0.0.1:5432/db?connect_timeout=2"}); code != 502 || !strings.Contains(body, "not allowed by this server's network policy") {
		t.Fatalf("loopback postgres: %d %s", code, body)
	}
	if code, body := call("POST", "/api/v1/connections/shop/query", k.Key, map[string]any{"query": "SELECT count(*) AS n FROM orders"}); code != 200 || !strings.Contains(body, `"rows":[["250"]]`) {
		t.Fatalf("query: %d %s", code, body)
	}
	remote := exec.Command(bin, "--server", base, "--key", k.Key, "conn", "ls")
	if out, err := remote.CombinedOutput(); err != nil || !strings.Contains(string(out), "shop") {
		t.Fatalf("remote CLI: %v %s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tr := &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{k.Key}}, DisableStandaloneSSE: true}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "sources", Arguments: map[string]any{}})
	if err != nil || res.Content[0].(*mcp.TextContent).Text != "shop sqlite ro\n" {
		t.Fatalf("mcp sources: %v %+v", err, res)
	}
	session.Close()

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not shut down")
	}
}
