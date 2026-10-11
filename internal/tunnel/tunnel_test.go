package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

// echo starts a TCP echo server and returns its address.
func echo(t *testing.T) string {
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
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, c net.Conn) {
	t.Helper()
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "ping" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestKeyAndPasswordAuth(t *testing.T) {
	target := echo(t)
	key, pub := sshtest.Key(t, "")
	enc, encPub := sshtest.Key(t, "pp")
	ctx := context.Background()
	for name, c := range map[string]struct {
		srv sshtest.Options
		cfg Config
	}{
		"key":           {sshtest.Options{User: "u", AuthorizedKey: pub}, Config{User: "u", PrivateKey: key}},
		"encrypted key": {sshtest.Options{User: "u", AuthorizedKey: encPub}, Config{User: "u", PrivateKey: enc, Passphrase: "pp"}},
		"password":      {sshtest.Options{User: "u", Password: "pw"}, Config{User: "u", Password: "pw"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := sshtest.Start(t, c.srv)
			c.cfg.Addr, c.cfg.HostKey = srv.Addr, srv.Fingerprint
			tun, err := New(c.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer tun.Close()
			conn, err := tun.Dial(ctx, "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip(t, conn)
		})
	}
}

func TestHostKeyAndAuthFailures(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	ctx := context.Background()
	other := "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"
	for want, cfg := range map[string]Config{
		"ssh: host key mismatch for " + srv.Addr + " (got " + srv.Fingerprint + ", expected " + other + ")": {Addr: srv.Addr, User: "u", Password: "pw", HostKey: other},
		"ssh: unknown host key for " + srv.Addr + ": it presented " + srv.Fingerprint:                       {Addr: srv.Addr, User: "u", Password: "pw"},
		"ssh: authentication failed for u@" + srv.Addr:                                                      {Addr: srv.Addr, User: "u", Password: "wrong", HostKey: srv.Fingerprint},
		"ssh: cannot reach the bastion 127.0.0.1:1":                                                         {Addr: "127.0.0.1:1", User: "u", Password: "pw", HostKey: srv.Fingerprint},
	} {
		tun, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		err = tun.Connect(ctx)
		tun.Close()
		if err == nil {
			t.Errorf("want %q, got success", want)
			continue
		}
		if qerr.From(err).Category != qerr.ConnectionFailed || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
		if err != nil && strings.Contains(err.Error(), "wrong") {
			t.Errorf("password leaked: %v", err)
		}
	}
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw"})
	err := tun.Connect(ctx)
	tun.Close()
	if err == nil || !strings.Contains(err.Error(), "--ssh-host-key "+srv.Fingerprint) {
		t.Errorf("unknown key hint: %v", err)
	}
}

func TestTargetUnreachableFromBastion(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	_, err := tun.Dial(context.Background(), "tcp", "127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "ssh: the bastion could not reach 127.0.0.1:1") {
		t.Fatal(err)
	}
}

func TestReconnectAfterBastionDrop(t *testing.T) {
	target := echo(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	ctx := context.Background()
	c, err := tun.Dial(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c)
	srv.DropClients()
	time.Sleep(50 * time.Millisecond)
	c, err = tun.Dial(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("no reconnect: %v", err)
	}
	roundTrip(t, c)
	if srv.Conns() != 2 {
		t.Fatalf("ssh connections = %d, want 2", srv.Conns())
	}
}

func TestConcurrentDialsShareOneConnection(t *testing.T) {
	target := echo(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := tun.Dial(context.Background(), "tcp", target)
			if err != nil {
				errs <- err
				return
			}
			c.Write([]byte("x"))
			b := make([]byte, 1)
			io.ReadFull(c, b)
			c.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if srv.Conns() != 1 {
		t.Fatalf("ssh connections = %d, want 1", srv.Conns())
	}
}

func TestServerModeTargetPolicy(t *testing.T) {
	target := echo(t) // 127.0.0.1:port — loopback, exempt
	_, port, _ := net.SplitHostPort(target)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	deny := func(a netip.Addr) bool { return !a.IsPrivate() }
	resolve := func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "db.test":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "mixed.test":
			return []netip.Addr{netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, errors.New("no such host")
	}
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint, CheckTarget: deny, Resolve: resolve})
	defer tun.Close()
	ctx := context.Background()
	c, err := tun.Dial(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("loopback target refused: %v", err)
	}
	roundTrip(t, c)
	_, err = tun.Dial(ctx, "tcp", "10.1.2.3:5432")
	var de *netguard.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("private target allowed: %v", err)
	}
	_, err = tun.Dial(ctx, "tcp", "mixed.test:5432")
	if !errors.As(err, &de) {
		t.Fatalf("name with a denied address allowed: %v", err)
	}
	_, err = tun.Dial(ctx, "tcp", "no-such-host.invalid:5432")
	if err == nil || !strings.Contains(err.Error(), "Use the database's IP address") {
		t.Fatalf("unresolvable name: %v", err)
	}
	// A name is resolved here and the bastion is asked for the checked IP.
	c, err = tun.Dial(ctx, "tcp", "db.test:"+port)
	if err != nil {
		t.Fatalf("resolvable name refused: %v", err)
	}
	roundTrip(t, c)
	targets := srv.Targets()
	for _, tg := range targets {
		if strings.Contains(tg, "db.test") {
			t.Fatalf("name sent to bastion: %v", targets)
		}
	}
	if len(targets) != 2 || targets[0] != target || targets[1] != "127.0.0.1:"+port {
		t.Fatalf("bastion targets = %v", targets)
	}
	// A cancelled context is reported as such, not as an unresolvable name.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	slow := func(ctx context.Context, _ string) ([]netip.Addr, error) { return nil, ctx.Err() }
	tun2, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint, CheckTarget: deny, Resolve: slow})
	defer tun2.Close()
	if _, err := tun2.Dial(cctx, "tcp", "db.test:5432"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolve: %v", err)
	}
}

