package core

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func startPG(t *testing.T) (realAddr, user, password string) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("app"), tcpostgres.WithPassword("first-pw"), tcpostgres.BasicWaitStrategies())
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	return u.Host, "app", "first-pw"
}

func TestQueryThroughTunnelAndRotatePassword(t *testing.T) {
	real, user, pw := startPG(t)
	key, pub := sshtest.Key(t, "")
	srv := sshtest.Start(t, sshtest.Options{User: "jump", AuthorizedKey: pub,
		Forward: func(target string) (net.Conn, error) {
			if target != "db.internal:5432" {
				return nil, net.UnknownNetworkError(target)
			}
			return net.Dial("tcp", real)
		}})
	h := newHarness(t)
	ctx := context.Background()
	t.Setenv("QH_TEST_PG_PW", pw)
	_, tr, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", PoolSize: 1,
		DSN: "postgres://" + user + "@db.internal:5432/app?sslmode=disable", PasswordRef: "env:QH_TEST_PG_PW",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "jump", HostKey: srv.Fingerprint, PrivateKey: key}}, true)
	if err != nil || tr == nil {
		t.Fatalf("create: %v", err)
	}
	out, err := h.svc.Query(ctx, ws, QueryInput{Source: "pg", Query: "SELECT current_user"})
	if err != nil || !strings.Contains(out, user) {
		t.Fatalf("query: %q %v", out, err)
	}
	if got := srv.Targets(); len(got) == 0 || got[0] != "db.internal:5432" {
		t.Fatalf("targets %v", got)
	}

	// Rotate: change the password in Postgres and in the environment, then
	// end the pooled session so the next call must reconnect.
	admin, err := pgx.Connect(ctx, "postgres://"+user+":"+pw+"@"+real+"/app?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "ALTER USER app PASSWORD 'second-pw'"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QH_TEST_PG_PW", "second-pw")
	if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'app' AND pid <> pg_backend_pid()"); err != nil {
		t.Fatal(err)
	}
	var lastErr error
	for range 3 { // the first call may fail on the dead session; the pool is then dropped and reopened
		if out, lastErr = h.svc.Query(ctx, ws, QueryInput{Source: "pg", Query: "SELECT 1 AS n"}); lastErr == nil {
			break
		}
	}
	if lastErr != nil || !strings.HasPrefix(out, "n\n1\n") {
		t.Fatalf("after rotation: %q %v", out, lastErr)
	}
}
