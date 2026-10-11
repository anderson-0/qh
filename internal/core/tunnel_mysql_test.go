package core

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/mysql"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func TestMysqlQueryThroughTunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("app"),
		tcmysql.WithUsername("app"), tcmysql.WithPassword("mysql-pw"))
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	real := net.JoinHostPort(host, port.Port())

	srv := sshtest.Start(t, sshtest.Options{User: "jump", Password: "ssh-pw",
		Forward: func(target string) (net.Conn, error) {
			if target != "db.internal:3306" {
				return nil, net.UnknownNetworkError(target)
			}
			return net.Dial("tcp", real)
		}})
	h := newHarness(t)
	t.Setenv("QH_TEST_MY_PW", "mysql-pw")
	_, tr, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "my", Engine: "mysql", PoolSize: 1,
		DSN: "mysql://app@db.internal:3306/app", PasswordRef: "env:QH_TEST_MY_PW",
		SSH: &engine.SSH{Host: "127.0.0.1", Port: portOf(t, srv.Addr), User: "jump", HostKey: srv.Fingerprint, Password: "ssh-pw"}}, true)
	if err != nil || tr == nil {
		t.Fatalf("create: %v", err)
	}
	out, err := h.svc.Query(ctx, ws, QueryInput{Source: "my", Query: "SELECT 1 AS n"})
	if err != nil || !strings.HasPrefix(out, "n\n1\n") {
		t.Fatalf("query: %q %v", out, err)
	}
	if got := srv.Targets(); len(got) == 0 || got[0] != "db.internal:3306" {
		t.Fatalf("targets %v", got)
	}
}
