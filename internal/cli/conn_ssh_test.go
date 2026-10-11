package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func TestConnAddWithRefsAndSSHSkipTest(t *testing.T) {
	home := t.TempDir()
	key, _ := sshtest.Key(t, "")
	keyFile := filepath.Join(t.TempDir(), "id")
	os.WriteFile(keyFile, []byte(key), 0o600)
	fp := "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"
	out, errOut, code := run(t, home, "", "conn", "add", "pg", "--engine", "postgres", "--dsn-ref", "env:QH_PG_DSN",
		"--password-ref", "file:/run/secrets/pw", "--ssh-host", "jump@bastion.example.com:2222", "--ssh-host-key", fp,
		"--ssh-key", keyFile, "--skip-test")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	out, errOut, code = run(t, home, "", "conn", "show", "pg")
	for _, want := range []string{
		"dsn: (from env:QH_PG_DSN)",
		"password: from file:/run/secrets/pw",
		"ssh: jump@bastion.example.com:2222 · host key " + fp + " · key",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "BEGIN OPENSSH") {
		t.Fatal("key printed")
	}
	_, _, code = run(t, home, "", "conn", "edit", "pg", "--no-ssh", "--skip-test")
	out, _, _ = run(t, home, "", "conn", "show", "pg")
	if code != 0 || !strings.Contains(out, "ssh: none") {
		t.Fatalf("no-ssh: %d\n%s", code, out)
	}
}

func TestConnAddSSHFlagErrors(t *testing.T) {
	home := t.TempDir()
	for want, args := range map[string][]string{
		"--ssh-user needs --ssh-host":  {"--ssh-user", "u"},
		"only one flag can read stdin": {"--password-stdin", "--ssh-password-stdin", "--ssh-host", "b"},
		"invalid --ssh-host port":      {"--ssh-host", "b:notaport"},
	} {
		a := append([]string{"conn", "add", "x", "--engine", "postgres", "--dsn", "postgres://u@db/app", "--skip-test"}, args...)
		_, errOut, code := run(t, home, "", a...)
		if code == 0 || !strings.Contains(errOut, want) {
			t.Errorf("%q: %d %s", want, code, errOut)
		}
	}
}

func TestServeRefFlagsNeedServerMode(t *testing.T) {
	_, errOut, code := run(t, t.TempDir(), "", "serve", "--allow-secret-ref", "env:X_*")
	if code == 0 || !strings.Contains(errOut, "apply to server mode only") {
		t.Fatalf("%d %s", code, errOut)
	}
	// serve unsets PG* variables in server mode; put the test process's own back.
	for _, kv := range os.Environ() {
		if name, value, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "PG") {
			t.Cleanup(func() { os.Setenv(name, value) })
		}
	}
	t.Setenv("QH_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	_, errOut, code = run(t, t.TempDir(), "", "serve", "--http", "127.0.0.1:0", "--allow-secret-ref", "QH_*")
	if code == 0 || !strings.Contains(errOut, "invalid --allow-secret-ref pattern") {
		t.Fatalf("%d %s", code, errOut)
	}
}
