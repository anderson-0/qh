package core

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secretref"
	"github.com/anderson-0/qh/internal/sqlx"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenConnUnknownHostKeyNamesFingerprint(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	h := newHarness(t)
	_, err := h.svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@db.internal:5432/app?sslmode=disable",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "u", Password: "pw"}})
	if err == nil || !strings.Contains(err.Error(), "unknown host key") || !strings.Contains(err.Error(), "--ssh-host-key "+srv.Fingerprint) {
		t.Fatalf("%v", err)
	}
}

func TestOpenConnResolvesRefsAndScrubsThem(t *testing.T) {
	h := newHarness(t)
	t.Setenv("QH_TEST_DSN", "postgres://u:resolved-dsn-pw@127.0.0.1:1/app?sslmode=disable&connect_timeout=2")
	t.Setenv("QH_TEST_PW", "resolved-pw-value")
	_, err := h.svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSNRef: "env:QH_TEST_DSN", PasswordRef: "env:QH_TEST_PW"})
	if err == nil {
		t.Fatal("connected to port 1")
	}
	if s := err.Error(); strings.Contains(s, "resolved-dsn-pw") || strings.Contains(s, "resolved-pw-value") {
		t.Fatalf("resolved secret in error: %v", err)
	}
	_, err = h.svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSNRef: "env:QH_TEST_UNSET"})
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "env:QH_TEST_UNSET: not set") {
		t.Fatalf("unset: %v", err)
	}
}

// TestConnection tests a stored connection with all its settings, including
// its tunnel.
func TestTestConnectionUsesTunnel(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	h := newHarness(t)
	ctx := context.Background()
	_, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u:dbpw@db.internal:5432/app?sslmode=disable",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "u", Password: "pw", HostKey: fp}}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.TestConnection(ctx, ws, "pg")
	if err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("%v", err)
	}
}

// In server mode a resolved dsn_ref must pass the engine's server-mode
// connection-string limits before anything is dialled.
func TestServerModeChecksResolvedDSN(t *testing.T) {
	svc := newServiceWith(t, openStore(t), Options{Dial: dialNowhere, SecretRefs: []string{"env:QH_TEST_*"}})
	t.Setenv("QH_TEST_FILE_DSN", "postgres://u@db.example:5432/app?passfile=/etc/passwd")
	_, err := svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSNRef: "env:QH_TEST_FILE_DSN"})
	if err == nil || !strings.Contains(err.Error(), "only accepts these Postgres connection settings") || strings.Contains(err.Error(), "no network in tests") {
		t.Fatalf("%v", err)
	}
}

// A stored reference is checked again against the allow-list of the server
// that uses it.
func TestResolveRechecksStoredRefs(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	wide := newServiceWith(t, st, Options{Dial: dialNowhere, SecretRefs: []string{"env:QH_TEST_*"}})
	if _, _, err := wide.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSNRef: "env:QH_TEST_WIDE"}, false); err != nil {
		t.Fatal(err)
	}
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	narrow := New(st, wide.keys, pools, catalog.New(10*time.Minute), Options{Dial: dialNowhere, SecretRefs: []string{"env:QH_OTHER_*"}})
	t.Cleanup(narrow.Close)
	if _, err := narrow.Query(ctx, ws, QueryInput{Source: "pg", Query: "SELECT 1"}); qerr.From(err).Category != qerr.Forbidden {
		t.Fatalf("query: %v", err)
	}
	if _, err := narrow.TestConnection(ctx, ws, "pg"); qerr.From(err).Category != qerr.Forbidden {
		t.Fatalf("test: %v", err)
	}
}

// A bastion refused by the server's network policy is a connection
// failure worded like a refused database.
func TestServerModeBastionDeniedByPolicy(t *testing.T) {
	policy, err := netguard.New(nil, nil) // loopback is denied by default
	if err != nil {
		t.Fatal(err)
	}
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	svc := newServiceWith(t, openStore(t), Options{Dial: policy.DialContext, CheckTarget: policy.Allowed})
	_, err = svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "u", Password: "pw", HostKey: srv.Fingerprint}})
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "not allowed by this server's network policy") ||
		!strings.Contains(err.Error(), "bastion") {
		t.Fatalf("%v", err)
	}
}

