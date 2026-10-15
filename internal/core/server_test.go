package core

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func newServiceWith(t *testing.T, st *store.SQLite, opts Options) *Service {
	t.Helper()
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, err := secret.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	svc := New(st, keys, pools, catalog.New(10*time.Minute), opts)
	t.Cleanup(svc.Close) // writes the queued query-log entries before the store closes
	return svc
}

func openStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSQLiteDisabled(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	db := testutil.SQLiteFixture(t)
	open := newServiceWith(t, st, Options{})
	if _, _, err := open.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, true); err != nil {
		t.Fatal(err)
	}
	locked := newServiceWith(t, st, Options{SQLiteDisabled: true})
	_, _, err := locked.CreateConnection(ctx, ws, ConnInput{Name: "other", Engine: "sqlite", DSN: db}, false)
	if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: SQLite connections are disabled on this server") {
		t.Fatalf("create: %v", err)
	}
	if _, err := locked.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT 1"}); err == nil || qerr.From(err).Category != qerr.Invalid {
		t.Fatalf("existing sqlite connection still usable: %v", err)
	}
	if _, err := locked.TestInput(ctx, ws, ConnInput{Engine: "sqlite", DSN: db}); err == nil {
		t.Fatal("TestInput allowed sqlite")
	}
}

// copyFixture copies the SQLite fixture to dir/name.
func copyFixture(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(testutil.SQLiteFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Each workspace is confined to its own directory under the SQLite root.
func TestSQLiteRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mine := copyFixture(t, filepath.Join(root, ws), "a.db")
	theirs := copyFixture(t, filepath.Join(root, "team"), "b.db")
	shared := copyFixture(t, root, "c.db")
	outside := testutil.SQLiteFixture(t)
	svc := newServiceWith(t, openStore(t), Options{SQLiteRoot: root})
	if _, err := svc.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "in", Engine: "sqlite", DSN: mine}, true); err != nil {
		t.Fatalf("inside: %v", err)
	}
	if _, err := svc.TestInput(ctx, ws, ConnInput{Engine: "sqlite", DSN: mine}); err != nil {
		t.Fatalf("test input inside: %v", err)
	}
	if _, _, err := svc.CreateConnection(ctx, "team", ConnInput{Name: "in", Engine: "sqlite", DSN: theirs}, true); err != nil {
		t.Fatalf("team inside: %v", err)
	}
	for _, dsn := range []string{theirs, shared, outside} {
		if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "out", Engine: "sqlite", DSN: dsn}, true); err == nil || !strings.Contains(err.Error(), "outside the allowed directory") {
			t.Errorf("create %s: %v", dsn, err)
		}
		if _, err := svc.TestInput(ctx, ws, ConnInput{Engine: "sqlite", DSN: dsn}); err == nil || !strings.Contains(err.Error(), "outside the allowed directory") {
			t.Errorf("test input %s: %v", dsn, err)
		}
	}
	// A row that points into another workspace's directory (stored without
	// a test) is refused when it is used.
	if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "sneaky", Engine: "sqlite", DSN: theirs}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Query(ctx, ws, QueryInput{Source: "sneaky", Query: "SELECT 1"}); err == nil || !strings.Contains(err.Error(), "outside the allowed directory") {
		t.Errorf("query sneaky: %v", err)
	}
}

func TestDialOption(t *testing.T) {
	ctx := context.Background()
	called := false
	svc := newServiceWith(t, openStore(t), Options{Dial: func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("refused by policy")
	}})
	_, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u:hunter22@db.example.com/db?connect_timeout=2"}, true)
	if !called || err == nil || !strings.Contains(err.Error(), "refused by policy") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func TestQueryResultCatalogAndTestInput(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	r, err := h.svc.QueryResult(ctx, ws, QueryInput{Source: "shop", Query: "SELECT count(*) AS n FROM orders"})
	if err != nil || len(r.Columns) != 1 || r.Columns[0].Name != "n" || r.Rows[0][0].Text != "250" {
		t.Fatalf("query result: %+v %v", r, err)
	}
	cat, err := h.svc.Catalog(ctx, ws, "shop")
	if err != nil || len(cat.Lookup("users")) != 1 {
		t.Fatalf("catalog: %v", err)
	}
	tr, err := h.svc.TestInput(ctx, ws, ConnInput{Engine: "sqlite", DSN: testutil.SQLiteFixture(t)})
	if err != nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("test input: %+v %v", tr, err)
	}
	if _, err := h.svc.TestInput(ctx, ws, ConnInput{Engine: "sqlite", DSN: filepath.Join(t.TempDir(), "missing.db")}); qerr.From(err).Category != qerr.ConnectionFailed {
		t.Fatalf("missing file: %v", err)
	}
}