func TestBastionDialUsesPolicy(t *testing.T) {
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	called := false
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint,
		DialBastion: func(ctx context.Context, n, a string) (net.Conn, error) {
			called = true
			return nil, &netguard.DeniedError{Target: a}
		}})
	defer tun.Close()
	err := tun.Connect(context.Background())
	var de *netguard.DeniedError
	if !called || !errors.As(err, &de) {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

// An injected bastion dialer (server mode: netguard, which has no timeout of
// its own) is still bounded by the handshake limit.
func TestBastionDialIsBounded(t *testing.T) {
	var left time.Duration
	tun, _ := New(Config{Addr: "203.0.113.1:22", User: "u", Password: "pw",
		DialBastion: func(ctx context.Context, n, a string) (net.Conn, error) {
			d, ok := ctx.Deadline()
			if !ok {
				return nil, errors.New("no deadline")
			}
			left = time.Until(d)
			return nil, errors.New("refused")
		}})
	defer tun.Close()
	err := tun.Connect(context.Background())
	if err == nil || left <= 0 || left > handshakeTimeout {
		t.Fatalf("left=%v err=%v", left, err)
	}
}

// expiredCtx has passed its deadline but does not report it yet, as a
// timer context does in the instant before its timer fires.
type expiredCtx struct{ context.Context }

func (expiredCtx) Deadline() (time.Time, bool) { return time.Now().Add(-time.Millisecond), true }

// A dial or handshake that fails because the caller's deadline passed is
// reported as the deadline, even before the context marks itself done
// (socket deadlines and context timers fire independently).
func TestPassedDeadlineReportedAsDeadline(t *testing.T) {
	tun, _ := New(Config{Addr: "203.0.113.1:22", User: "u", Password: "pw",
		DialBastion: func(ctx context.Context, n, a string) (net.Conn, error) {
			return nil, errors.New("dial tcp 203.0.113.1:22: i/o timeout")
		}})
	defer tun.Close()
	if err := tun.Connect(expiredCtx{context.Background()}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
}

func TestBadAddressKeepsSharedClient(t *testing.T) {
	target := echo(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	ctx := context.Background()
	roundTrip(t, mustDial(t, tun, target))
	for _, c := range []struct{ network, addr, want string }{
		{"tcp", "db:notaport", "ssh: invalid database address db:notaport"},
		{"tcp", "db", "ssh: invalid database address db"},
		{"tcp", ":5432", "ssh: invalid database address :5432"},
		{"tcp", "db:70000", "ssh: invalid database address db:70000"},
		{"unix", "/var/run/mysqld.sock", "ssh: only TCP connections can go through an SSH tunnel"},
	} {
		_, err := tun.Dial(ctx, c.network, c.addr)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: want %q, got %v", c.network, c.addr, c.want, err)
		}
	}
	roundTrip(t, mustDial(t, tun, target))
	if srv.Conns() != 1 {
		t.Fatalf("ssh connections = %d, want 1 (the shared client was dropped)", srv.Conns())
	}
}

func mustDial(t *testing.T, tun *Tunnel, addr string) net.Conn {
	t.Helper()
	c, err := tun.Dial(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// hang accepts TCP connections and never speaks SSH. accepted receives one
// value per connection.
func hang(t *testing.T) (addr string, accepted chan struct{}) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted = make(chan struct{}, 16)
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
			accepted <- struct{}{}
		}
	}()
	return ln.Addr().String(), accepted
}

func TestHangingHandshakeHonoursContextsAndClose(t *testing.T) {
	addr, accepted := hang(t)
	tun, _ := New(Config{Addr: addr, User: "u", Password: "pw", HostKey: "SHA256:x"})
	defer tun.Close()

	// A short context ends the handshake promptly.
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	_, err := tun.Dial(ctx, "tcp", "127.0.0.1:5432")
	cancel()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("hung handshake: err=%v after %v", err, time.Since(start))
	}
	<-accepted

	// While one caller handshakes, another with its own short context and
	// Close are not blocked behind it.
	first := make(chan error, 1)
	go func() {
		_, err := tun.Dial(context.Background(), "tcp", "127.0.0.1:5432")
		first <- err
	}()
	<-accepted
	start = time.Now()
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	_, err = tun.Dial(ctx, "tcp", "127.0.0.1:5432")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("second dial: err=%v after %v", err, time.Since(start))
	}
	start = time.Now()
	tun.Close()
	if time.Since(start) > time.Second {
		t.Fatalf("Close blocked for %v", time.Since(start))
	}
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("dial succeeded after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hung dial not released by Close")
	}
}

func TestDialedConnSupportsDeadlines(t *testing.T) {
	target := echo(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	c := mustDial(t, tun, target)
	defer c.Close()
	if err := c.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := c.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("read with deadline: %v after %v", err, time.Since(start))
	}
	if err := c.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping"))
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "ping" {
		t.Fatalf("after deadline: %q %v", b, err)
	}
	if c.RemoteAddr() == nil || c.LocalAddr() == nil {
		t.Fatal("missing addresses")
	}
}

func TestDialedConnsDoNotLeakGoroutines(t *testing.T) {
	target := echo(t)
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint})
	defer tun.Close()
	roundTrip(t, mustDial(t, tun, target)) // connects; settle the baseline
	settle := func(max int) int {
		n := runtime.NumGoroutine()
		for end := time.Now().Add(3 * time.Second); n > max && time.Now().Before(end); n = runtime.NumGoroutine() {
			time.Sleep(20 * time.Millisecond)
		}
		return n
	}
	time.Sleep(300 * time.Millisecond)
	base := runtime.NumGoroutine()
	for range 20 {
		roundTrip(t, mustDial(t, tun, target))
	}
	if n := settle(base + 2); n > base+2 {
		t.Fatalf("goroutines grew from %d to %d", base, n)
	}
}
