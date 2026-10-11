package core

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
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
