package mysql

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func TestOpenUsesDial(t *testing.T) {
	called := false
	spec := engine.ConnSpec{ID: "x", DSN: "mysql://u:hunter22@db.example.com:3306/db?timeout=2s", Mode: engine.ModeRO, PoolSize: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			called = true
			return nil, errors.New("blocked by test policy")
		}}
	_, err := Engine{}.Open(context.Background(), spec)
	if !called || qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "blocked by test policy") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

// A unix-socket address must also go through Dial, so server mode can refuse
// it there instead of the driver bypassing the policy.
func TestOpenUsesDialForUnixSocket(t *testing.T) {
	var network, addr string
	spec := engine.ConnSpec{ID: "x", DSN: "u@unix(/tmp/mysql.sock)/db?timeout=2s", Mode: engine.ModeRO, PoolSize: 1,
		Dial: func(_ context.Context, n, a string) (net.Conn, error) {
			network, addr = n, a
			return nil, errors.New("blocked by test policy")
		}}
	_, err := Engine{}.Open(context.Background(), spec)
	if network != "unix" || addr != "/tmp/mysql.sock" || err == nil || !strings.Contains(err.Error(), "blocked by test policy") {
		t.Fatalf("network=%q addr=%q err=%v", network, addr, err)
	}
}
