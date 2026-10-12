package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func run(t *testing.T, home, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Execute("test", append([]string{"--home", home}, args...), strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestConnLifecycle(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	out, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db, "--description", "Shop DB")
	if code != 0 || !strings.HasPrefix(out, "added shop (sqlite, ro) · SQLite ") {
		t.Fatalf("add: %d %q %q", code, out, errOut)
	}
	if out, _, _ = run(t, home, "", "conn", "ls"); !strings.Contains(out, "shop") || !strings.Contains(out, "Shop DB") || !strings.HasPrefix(out, "NAME") {
		t.Fatalf("ls: %q", out)
	}
	if out, _, _ = run(t, home, "", "query", "shop", "SELECT id FROM users ORDER BY id"); !strings.HasPrefix(out, "id\n1\n2\n3\n-- 3 rows · ") {
		t.Fatalf("query: %q", out)
	}
	if _, errOut, code = run(t, home, "", "query", "shop", "DELETE FROM users"); code != 1 || !strings.HasPrefix(errOut, "error[read_only]") {
		t.Fatalf("write on ro: %d %q", code, errOut)
	}
	if out, _, _ = run(t, home, "", "schema", "shop", "users"); !strings.Contains(out, "main.users") {
		t.Fatalf("schema: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "edit", "shop", "--mode", "rw"); out != "updated shop (version 2)\n" {
		t.Fatalf("edit: %q", out)
	}
	if out, _, _ = run(t, home, "", "query", "shop", "DELETE FROM order_items WHERE order_id = 1"); !strings.HasPrefix(out, "-- 2 rows affected") {
		t.Fatalf("write on rw: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "show", "shop"); !strings.Contains(out, "mode: rw\n") || !strings.Contains(out, "version: 2\n") {
		t.Fatalf("show: %q", out)
	} else if strings.Contains(out, "password:") {
		t.Fatalf("show prints a password line for SQLite: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "test", "shop"); !strings.HasPrefix(out, "ok · SQLite ") {
		t.Fatalf("test: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "rm", "shop"); out != "removed shop\n" {
		t.Fatalf("rm: %q", out)
	}
	if _, errOut, code = run(t, home, "", "query", "shop", "SELECT 1"); code != 1 || !strings.HasPrefix(errOut, "error[unknown_source]") {
		t.Fatalf("after rm: %d %q", code, errOut)
	}
}

func TestDSNFromStdinAndKeyFile(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	if out, errOut, code := run(t, home, db+"\n", "conn", "add", "shop", "--engine", "sqlite", "--dsn", "-"); code != 0 {
		t.Fatalf("add from stdin: %q %q", out, errOut)
	}
	info, err := os.Stat(filepath.Join(home, "master.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("master.key: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, "qh.db")); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFlags(t *testing.T) {
	home := t.TempDir()
	if _, errOut, code := run(t, home, "", "conn", "add", "shop", "--dsn", "x"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --engine is required") {
		t.Fatalf("%d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "sqlite"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --dsn is required") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestMySQLEngineIsRegistered(t *testing.T) {
	home := t.TempDir()
	_, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "mysql",
		"--dsn", "mysql://u:hunter22@127.0.0.1:1/db?timeout=1s")
	if code != 1 || !strings.HasPrefix(errOut, "error[connection_failed]") || strings.Contains(errOut, "hunter22") {
		t.Fatalf("%d %q", code, errOut)
	}
	out, errOut, code := run(t, home, "", "conn", "add", "maria", "--engine", "mariadb",
		"--dsn", "mariadb://u:hunter22@127.0.0.1:1/db", "--skip-test")
	if code != 0 || !strings.HasPrefix(out, "added maria (mariadb, ro)") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if out, _, _ = run(t, home, "", "conn", "show", "maria"); !strings.Contains(out, "dsn: mariadb://u:***@127.0.0.1:1/db") {
		t.Fatalf("show: %q", out)
	}
}
