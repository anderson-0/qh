package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/auth"
)

func TestWorkspaceAndKeyCommands(t *testing.T) {
	home := t.TempDir()
	if out, errOut, code := run(t, home, "", "workspace", "create", "team"); code != 0 || out != "created workspace team\n" {
		t.Fatalf("create: %d %q %q", code, out, errOut)
	}
	if out, _, _ := run(t, home, "", "workspace", "ls"); !strings.Contains(out, "default") || !strings.Contains(out, "team") {
		t.Fatalf("ls: %q", out)
	}
	out, errOut, code := run(t, home, "", "key", "create", "--workspace", "team", "--scopes", "query,manage", "--name", "ci", "--expires", "30d")
	if code != 0 || !regexp.MustCompile(`(?m)^qh_[a-z0-9]{8}_[0-9A-Za-z]+$`).MatchString(out) || !strings.Contains(out, "will not be shown again") {
		t.Fatalf("key create: %d %q %q", code, out, errOut)
	}
	if out, _, code = run(t, home, "", "key", "create", "--scopes", "admin"); code != 0 || !strings.Contains(out, "admin key") {
		t.Fatalf("admin key: %d %q", code, out)
	}
	out, _, _ = run(t, home, "", "key", "ls")
	if !strings.HasPrefix(out, "ID") || !strings.Contains(strings.Split(out, "\n")[0], "CREATED BY") || strings.Count(out, "\n") != 3 || !strings.Contains(out, "query,manage") || strings.Contains(out, "qh_") {
		t.Fatalf("key ls: %q", out)
	}
	id := strings.Fields(strings.Split(out, "\n")[1])[0]
	if out, _, code := run(t, home, "", "key", "rm", id); code != 0 || out != "revoked key "+id+"\n" {
		t.Fatalf("key rm: %d %q", code, out)
	}
	if _, errOut, code := run(t, home, "", "key", "create", "--workspace", "team", "--scopes", "root"); code != 1 || !strings.HasPrefix(errOut, `error[invalid]: unknown scope "root"`) {
		t.Fatalf("bad scope: %d %q", code, errOut)
	}
	if out, _, code := run(t, home, "", "workspace", "rm", "team"); code != 0 || out != "deleted workspace team\n" {
		t.Fatalf("rm: %d %q", code, out)
	}
}

var createdKeyID = regexp.MustCompile(`(?m)^created key (\S+) `)

// key rm prints its success line on stdout and the fixed notice on stderr;
// a failed revocation prints neither.
func TestKeyRevokeWarning(t *testing.T) {
	home := t.TempDir()
	create := func(args ...string) string {
		t.Helper()
		out, errOut, code := run(t, home, "", append([]string{"key", "create"}, args...)...)
		m := createdKeyID.FindStringSubmatch(out)
		if code != 0 || m == nil {
			t.Fatalf("key create: %d %q", code, errOut) // out holds the key; do not print it
		}
		return m[1]
	}
	for _, verb := range []string{"rm", "revoke"} {
		id := create("--scopes", "query,manage")
		out, errOut, code := run(t, home, "", "key", verb, id)
		if code != 0 || out != "revoked key "+id+"\n" || errOut != auth.RevocationWarning+"\n" {
			t.Fatalf("key %s: %d %q %q", verb, code, out, errOut)
		}
		out, errOut, code = run(t, home, "", "key", verb, id)
		if code != 1 || out != "" || !strings.HasPrefix(errOut, `error[unknown_source]: no API key with id "`+id+`"`) ||
			strings.Contains(errOut, auth.RevocationWarning) {
			t.Fatalf("key %s twice: %d %q %q", verb, code, out, errOut)
		}
	}
	if out, errOut, code := run(t, home, "", "key", "rm"); code != 1 || out != "" || strings.Contains(errOut, auth.RevocationWarning) {
		t.Fatalf("key rm without id: %d %q %q", code, out, errOut)
	}
}

func TestServeFlagValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QH_MASTER_KEY", "")
	if _, errOut, code := run(t, home, "", "serve", "--http", "127.0.0.1:0"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: server mode needs QH_MASTER_KEY") {
		t.Fatalf("missing master key: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "serve", "--sqlite-root", home); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --allow-cidr, --deny-cidr, --sqlite-root, --allow-secret-ref and --deny-ssh apply to server mode only") {
		t.Fatalf("server-only flags in local mode: %d %q", code, errOut)
	}
}

func TestServeClearsPGEnvironment(t *testing.T) {
	// serve runs in-process and unsets every PG* variable; put the test
	// process's own back afterwards.
	for _, kv := range os.Environ() {
		if name, value, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "PG") {
			t.Cleanup(func() { os.Setenv(name, value) })
		}
	}
	t.Setenv("QH_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("PGPASSWORD", "operator-secret")
	t.Setenv("PGSERVICE", "operator-service")
	// The invalid port stops serve right after startup.
	_, errOut, code := run(t, t.TempDir(), "", "serve", "--http", "127.0.0.1:99999")
	if code != 1 || !regexp.MustCompile(`(?m)^ignoring (PG[A-Z_]+, )*PGPASSWORD, (PG[A-Z_]+, )*PGSERVICE(, PG[A-Z_]+)* in server mode$`).MatchString(errOut) ||
		strings.Contains(errOut, "operator-secret") || strings.Contains(errOut, "operator-service") {
		t.Fatalf("%d %q", code, errOut)
	}
	if os.Getenv("PGPASSWORD") != "" || os.Getenv("PGSERVICE") != "" {
		t.Fatal("PG* variables still set")
	}
}
