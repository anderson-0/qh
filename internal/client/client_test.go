package client

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/httpapi"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

// server starts a server-mode API and returns its URL and an admin key.
func server(t *testing.T) (string, string) {
	t.Helper()
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
	svc := core.New(st, keys, pools, catalog.New(time.Minute))
	admin, _, err := auth.Bootstrap(context.Background(), st, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Config{Service: svc, Store: st, Auth: auth.NewAuthenticator(st), Version: "test"}))
	t.Cleanup(srv.Close)
	return srv.URL, admin
}

func TestClientAgainstServer(t *testing.T) {
	ctx := context.Background()
	url, admin := server(t)
	a, err := New(url, admin, "")
	if err != nil {
		t.Fatal(err)
	}
	if ws, err := a.CreateWorkspace(ctx, "team"); err != nil || ws.Name != "team" {
		t.Fatalf("workspace: %+v %v", ws, err)
	}
	if list, _ := a.ListWorkspaces(ctx); len(list) != 2 {
		t.Fatalf("workspaces: %+v", list)
	}
	tok, info, err := a.CreateKey(ctx, auth.KeyInput{Workspace: "team", Scopes: []string{"query", "manage"}, Name: "ci", ExpiresIn: 48 * time.Hour})
	if err != nil || !strings.HasPrefix(tok, "qh_") || info.Workspace != "team" || info.ExpiresAt == nil {
		t.Fatalf("key: %q %+v %v", tok, info, err)
	}
	c, _ := New(url, tok, "")
	db := testutil.SQLiteFixture(t)
	ci, tr, err := c.CreateConnection(ctx, core.ConnInput{Name: "shop", Engine: "sqlite", Database: db, Description: "Shop"}, true)
	if err != nil || ci.Name != "shop" || ci.Database != db || tr == nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("create: %+v %+v %v", ci, tr, err)
	}
	f := testutil.NewTLSFixture(t)
	pg, _, err := c.CreateConnection(ctx, core.ConnInput{Name: "pg", Engine: "postgres", Host: "db.example.com", Username: "u",
		Password: "hunter22-pw", TLS: &engine.TLS{Mode: engine.TLSVerifyFull, CA: f.CA}}, false)
	if err != nil || pg.User != "u" || pg.TLS == nil || pg.TLS.Mode != "verify-full" || !pg.TLS.CA {
		t.Fatalf("pg: %+v %v", pg, err)
	}
	pg, _, err = c.UpdateConnection(ctx, "pg", core.ConnPatch{ClearTLS: true}, false)
	if err != nil || pg.TLS != nil || pg.Version != 2 {
		t.Fatalf("clear tls: %+v %v", pg, err)
	}
	if list, _ := c.ListConnections(ctx); len(list) != 2 || list[1].Name != "shop" {
		t.Fatalf("list: %+v", list)
	}
	if got, err := c.GetConnection(ctx, "shop"); err != nil || got.Description != "Shop" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if tr, err := c.TestConnection(ctx, "shop"); err != nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("test: %+v %v", tr, err)
	}
	if out, err := c.Query(ctx, core.QueryInput{Source: "shop", Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query: %q %v", out, err)
	}
	if out, err := c.Schema(ctx, core.SchemaInput{Source: "shop", Pattern: "user*"}); err != nil || !strings.Contains(out, "main.users") {
		t.Fatalf("schema: %q %v", out, err)
	}
	if log, err := c.QueryLog(ctx, core.QueryLogFilter{Connection: "shop", Since: time.Now().Add(-time.Hour), Limit: 10}); err != nil ||
		len(log) != 1 || log[0].Rows != 1 || log[0].KeyID != info.ID {
		t.Fatalf("log: %+v %v", log, err)
	}
	if _, err := c.Query(ctx, core.QueryInput{Source: "nope", Query: "SELECT 1"}); err == nil || !strings.HasPrefix(err.Error(), `error[unknown_source]: no connection named "nope"`) {
		t.Fatalf("unknown: %v", err)
	}
	if err := c.DeleteConnection(ctx, "pg"); err != nil {
		t.Fatal(err)
	}
	keys, err := a.ListKeys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys: %+v %v", keys, err)
	}
	if err := a.DeleteKey(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListConnections(ctx); err == nil || !strings.HasPrefix(err.Error(), "error[auth]: invalid API key") {
		t.Fatalf("revoked key: %v", err)
	}
	if err := a.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ListConnections(ctx); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: admin keys must name a workspace") {
		t.Fatalf("admin without workspace: %v", err)
	}
	inDefault, _ := New(url, admin, "default")
	if list, err := inDefault.ListConnections(ctx); err != nil || len(list) != 0 {
		t.Fatalf("admin with workspace: %+v %v", list, err)
	}
}

func TestNewRefuses(t *testing.T) {
	for _, c := range []struct{ base, key, want string }{
		{"http://db.example.com:8080", "qh_k", "error[invalid]: refusing to send an API key over plain http to db.example.com:8080"},
		{"ftp://x", "qh_k", "error[invalid]: --server must be an http(s) URL"},
		{"https://user:pw@x", "qh_k", "error[invalid]: --server must be an http(s) URL"},
		{"https://qh.example.com", "", "error[invalid]: remote mode needs an API key"},
	} {
		if _, err := New(c.base, c.key, ""); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v", c.base, err)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:8765", "http://localhost:8765/", "http://[::1]:8765", "https://qh.example.com"} {
		if _, err := New(ok, "qh_k", ""); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	c, _ := New("http://127.0.0.1:1", "qh_secretkey", "")
	_, err := c.ListConnections(context.Background())
	if err == nil || err.Error() != "error[connection_failed]: cannot reach qh server at http://127.0.0.1:1" {
		t.Fatalf("unreachable: %v", err)
	}
}
