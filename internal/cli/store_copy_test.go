package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestStoreCopyMovesEverything(t *testing.T) {
	dsn := pgStoreDSN(t)
	home := t.TempDir()
	t.Setenv("QH_MASTER_KEY", testMasterKey(t)) // the same key on both sides
	db := testutil.SQLiteFixture(t)
	for _, args := range [][]string{
		{"workspace", "create", "team"},
		{"conn", "add", "shop", "--engine", "sqlite", "--dsn", db, "--description", "Shop"},
		{"--workspace", "team", "key", "create", "--scopes", "query", "--name", "agent"},
		{"query", "shop", "SELECT 1"}, // one query-log entry
	} {
		if _, errOut, code := run(t, home, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	out, errOut, code := run(t, home, "", "store", "copy", "--to", dsn, "--with-query-log")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if !strings.Contains(out, "copied 2 workspaces, 1 connections, 1 keys, 1 query-log entries") {
		t.Fatalf("summary: %q", out)
	}
	assertStoresMatch(t, home, dsn)
	// The copied store works with the same master key.
	t.Setenv("QH_STORE_DSN", dsn)
	if out, errOut, code := run(t, t.TempDir(), "", "query", "shop", "SELECT count(*) AS n FROM orders"); code != 0 || !strings.Contains(out, "250") {
		t.Fatalf("query on the copy: %d %q %q", code, out, errOut)
	}
	// Copying again into the now non-empty store is refused.
	t.Setenv("QH_STORE_DSN", "")
	if _, errOut, code := run(t, home, "", "store", "copy", "--to", dsn); code == 0 || !strings.Contains(errOut, "is not empty") {
		t.Fatalf("second copy: %d %q", code, errOut)
	}
}

// assertStoresMatch compares the SQLite store in home with the Postgres store
// at dsn row by row.
func assertStoresMatch(t *testing.T, home, dsn string) {
	t.Helper()
	ctx := context.Background()
	src, err := store.Open(ctx, "", home)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := store.Open(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	sw, _ := src.ListWorkspaces(ctx)
	dw, _ := dst.ListWorkspaces(ctx)
	if len(sw) != 2 || len(dw) != 2 {
		t.Fatalf("workspaces: %d in the source, %d in the copy", len(sw), len(dw))
	}
	for i := range sw {
		if sw[i].ID != dw[i].ID || sw[i].CreatedAt.Sub(dw[i].CreatedAt).Abs() > time.Microsecond {
			t.Fatalf("workspace %d: %+v vs %+v", i, sw[i], dw[i])
		}
	}
	sc, err := src.GetConnection(ctx, store.DefaultWorkspace, "shop")
	if err != nil {
		t.Fatal(err)
	}
	dc, err := dst.GetConnection(ctx, store.DefaultWorkspace, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if dc.ID != sc.ID || dc.Version != sc.Version || !bytes.Equal(dc.Secret, sc.Secret) || dc.SecretKeyID != sc.SecretKeyID ||
		dc.Description != sc.Description || sc.CreatedAt.Sub(dc.CreatedAt).Abs() > time.Microsecond {
		t.Fatalf("connection: %+v vs %+v", sc, dc)
	}
	sk, _ := src.ListAllKeys(ctx)
	dk, _ := dst.ListAllKeys(ctx)
	if len(sk) != 1 || len(dk) != 1 || dk[0].ID != sk[0].ID || dk[0].Prefix != sk[0].Prefix || dk[0].WorkspaceID != "team" ||
		!bytes.Equal(dk[0].Hash, sk[0].Hash) || !reflect.DeepEqual(dk[0].Scopes, sk[0].Scopes) {
		t.Fatalf("keys: %+v vs %+v", sk, dk)
	}
	sl, _ := src.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace})
	dl, _ := dst.ListQueryLog(ctx, store.QueryLogFilter{WorkspaceID: store.DefaultWorkspace})
	if len(sl) != 1 || len(dl) != 1 || dl[0].Query != "SELECT 1" || dl[0].Query != sl[0].Query || dl[0].ConnectionName != "shop" {
		t.Fatalf("query log: %+v vs %+v", sl, dl)
	}
}

func TestStoreCopyErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QH_MASTER_KEY", testMasterKey(t))
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"store", "copy"}, "--to is required"},
		{[]string{"store", "copy", "--to", "sqlite:///x.db"}, "--to must be a postgres:// URL"},
	} {
		_, errOut, code := run(t, home, "", c.args...)
		if code == 0 || !strings.Contains(errOut, c.want) {
			t.Fatalf("%v: %d %q", c.args, code, errOut)
		}
	}
}
