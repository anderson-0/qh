package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
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
	c.SetDeadline(time.Now().Add(5 * time.Second))
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
	if !strings.Contains(err.Error(), "--ssh-host-key "+srv.Fingerprint) {
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
	srv := sshtest.Start(t, sshtest.Options{User: "u", Password: "pw"})
	deny := func(a netip.Addr) bool { return !a.IsPrivate() }
	tun, _ := New(Config{Addr: srv.Addr, User: "u", Password: "pw", HostKey: srv.Fingerprint, CheckTarget: deny})
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
	_, err = tun.Dial(ctx, "tcp", "no-such-host.invalid:5432")
	if err == nil || !strings.Contains(err.Error(), "Use the database's IP address") {
		t.Fatalf("unresolvable name: %v", err)
	}
	// The bastion is asked for the checked IP, never the name.
	for _, tg := range srv.Targets() {
		if strings.Contains(tg, "localhost") {
			t.Fatalf("name sent to bastion: %v", srv.Targets())
		}
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
