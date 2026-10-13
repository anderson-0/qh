package tunnel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

const serverRounds = 128 // what core sets in server mode

func wantKeyErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %q, got success", want)
	}
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
	if strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("key material in error: %v", err)
	}
}

func TestOversizeKeyRefused(t *testing.T) {
	key, _ := sshtest.Key(t, "")
	big := key + strings.Repeat("\n", 16<<10)
	for _, rounds := range []int{0, serverRounds} {
		_, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: big, MaxKDFRounds: rounds})
		wantKeyErr(t, err, "ssh: the private key is larger than 16 KiB")
	}
}

// A tenant's key whose KDF asks for a huge round count is refused before
// x/crypto starts on it.
func TestKDFRoundsCappedInServerMode(t *testing.T) {
	// x/crypto itself refuses more than 2048 rounds at once, so 2048 (which
	// it would run, for seconds) is what shows qh's own cap at work.
	for _, rounds := range []uint32{2048, 1_000_000} {
		for _, pp := range []string{"", "pp"} {
			start := time.Now()
			_, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: sshtest.KeyWithRounds(rounds), Passphrase: pp, MaxKDFRounds: serverRounds})
			if d := time.Since(start); d > 500*time.Millisecond {
				t.Fatalf("%d rounds: refusing took %v", rounds, d)
			}
			wantKeyErr(t, err, fmt.Sprintf("ssh: the private key's KDF uses %d rounds; this server accepts at most 128 (re-encrypt it with ssh-keygen -p -a 100)", rounds))
		}
	}
	huge := sshtest.KeyWithRounds(1_000_000)
	// Without a cap (local mode) qh does not refuse it for its round count;
	// x/crypto then fails on it as it would on any unreadable key.
	_, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: huge, Passphrase: "pp"})
	if err == nil || strings.Contains(err.Error(), "this server accepts") {
		t.Fatalf("local mode: %v", err)
	}
}

// A key encrypted with a normal round count (x/crypto writes 16) works in
// both modes.
func TestEncryptedKeyWithNormalRoundsWorks(t *testing.T) {
	target := echo(t)
	enc, pub := sshtest.Key(t, "pp")
	srv := sshtest.Start(t, sshtest.Options{User: "u", AuthorizedKey: pub})
	for _, rounds := range []int{0, serverRounds} {
		tun, err := New(Config{Addr: srv.Addr, User: "u", HostKey: srv.Fingerprint, PrivateKey: enc, Passphrase: "pp", MaxKDFRounds: rounds})
		if err != nil {
			t.Fatalf("cap %d: %v", rounds, err)
		}
		c, err := tun.Dial(context.Background(), "tcp", target)
		if err != nil {
			t.Fatalf("cap %d: %v", rounds, err)
		}
		roundTrip(t, c)
		tun.Close()
	}
}

// A wrong passphrase is named as such, in both modes. (In server mode it is
// first found here: input validation does not decrypt.)
func TestWrongPassphrase(t *testing.T) {
	enc, _ := sshtest.Key(t, "pp")
	for _, rounds := range []int{0, serverRounds} {
		_, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: enc, Passphrase: "wrong", MaxKDFRounds: rounds})
		wantKeyErr(t, err, "ssh: the passphrase does not decrypt the private key")
		if strings.Contains(err.Error(), "wrong") {
			t.Fatalf("passphrase leaked: %v", err)
		}
	}
}

// In server mode an RSA key larger than 8192 bits is refused once parsed:
// x/crypto caps OpenSSH-format RSA keys there but not PKCS#1 or PKCS#8 ones.
func TestRSAKeySizeCappedInServerMode(t *testing.T) {
	_, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: sshtest.RSA8704, MaxKDFRounds: serverRounds, MaxRSABits: 8192})
	wantKeyErr(t, err, "ssh: the private key is an 8704-bit RSA key; this server accepts at most 8192 bits")
	tun, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: sshtest.RSA8704})
	if err != nil {
		t.Fatalf("local mode: %v", err)
	}
	tun.Close()
}

// DSA keys are refused in server mode: x/crypto does not check a DSA
// private key's parameters, so a huge one would be slow to sign with.
// Local mode still accepts them.
func TestDSAKeyRefusedInServerMode(t *testing.T) {
	key := sshtest.DSAKey(t)
	_, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: key, MaxKDFRounds: serverRounds, MaxRSABits: 8192, RefuseDSA: true})
	wantKeyErr(t, err, "ssh: DSA keys are not accepted; use an Ed25519, ECDSA or RSA key")
	tun, err := New(Config{Addr: "127.0.0.1:22", User: "u", PrivateKey: key})
	if err != nil {
		t.Fatalf("local mode: %v", err)
	}
	tun.Close()
}

