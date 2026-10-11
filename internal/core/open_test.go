package core

import (
	"context"
	"net"
	"net/netip"
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

func (leakEngine) Name() string                           { return "qhtest-leak" }
func (leakEngine) Dialect() sqlx.Dialect                  { return sqlx.Postgres }
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
