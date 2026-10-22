package mysql

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/netguard"
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

// A native DSN can name any network; the server-mode policy refuses all but
// TCP before it looks up a name. Loopback is allowed here, so only the
// network can cause the refusal.
func TestOpenRefusesNonTCPNetworkThroughPolicy(t *testing.T) {
	const secret = "pw-!t"
	policy, err := netguard.New([]string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	policy.Resolve = func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	var mu sync.Mutex
	var calls []string // "network addr"
	var conns int
	open := func(dsn string) error {
		mu.Lock()
		calls, conns = nil, 0
		mu.Unlock()
		lookups.Store(0)
		spec := engine.ConnSpec{ID: "x", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1,
			Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
				c, err := policy.DialContext(ctx, n, a)
				mu.Lock()
				calls = append(calls, n+" "+a)
				if c != nil {
					conns++
				}
				mu.Unlock()
				return c, err
			}}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c, err := Engine{}.Open(ctx, spec)
		if c != nil {
			c.Close()
		}
		return err
	}
	recorded := func() ([]string, int) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls), conns
	}

	const want = "cannot reach the database: connections to non-TCP networks are not allowed by this server's network policy"
	for _, c := range []struct{ dsn, call string }{
		{"u:" + secret + "@udp(127.0.0.1:9)/db?timeout=2s", "udp 127.0.0.1:9"},
		// A name is refused before it is looked up.
		{"u:" + secret + "@udp(localhost:9)/db?timeout=2s", "udp localhost:9"},
	} {
		err := open(c.dsn)
		got, n := recorded()
		if !slices.Equal(got, []string{c.call}) || n != 0 || lookups.Load() != 0 {
			t.Errorf("%s: calls %q, conns %d, lookups %d; want [%q], 0, 0", c.call, got, n, lookups.Load(), c.call)
		}
		if err == nil {
			t.Errorf("%s: opened", c.call)
			continue
		}
		if qe := qerr.From(err); qe.Category != qerr.ConnectionFailed || qe.Message != want {
			t.Errorf("%s: %s %q, want %s %q", c.call, qe.Category, qe.Message, qerr.ConnectionFailed, want)
		}
		var de *netguard.DeniedError
		if !errors.As(err, &de) {
			t.Errorf("%s: %v is not a policy refusal", c.call, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error shows the password: %v", c.call, err)
		}
	}

	// Over tcp4 the policy dials the address, so the refusals above come from
	// the network alone. The listener closes each connection, so Open fails.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	err = open("u:" + secret + "@tcp4(" + ln.Addr().String() + ")/db?timeout=2s")
	got, n := recorded()
	other := func(call string) bool { return call != "tcp4 "+ln.Addr().String() }
	if err == nil || len(got) == 0 || slices.ContainsFunc(got, other) || n == 0 || accepted.Load() == 0 {
		t.Fatalf("tcp4: calls %q, conns %d, accepted %d, err %v", got, n, accepted.Load(), err)
	}
}
