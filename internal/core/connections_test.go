package core

import (
	"bytes"
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
	"github.com/anderson-0/qh/internal/testutil"
)

// leakyEngine puts its connection string and password into every error.
type leakyEngine struct{}

func (leakyEngine) Name() string          { return "leaky" }
func (leakyEngine) Dialect() sqlx.Dialect { return sqlx.Postgres }
func (leakyEngine) Inspect(dsn string) (engine.DSNInfo, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return engine.DSNInfo{}, qerr.New(qerr.Invalid, "bad leaky dsn")
	}
	pw, _ := u.User.Password()
	return engine.DSNInfo{Host: u.Host, User: u.User.Username(), Password: pw, Redacted: "leaky://" + u.User.Username() + ":***@" + u.Host}, nil
}
func (leakyEngine) Open(_ context.Context, s engine.ConnSpec) (engine.Conn, error) {
	return nil, qerr.New(qerr.ConnectionFailed, "cannot connect with "+s.DSN+" (password hunter22)")
}

func init() { engine.Register(leakyEngine{}) }

func TestSecretsAreSealedAndScrubbed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dsn := "leaky://bob:hunter22@db.example.com"
	_, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "leaky", Engine: "leaky", DSN: dsn}, true)
	if err == nil || strings.Contains(err.Error(), "hunter22") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("test error not scrubbed: %v", err)
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "leaky", Engine: "leaky", DSN: dsn}, false); err != nil {
		t.Fatal(err)
	}
	rec, _ := h.st.GetConnection(ctx, ws, "leaky")
	if bytes.Contains(rec.Secret, []byte("hunter22")) {
		t.Fatal("password stored in plaintext")
	}
	info, _ := h.svc.GetConnection(ctx, ws, "leaky")
	if info.DSN != "leaky://bob:***@db.example.com" || info.Host != "db.example.com" || info.User != "bob" {
		t.Fatalf("info = %+v", info)
	}
	_, err = h.svc.Query(ctx, ws, QueryInput{Source: "leaky", Query: "SELECT 1"})
	if err == nil || strings.Contains(err.Error(), "hunter22") || !strings.HasPrefix(err.Error(), "error[connection_failed]") {
		t.Fatalf("query error not scrubbed: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	db := testutil.SQLiteFixture(t)
	missing := filepath.Join(t.TempDir(), "missing.db")
	cases := []struct {
		in   ConnInput
		test bool
		want string
	}{
		{ConnInput{Name: "Shop!", Engine: "sqlite", DSN: db}, true, `error[invalid]: invalid connection name "Shop!"`},
		{ConnInput{Name: "shop", Engine: "oracle", DSN: db}, true, `error[invalid]: unknown engine "oracle"`},
		{ConnInput{Name: "shop", Engine: "sqlite"}, true, "error[invalid]: connection string is required"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, Mode: "admin"}, true, "error[invalid]: mode must be ro or rw"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, MaxRows: 20000}, true, "error[invalid]: max_rows must be between 1 and 10000"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, TimeoutMS: 400000}, true, "error[invalid]: timeout_ms must be between 1 and 300000"},
		{ConnInput{Name: "shop", Engine: "sqlite", DSN: db, PoolSize: 51}, true, "error[invalid]: pool_size must be between 1 and 50"},
		{ConnInput{Name: "gone", Engine: "sqlite", DSN: missing}, true, "error[connection_failed]: database file not found"},
	}
	for _, c := range cases {
		_, _, err := h.svc.CreateConnection(ctx, ws, c.in, c.test)
		if !strings.HasPrefix(errText(err), c.want) {
			t.Errorf("%+v: got %q, want prefix %q", c.in, errText(err), c.want)
		}
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "gone", Engine: "sqlite", DSN: missing}, false); err != nil {
		t.Fatalf("skip test should save: %v", err)
	}
	info, tr, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, true)
	if err != nil || tr == nil || !strings.HasPrefix(tr.ServerVersion, "SQLite ") {
		t.Fatalf("create = %+v %+v %v", info, tr, err)
	}
	if info.Mode != engine.ModeRO || info.MaxRows != 100 || info.TimeoutMS != 30000 || info.PoolSize != 5 || info.Version != 1 {
		t.Fatalf("defaults = %+v", info)
	}
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, true); errText(err) != `error[invalid]: a connection named "shop" already exists. Edit it, or choose another name` {
		t.Fatalf("duplicate: %q", errText(err))
	}
}