// A bastion that accepts TCP but never speaks SSH fails as a connection
// failure naming the stage when the caller's context ends.
func TestSilentBastionTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = h.svc.TestInput(ctx, ws, ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@db.internal:5432/app",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, ln.Addr().String()), User: "u", Password: "secret-ssh-pw", HostKey: fp}})
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "ssh: timed out reaching the bastion 127.0.0.1:") {
		t.Fatalf("%v", err)
	}
}

// Server mode never builds a tunnel whose targets go unchecked.
func TestServerModeTunnelNeedsTargetPolicy(t *testing.T) {
	svc := newServiceWith(t, openStore(t), Options{Dial: dialNowhere})
	_, err := svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app",
		SSH: &engine.SSH{Host: "10.0.0.2", User: "u", Password: "pw", HostKey: fp}})
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "ssh: this server has no network policy for tunnel targets") {
		t.Fatalf("%v", err)
	}
}

// leakEngine fails to open with an error quoting the resolved secrets.
type leakEngine struct{}

func (leakEngine) Name() string                             { return "qhtest-leak" }
func (leakEngine) Dialect() sqlx.Dialect                    { return sqlx.Postgres }
func (leakEngine) Classify(q string) (engine.Access, error) { return sqlx.Classify(q, sqlx.Postgres) }
func (leakEngine) ReferencedObjects(q string) []string {
	return sqlx.ReferencedTables(q, sqlx.Postgres)
}
func (leakEngine) Inspect(string) (engine.DSNInfo, error) { return engine.DSNInfo{}, nil }
func (leakEngine) Open(_ context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	return nil, qerr.New(qerr.ConnectionFailed, "cannot open "+spec.DSN+" with "+spec.Password).WithHint("password was " + spec.Password)
}

var registerLeak sync.Once

func TestOpenConnScrubsResolvedValuesFromEngineErrors(t *testing.T) {
	registerLeak.Do(func() { engine.Register(leakEngine{}) })
	t.Setenv("QH_TEST_LEAK_DSN", "leak://resolved-dsn-value")
	t.Setenv("QH_TEST_LEAK_PW", "resolved-password-value")
	_, err := OpenConn(context.Background(), engine.ConnSpec{Engine: "qhtest-leak", DSNRef: "env:QH_TEST_LEAK_DSN",
		PasswordRef: "env:QH_TEST_LEAK_PW", Resolve: secretref.NewSet(false, nil).Resolve})
	if err == nil || !strings.Contains(err.Error(), "cannot open") {
		t.Fatalf("%v", err)
	}
	if s := err.Error(); strings.Contains(s, "resolved-dsn-value") || strings.Contains(s, "resolved-password-value") {
		t.Fatalf("resolved secret in error: %v", err)
	}
}

// A database target refused by the policy, behind an allowed bastion,
// surfaces through the engine as a connection failure.
func TestServerModeTunnelTargetDenied(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	var d net.Dialer
	svc := newServiceWith(t, openStore(t), Options{Dial: d.DialContext, CheckTarget: func(netip.Addr) bool { return false }})
	_, err := svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "u", Password: "pw", HostKey: srv.Fingerprint}})
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "10.0.0.1:5432 are not allowed by this server's network policy") {
		t.Fatalf("%v", err)
	}
	if got := srv.Targets(); len(got) != 0 {
		t.Fatalf("bastion asked to reach %v", got)
	}
}

