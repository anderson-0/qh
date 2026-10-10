package postgres

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
	spec := engine.ConnSpec{ID: "x", DSN: "postgres://u:hunter22@db.example.com:5432/db?connect_timeout=2", Mode: engine.ModeRO, PoolSize: 1,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			called = true
			return nil, errors.New("blocked by test policy")
		}}
	_, err := Engine{}.Open(context.Background(), spec)
	if !called || qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), "blocked by test policy") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

// A unix-socket host must also go through Dial, so server mode can refuse it
// there instead of the driver bypassing the policy.
func TestOpenUsesDialForUnixSocket(t *testing.T) {
	var network, addr string
	spec := engine.ConnSpec{ID: "x", DSN: "postgres://u@/db?host=/tmp&connect_timeout=2", Mode: engine.ModeRO, PoolSize: 1,
		Dial: func(_ context.Context, n, a string) (net.Conn, error) {
			network, addr = n, a
			return nil, errors.New("blocked by test policy")
		}}
	_, err := Engine{}.Open(context.Background(), spec)
	if network != "unix" || !strings.HasPrefix(addr, "/tmp/") || err == nil || !strings.Contains(err.Error(), "blocked by test policy") {
		t.Fatalf("network=%q addr=%q err=%v", network, addr, err)
	}
}
