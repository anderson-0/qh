package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestConnFieldAndTLSFlags(t *testing.T) {
	home := t.TempDir()
	f := testutil.NewTLSFixture(t)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte(f.CA), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := run(t, home, "s3cret-pw\n", "conn", "add", "pg", "--engine", "postgres", "--host", "db.example.com",
		"--port", "5433", "--database", "app", "--user", "reader", "--password-stdin", "--param", "application_name=qh-cli",
		"--tls-mode", "verify-full", "--tls-ca", ca, "--skip-test")
	if code != 0 || out != "added pg (postgres, ro)\n" {
		t.Fatalf("add: %d %q %q", code, out, errOut)
	}
	out, _, _ = run(t, home, "", "conn", "show", "pg")
	if !strings.Contains(out, "dsn: postgres://reader:***@db.example.com:5433/app?application_name=qh-cli\n") ||
		!strings.Contains(out, "tls: verify-full (ca)\n") || strings.Contains(out, "s3cret-pw") {
		t.Fatalf("show: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "edit", "pg", "--no-tls", "--skip-test"); out != "updated pg (version 2)\n" {
		t.Fatalf("edit --no-tls: %q", out)
	}
	if out, _, _ = run(t, home, "", "conn", "show", "pg"); !strings.Contains(out, "tls: none") {
		t.Fatalf("show after --no-tls: %q", out)
	}
	if _, errOut, code := run(t, home, "", "conn", "edit", "pg", "--tls-ca", ca); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --tls-ca needs --tls-mode") {
		t.Fatalf("tls flag without mode: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "x\n", "conn", "add", "b", "--engine", "postgres", "--dsn", "-", "--password-stdin"); code != 1 ||
		!strings.HasPrefix(errOut, "error[invalid]: --dsn - and --password-stdin cannot both read stdin") {
		t.Fatalf("two stdin readers: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "conn", "add", "c", "--engine", "postgres", "--dsn", "postgres://h/db", "--host", "h"); code != 1 ||
		!strings.HasPrefix(errOut, "error[invalid]: give either dsn or host") {
		t.Fatalf("dsn and host: %d %q", code, errOut)
	}
}

func TestQueryLogCommand(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	run(t, home, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db)
	run(t, home, "", "query", "shop", "SELECT id\nFROM users")
	run(t, home, "", "query", "shop", "DELETE FROM users")
	out, errOut, code := run(t, home, "", "log")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[0], "AT") || !strings.Contains(lines[1], "read_only") ||
		!strings.HasSuffix(lines[2], "SELECT id FROM users") {
		t.Fatalf("log: %d %q %q", code, out, errOut)
	}
	if out, _, _ := run(t, home, "", "log", "--limit", "1"); strings.Count(out, "\n") != 2 {
		t.Fatalf("limit: %q", out)
	}
	if out, _, _ := run(t, home, "", "log", "--connection", "crm"); out != "no logged queries\n" {
		t.Fatalf("filter: %q", out)
	}
	if _, errOut, code := run(t, home, "", "log", "--since", "yesterday"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: since must be") {
		t.Fatalf("since: %d %q", code, errOut)
	}
}

func TestQueryLogFlags(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"mcp", "--query-log-retention", "0"}, {"serve", "--query-log-retention", "soon"}} {
		if _, errOut, code := run(t, home, "", args...); code != 1 ||
			!strings.HasPrefix(errOut, "error[invalid]: --query-log-retention must be a positive duration such as 30d or 72h") {
			t.Fatalf("%v: %d %q", args, code, errOut)
		}
	}
}

func TestWorkspaceFlag(t *testing.T) {
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	run(t, home, "", "workspace", "create", "team")
	if out, errOut, code := run(t, home, "", "--workspace", "team", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db); code != 0 {
		t.Fatalf("add in team: %q %q", out, errOut)
	}
	if out, _, _ := run(t, home, "", "conn", "ls"); !strings.HasPrefix(out, "no connections") {
		t.Fatalf("default workspace: %q", out)
	}
	if out, _, _ := run(t, home, "", "--workspace", "team", "conn", "ls"); !strings.Contains(out, "shop") {
		t.Fatalf("team: %q", out)
	}
	t.Setenv("QH_WORKSPACE", "team")
	if out, _, _ := run(t, home, "", "query", "shop", "SELECT 1 AS one"); !strings.HasPrefix(out, "one\n1\n") {
		t.Fatalf("QH_WORKSPACE: %q", out)
	}
}