func TestUpdateAppliesOnNextCall(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := context.Background()
	if _, err := h.query(t, QueryInput{Query: "SELECT id FROM orders"}); err != nil {
		t.Fatal(err) // opens the pool for version 1
	}
	five := 5
	info, _, err := h.svc.UpdateConnection(ctx, ws, "shop", ConnPatch{MaxRows: &five}, true)
	if err != nil || info.Version != 2 {
		t.Fatalf("update = %+v, %v", info, err)
	}
	out, _ := h.query(t, QueryInput{Query: "SELECT id FROM orders ORDER BY id"})
	if !strings.Contains(out, "-- 5 rows shown") {
		t.Fatalf("new max_rows not applied: %q", out)
	}
	rw := engine.ModeRW
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "shop", ConnPatch{Mode: &rw}, true); err != nil {
		t.Fatal(err)
	}
	out, err = h.query(t, QueryInput{Query: "DELETE FROM order_items WHERE order_id = 1"})
	if err != nil || !strings.HasPrefix(out, "-- 2 rows affected") {
		t.Fatalf("write after switching to rw: %q, %v", out, err)
	}
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "nope", ConnPatch{}, true); !strings.HasPrefix(errText(err), "error[unknown_source]") {
		t.Fatalf("update missing: %v", err)
	}
}

func TestDeleteAndList(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRO)
	ctx := context.Background()
	list, err := h.svc.ListConnections(ctx, ws)
	if err != nil || len(list) != 1 || list[0].Name != "shop" || list[0].DSN == "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if tr, err := h.svc.TestConnection(ctx, ws, "shop"); err != nil || tr.ServerVersion == "" {
		t.Fatalf("test = %+v, %v", tr, err)
	}
	if err := h.svc.DeleteConnection(ctx, ws, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.query(t, QueryInput{Query: "SELECT 1"}); !strings.HasPrefix(errText(err), "error[unknown_source]") {
		t.Fatalf("query after delete: %v", err)
	}
	if err := h.svc.DeleteConnection(ctx, ws, "shop"); !strings.HasPrefix(errText(err), "error[unknown_source]") {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestAllowedNamespaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{
		Name: "shop", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), AllowedNamespaces: []string{"other", " "},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := h.svc.Schema(ctx, ws, SchemaInput{Source: "shop"}); out != "-- no tables or views found\n" {
		t.Fatalf("got %q", out)
	}
	info, _ := h.svc.GetConnection(ctx, ws, "shop")
	if len(info.AllowedNamespaces) != 1 || info.AllowedNamespaces[0] != "other" {
		t.Fatalf("namespaces = %v", info.AllowedNamespaces)
	}
}

func TestCreateConnectionInUnknownWorkspace(t *testing.T) {
	h := newHarness(t)
	_, _, err := h.svc.CreateConnection(context.Background(), "nosuch", ConnInput{Name: "x", Engine: "sqlite", DSN: testutil.SQLiteFixture(t)}, false)
	if !strings.HasPrefix(errText(err), `error[unknown_source]: no workspace named "nosuch"`) {
		t.Fatalf("got %q", errText(err))
	}
}

// resolve caches a connection's unsealed settings; an edit must replace them.
func TestQueryUsesEditedConnection(t *testing.T) {
	h := newHarness(t)
	h.addShop(t, engine.ModeRW)
	ctx := context.Background()
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "CREATE TABLE marker (x int)"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT x FROM marker"}); err != nil {
		t.Fatal(err)
	}
	other := testutil.SQLiteFixture(t)
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "shop", ConnPatch{DSN: &other}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "shop", Query: "SELECT x FROM marker"}); err == nil {
		t.Fatal("query still ran on the old database")
	}
}