// In server mode a tenant's encrypted private key may not ask for more
// than 128 bcrypt rounds: refused before any are run, whether the key is
// given inline (checked on input) or by reference (checked when the tunnel
// is built). Local mode leaves the round count to x/crypto.
func TestServerModeCapsKeyKDFRounds(t *testing.T) {
	const want = "ssh: the private key's KDF uses 1000000 rounds; this server accepts at most 128 (re-encrypt it with ssh-keygen -p -a 100)"
	huge := sshtest.KeyWithRounds(1_000_000)
	var d net.Dialer
	server := newServiceWith(t, openStore(t), Options{Dial: d.DialContext, CheckTarget: func(netip.Addr) bool { return true }})
	local := newHarness(t).svc
	inline := ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app",
		SSH: &engine.SSH{Host: "127.0.0.1", User: "u", HostKey: fp, PrivateKey: huge, Passphrase: "pp"}}

	start := time.Now()
	_, err := server.TestInput(context.Background(), ws, inline)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("refusing took %v", d)
	}
	if qerr.From(err).Category != qerr.Invalid || !strings.Contains(err.Error(), want) {
		t.Fatalf("server, inline: %v", err)
	}
	if _, err := local.TestInput(context.Background(), ws, inline); err == nil || strings.Contains(err.Error(), "this server accepts") {
		t.Fatalf("local, inline: %v", err)
	}

	t.Setenv("QH_TEST_HUGE_KEY", huge)
	spec := engine.ConnSpec{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app", Resolve: secretref.NewSet(false, nil).Resolve,
		SSH: &engine.SSH{Host: "127.0.0.1", Port: 1, User: "u", HostKey: fp, PrivateKeyRef: "env:QH_TEST_HUGE_KEY", Passphrase: "pp"}}
	_, err = OpenConn(context.Background(), spec)
	if err == nil || strings.Contains(err.Error(), "this server accepts") {
		t.Fatalf("local, ref: %v", err)
	}
	spec.Server, spec.Dial, spec.CheckTarget = true, d.DialContext, func(netip.Addr) bool { return true }
	// x/crypto refuses more than 2048 rounds itself, at once; 2048 it would
	// run (for seconds), so that case shows qh's cap at work.
	for _, rounds := range []uint32{2048, 1_000_000} {
		t.Setenv("QH_TEST_HUGE_KEY", sshtest.KeyWithRounds(rounds))
		want := fmt.Sprintf("ssh: the private key's KDF uses %d rounds; this server accepts at most 128", rounds)
		start = time.Now()
		_, err = OpenConn(context.Background(), spec)
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("%d rounds, ref: refusing took %v", rounds, d)
		}
		if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
			t.Fatalf("server, ref: %v", err)
		}
		inline.SSH.PrivateKey = sshtest.KeyWithRounds(rounds)
		start = time.Now()
		_, err = server.TestInput(context.Background(), ws, inline)
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("%d rounds, inline: refusing took %v", rounds, d)
		}
		if qerr.From(err).Category != qerr.Invalid || !strings.Contains(err.Error(), want) {
			t.Fatalf("server, inline: %v", err)
		}
	}
	// An RSA key over 8192 bits is refused in server mode only.
	t.Setenv("QH_TEST_HUGE_KEY", sshtest.RSA8704)
	spec.SSH.Passphrase = ""
	_, err = OpenConn(context.Background(), spec)
	if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "ssh: the private key is an RSA key of 8704 bits; this server accepts at most 8192 bits") {
		t.Fatalf("server, big RSA key: %v", err)
	}
	spec.Server, spec.Dial, spec.CheckTarget = false, nil, nil
	if _, err = OpenConn(context.Background(), spec); err == nil || strings.Contains(err.Error(), "RSA key") {
		t.Fatalf("local, big RSA key: %v", err)
	}
}

