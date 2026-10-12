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

const testHostKey = "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"

func showOut(t *testing.T, home, name string) string {
	t.Helper()
	out, errOut, code := run(t, home, "", "conn", "show", name)
	if code != 0 {
		t.Fatalf("show: %d %s", code, errOut)
	}
	return out
}

func addSSH(t *testing.T, home, stdin string, extra ...string) {
	t.Helper()
	a := append([]string{"conn", "add", "pg", "--engine", "postgres", "--dsn", "postgres://u@db/app", "--skip-test", "--ssh-host-key", testHostKey}, extra...)
	if _, errOut, code := run(t, home, stdin, a...); code != 0 {
		t.Fatalf("add %v: %d %s", extra, code, errOut)
	}
}

func TestConnAddSSHSecretsFromStdin(t *testing.T) {
	home := t.TempDir()
	addSSH(t, home, "s3cret\n", "--ssh-host", "jump@bastion", "--ssh-password-stdin")
	out := showOut(t, home, "pg")
	if !strings.Contains(out, "ssh: jump@bastion:22 · host key "+testHostKey+" · password") || strings.Contains(out, "s3cret") {
		t.Fatalf("password stdin:\n%s", out)
	}

	// A passphrase is read from stdin the same way; the key makes the auth "key".
	key, _ := sshtest.Key(t, "pp")
	keyFile := filepath.Join(t.TempDir(), "id")
	os.WriteFile(keyFile, []byte(key), 0o600)
	home = t.TempDir()
	addSSH(t, home, "pp\n", "--ssh-host", "jump@bastion", "--ssh-key", keyFile, "--ssh-passphrase-stdin")
	out = showOut(t, home, "pg")
	if !strings.Contains(out, "· key") || strings.Contains(out, "passphrase") {
		t.Fatalf("passphrase stdin:\n%s", out)
	}
	// Without the passphrase the encrypted key is refused, so the stdin line really reached the SSH block.
	a := []string{"conn", "add", "pg2", "--engine", "postgres", "--dsn", "postgres://u@db/app", "--skip-test",
		"--ssh-host-key", testHostKey, "--ssh-host", "jump@bastion", "--ssh-key", keyFile}
	if _, _, code := run(t, home, "", a...); code == 0 {
		t.Fatal("encrypted key without passphrase was accepted; the stdin test proves nothing")
	}
}

func TestConnAddSSHHostParsing(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"explicit user wins over user@": {[]string{"--ssh-host", "alice@b:2200", "--ssh-user", "bob"}, "ssh: bob@b:2200 · "},
		"user@ used without --ssh-user": {[]string{"--ssh-host", "alice@b"}, "ssh: alice@b:22 · "},
		"user containing @":             {[]string{"--ssh-host", "a@corp@b:2200"}, "ssh: a@corp@b:2200 · "},
		"ipv6 with port":                {[]string{"--ssh-host", "jump@[::1]:2222"}, "ssh: jump@[::1]:2222 · "},
		"bare ipv6":                     {[]string{"--ssh-host", "[::1]", "--ssh-user", "jump"}, "ssh: jump@[::1]:22 · "},
	} {
		home := t.TempDir()
		addSSH(t, home, "", append(tc.args, "--ssh-password-ref", "env:SSHPW")...)
		if out := showOut(t, home, "pg"); !strings.Contains(out, tc.want) {
			t.Errorf("%s: show lacks %q:\n%s", name, tc.want, out)
		}
	}
}

func TestConnEditSSHAndRefs(t *testing.T) {
	home := t.TempDir()
	key, _ := sshtest.Key(t, "")
	keyFile := filepath.Join(t.TempDir(), "id")
	os.WriteFile(keyFile, []byte(key), 0o600)
	addSSH(t, home, "", "--ssh-host", "jump@bastion", "--ssh-key", keyFile, "--password-ref", "env:DBPW")
	if out := showOut(t, home, "pg"); !strings.Contains(out, "· key") || !strings.Contains(out, "password: from env:DBPW") {
		t.Fatalf("before edit:\n%s", out)
	}

	// --ssh-host on edit replaces the whole block: the key is gone, the password is in.
	if _, errOut, code := run(t, home, "pw\n", "conn", "edit", "pg", "--ssh-host", "other@bastion2:2022",
		"--ssh-host-key", testHostKey, "--ssh-password-stdin", "--skip-test"); code != 0 {
		t.Fatalf("edit: %d %s", code, errOut)
	}
	out := showOut(t, home, "pg")
	if !strings.Contains(out, "ssh: other@bastion2:2022 · host key "+testHostKey+" · password") || strings.Contains(out, "· key") {
		t.Fatalf("after edit:\n%s", out)
	}

	// --no-ssh with --ssh-host is refused by core.
	_, errOut, code := run(t, home, "", "conn", "edit", "pg", "--no-ssh", "--ssh-host", "b", "--skip-test")
	if code == 0 || !strings.Contains(errOut, "give ssh or clear it, not both") {
		t.Fatalf("no-ssh + ssh-host: %d %s", code, errOut)
	}

	// An empty --password-ref removes the reference.
	if _, errOut, code := run(t, home, "", "conn", "edit", "pg", "--password-ref", "", "--skip-test"); code != 0 {
		t.Fatalf("clear ref: %d %s", code, errOut)
	}
	if out := showOut(t, home, "pg"); !strings.Contains(out, "password: none, or in the connection string") {
		t.Fatalf("ref not cleared:\n%s", out)
	}
}

func TestServeDenySSHNeedsServerMode(t *testing.T) {
	_, errOut, code := run(t, t.TempDir(), "", "serve", "--deny-ssh")
	if code != 1 || !strings.Contains(errOut, "apply to server mode only") {
		t.Fatalf("%d %s", code, errOut)
	}
}
