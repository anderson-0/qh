package cli

import (
	"regexp"
	"strings"
	"testing"
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
	if !strings.HasPrefix(out, "ID") || strings.Count(out, "\n") != 3 || !strings.Contains(out, "query,manage") || strings.Contains(out, "qh_") {
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

func TestServeFlagValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QH_MASTER_KEY", "")
	if _, errOut, code := run(t, home, "", "serve", "--http", "127.0.0.1:0"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: server mode needs QH_MASTER_KEY") {
		t.Fatalf("missing master key: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "serve", "--sqlite-root", home); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --allow-cidr, --deny-cidr and --sqlite-root apply to server mode only") {
		t.Fatalf("server-only flags in local mode: %d %q", code, errOut)
	}
}