// On a --deny-ssh server, SSH settings are refused before the key is
// parsed or decrypted: a junk key gets forbidden, not invalid.
func TestDenySSHRefusesBeforeKeyWork(t *testing.T) {
	ctx := context.Background()
	svc := newServiceWith(t, openStore(t), Options{Dial: dialNowhere, CheckTarget: func(netip.Addr) bool { return true }, DenySSH: true})
	junk := &engine.SSH{Host: "10.0.0.2", User: "u", HostKey: fp, PrivateKey: "junk", Passphrase: "pp"}
	in := ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app", SSH: junk}
	_, err := svc.TestInput(ctx, ws, in)
	if qerr.From(err).Category != qerr.Forbidden {
		t.Fatalf("test input: %v", err)
	}
	if _, _, err := svc.CreateConnection(ctx, ws, in, false); qerr.From(err).Category != qerr.Forbidden {
		t.Fatalf("create: %v", err)
	}
	in.SSH = nil
	if _, _, err := svc.CreateConnection(ctx, ws, in, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.UpdateConnection(ctx, ws, "pg", ConnPatch{SSH: junk}, false); qerr.From(err).Category != qerr.Forbidden {
		t.Fatalf("update: %v", err)
	}
}

// In server mode an inline key is decrypted once, by the tunnel: a wrong
// passphrase is not found on input but when connecting, and named there.
func TestServerModeWrongPassphraseFoundWhenConnecting(t *testing.T) {
	ctx := context.Background()
	enc, _ := sshtest.Key(t, "pp")
	var d net.Dialer
	svc := newServiceWith(t, openStore(t), Options{Dial: d.DialContext, CheckTarget: func(netip.Addr) bool { return true }})
	in := ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: 1, User: "u", HostKey: fp, PrivateKey: enc, Passphrase: "wrong"}}
	const want = "ssh: the passphrase does not decrypt the private key"
	if _, err := svc.TestInput(ctx, ws, in); qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
		t.Fatalf("test input: %v", err)
	}
	if _, _, err := svc.CreateConnection(ctx, ws, in, false); err != nil {
		t.Fatalf("create without a test: %v", err)
	}
	if _, err := svc.TestConnection(ctx, ws, "pg"); qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
		t.Fatalf("test connection: %v", err)
	}
}

// A private_key_ref or password_ref that resolves to nothing is an error,
// not a silent attempt at password authentication with an empty password.
func TestEmptySSHRefs(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for want, s := range map[string]*engine.SSH{
		"ssh: private_key_ref resolved to an empty value": {Host: "127.0.0.1", Port: 1, User: "u", HostKey: fp, PrivateKeyRef: "file:" + empty},
		"ssh: password_ref resolved to an empty value":    {Host: "127.0.0.1", Port: 1, User: "u", HostKey: fp, PasswordRef: "file:" + empty},
	} {
		_, err := OpenConn(context.Background(), engine.ConnSpec{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app",
			Resolve: secretref.NewSet(false, nil).Resolve, SSH: s})
		if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

// In server mode a DSA key is refused, given inline (on input) or by
// reference (when the tunnel is built).
func TestServerModeRefusesDSAKeys(t *testing.T) {
	const want = "ssh: DSA keys are not accepted; use an Ed25519, ECDSA or RSA key"
	key := sshtest.DSAKey(t)
	allow := func(netip.Addr) bool { return true }
	var d net.Dialer
	svc := newServiceWith(t, openStore(t), Options{Dial: d.DialContext, CheckTarget: allow})
	_, err := svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: 1, User: "u", HostKey: fp, PrivateKey: key}})
	if qerr.From(err).Category != qerr.Invalid || !strings.Contains(err.Error(), want) {
		t.Fatalf("inline: %v", err)
	}
	t.Setenv("QH_TEST_DSA_KEY", key)
	spec := engine.ConnSpec{Engine: "postgres", DSN: "postgres://u:dbpw@10.0.0.1:5432/app", Resolve: secretref.NewSet(false, nil).Resolve,
		Server: true, Dial: d.DialContext, CheckTarget: allow,
		SSH: &engine.SSH{Host: "127.0.0.1", Port: 1, User: "u", HostKey: fp, PrivateKeyRef: "env:QH_TEST_DSA_KEY"}}
	if _, err := OpenConn(context.Background(), spec); qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
		t.Fatalf("ref: %v", err)
	}
	spec.Server, spec.Dial, spec.CheckTarget = false, nil, nil
	if _, err := OpenConn(context.Background(), spec); err == nil || strings.Contains(err.Error(), "DSA") {
		t.Fatalf("local: %v", err)
	}
}
