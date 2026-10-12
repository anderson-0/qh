package cli

import (
	"strings"
	"testing"

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