func TestWorkspaceOperations(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	if _, err := h.svc.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"team":  `error[invalid]: a workspace named "team" already exists`,
		"Team!": `error[invalid]: invalid workspace name "Team!"`,
		"":      `error[invalid]: invalid workspace name ""`,
	} {
		if _, err := h.svc.CreateWorkspace(ctx, name); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v", name, err)
		}
	}
	list, err := h.svc.ListWorkspaces(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "default" || list[1].Name != "team" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, _, err := h.svc.CreateConnection(ctx, "team", ConnInput{Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t)}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Query(ctx, "team", QueryInput{Source: "shop", Query: "SELECT 1"}); err != nil {
		t.Fatal(err) // opens a pool for the connection
	}
	if ok, _ := h.svc.WorkspaceExists(ctx, "team"); !ok {
		t.Fatal("team should exist")
	}
	if err := h.svc.DeleteWorkspace(ctx, "default"); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: the default workspace cannot be deleted") {
		t.Fatalf("delete default: %v", err)
	}
	if err := h.svc.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.svc.WorkspaceExists(ctx, "team"); ok {
		t.Fatal("team still exists")
	}
	if err := h.svc.DeleteWorkspace(ctx, "team"); err == nil || !strings.HasPrefix(err.Error(), `error[unknown_source]: no workspace named "team"`) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := h.svc.Query(ctx, "team", QueryInput{Source: "shop", Query: "SELECT 1"}); qerr.From(err).Category != qerr.UnknownSource {
		t.Fatalf("connection outlived its workspace: %v", err)
	}
}

func TestServerDSNGate(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, err := secret.NewKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	bad := "postgres://u:hunter22@db.example.com/db?connect_timeout=2&passfile=/etc/hosts"
	// A row stored before the gate existed (or by local mode on the same store).
	local := New(st, keys, pools, catalog.New(10*time.Minute))
	t.Cleanup(local.Close)
	if _, _, err := local.CreateConnection(ctx, ws, ConnInput{Name: "legacy", Engine: "postgres", DSN: bad}, false); err != nil {
		t.Fatal(err)
	}
	dialed := false
	server := New(st, keys, pools, catalog.New(10*time.Minute), Options{Dial: func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("refused by policy")
	}})
	t.Cleanup(server.Close)
	const want = "error[invalid]: this server only accepts these Postgres connection settings"
	check := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "hunter22") || strings.Contains(err.Error(), "/etc/hosts") {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, _, err = server.CreateConnection(ctx, ws, ConnInput{Name: "new", Engine: "postgres", DSN: bad}, false)
	check("create", err)
	_, err = server.TestInput(ctx, ws, ConnInput{Engine: "postgres", DSN: bad})
	check("test input", err)
	desc := "changed"
	_, _, err = server.UpdateConnection(ctx, ws, "legacy", ConnPatch{Description: &desc}, false)
	check("update", err)
	_, err = server.Query(ctx, ws, QueryInput{Source: "legacy", Query: "SELECT 1"})
	check("query (resolve)", err)
	_, err = server.TestConnection(ctx, ws, "legacy")
	check("test connection (resolve)", err)
	info, err := server.GetConnection(ctx, ws, "legacy")
	if err != nil || strings.Contains(info.DSN, "passfile") || strings.Contains(info.DSN, "db.example.com") || !strings.HasPrefix(info.DSN, "<") {
		t.Errorf("info: %+v %v", info, err)
	}
	if dialed {
		t.Error("dialed despite a refused setting")
	}
	if _, _, err := server.CreateConnection(ctx, ws, ConnInput{Name: "ok", Engine: "postgres", DSN: "postgres://u:hunter22@db.example.com/db"}, false); err != nil {
		t.Errorf("allowed DSN refused: %v", err)
	}
}