// Keys written by OpenSSH's ssh-keygen: one within the cap works in server
// mode; one above it is refused there, and only there. (Decrypting it in
// local mode is slow under the race detector, so that case stops at the
// missing passphrase, which x/crypto reports after qh's check.)
func TestSSHKeygenRounds(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	keygen := func(rounds string) (string, ssh.PublicKey) {
		path := filepath.Join(dir, "id_"+rounds)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-a", rounds, "-N", "pp", "-C", "", "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v: %s", err, out)
		}
		priv, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path + ".pub")
		if err != nil {
			t.Fatal(err)
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey(b)
		if err != nil {
			t.Fatal(err)
		}
		return string(priv), pub
	}
	k16, pub16 := keygen("16")
	srv := sshtest.Start(t, sshtest.Options{User: "u", AuthorizedKey: pub16})
	tun, err := New(Config{Addr: srv.Addr, User: "u", HostKey: srv.Fingerprint, PrivateKey: k16, Passphrase: "pp", MaxKDFRounds: serverRounds})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tun.Dial(context.Background(), "tcp", echo(t))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, conn)
	tun.Close()

	k129, _ := keygen("129")
	_, err = New(Config{Addr: srv.Addr, User: "u", PrivateKey: k129, Passphrase: "pp", MaxKDFRounds: serverRounds})
	wantKeyErr(t, err, "ssh: the private key's KDF uses 129 rounds; this server accepts at most 128")
	_, err = New(Config{Addr: srv.Addr, User: "u", PrivateKey: k129})
	wantKeyErr(t, err, "ssh: the private key is encrypted; give a passphrase")
}

// The unknown-host-key hint names the type the bastion presented, as
// ssh-keyscan spells it. RSA host keys work through rsa-sha2-*.
func TestUnknownHostKeyHintNamesPresentedType(t *testing.T) {
	for _, c := range []struct{ hostKey, typ, scan string }{
		{"", "ssh-ed25519", "ed25519"},
		{"ecdsa", "ecdsa-sha2-nistp256", "ecdsa"},
		{"rsa", "ssh-rsa", "rsa"},
	} {
		srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw", HostKey: c.hostKey})
		tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw"})
		err := tun.Connect(context.Background())
		tun.Close()
		_, port, _ := strings.Cut(srv.Addr, ":")
		e := qerr.From(err)
		if !strings.Contains(e.Message, "it presented its "+c.typ+" key "+srv.Fingerprint) ||
			!strings.Contains(e.Hint, "ssh-keyscan -t "+c.scan+" -p "+port+" 127.0.0.1 | ssh-keygen -lf -") {
			t.Errorf("%s: %v", c.typ, err)
		}
		tun, _ = New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
		if err := tun.Connect(context.Background()); err != nil {
			t.Errorf("%s pinned: %v", c.typ, err)
		}
		tun.Close()
	}
}

func TestKeyscanType(t *testing.T) {
	for in, want := range map[string]string{
		"ssh-ed25519":                "ed25519",
		"ecdsa-sha2-nistp256":        "ecdsa",
		"ecdsa-sha2-nistp384":        "ecdsa",
		"ecdsa-sha2-nistp521":        "ecdsa",
		"ssh-rsa":                    "rsa",
		"rsa-sha2-256":               "rsa",
		"rsa-sha2-512":               "rsa",
		"ssh-dss":                    "",
		"sk-ssh-ed25519@openssh.com": "",
		"":                           "",
	} {
		if got := keyscanType(in); got != want {
			t.Errorf("keyscanType(%q) = %q, want %q", in, got, want)
		}
	}
	// An unknown type leaves -t out of the hint.
	tun, _ := New(Config{Addr: "bastion.test:2222", User: "u", Password: "pw"})
	e := qerr.From(tun.handshakeErr(&hostKeyError{typ: "ssh-dss", got: "SHA256:x"}))
	if e.Hint != "Verify it (ssh-keyscan -p 2222 bastion.test | ssh-keygen -lf -), then set it with --ssh-host-key SHA256:x" {
		t.Errorf("hint: %q", e.Hint)
	}
}
