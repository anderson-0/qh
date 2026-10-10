package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func mustPolicy(t *testing.T, allow, deny []string) *Policy {
	t.Helper()
	p, err := New(allow, deny)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAllowed(t *testing.T) {
	p := mustPolicy(t, nil, []string{"10.0.0.0/8"})
	cases := map[string]bool{
		"127.0.0.1": false, "127.9.9.9": false, "::1": false, "0.0.0.0": false, "::": false,
		"169.254.169.254": false, "fe80::1": false, "fd00:ec2::254": false, "100.100.100.200": false,
		"::ffff:127.0.0.1": false, "10.1.2.3": false, "::1%lo0": false, "fe80::1%en0": false,
		"192.168.1.10": true, "172.16.0.5": true, "8.8.8.8": true, "2606:4700::1111": true,
	}
	for ip, want := range cases {
		if got := p.Allowed(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", ip, got, want)
		}
	}
	open := mustPolicy(t, []string{"127.0.0.0/8"}, nil)
	if !open.Allowed(netip.MustParseAddr("127.0.0.1")) || open.Allowed(netip.MustParseAddr("169.254.169.254")) {
		t.Fatal("allow list must take precedence only for its own ranges")
	}
	if _, err := New([]string{"not-a-cidr"}, nil); err == nil || !strings.Contains(err.Error(), "not-a-cidr") {
		t.Fatalf("bad cidr: %v", err)
	}
}

func TestDialRefusesLoopbackForms(t *testing.T) {
	p := mustPolicy(t, nil, nil)
	p.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "localhost", "evil.example.com":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, errors.New("unexpected lookup of " + host)
	}
	for _, addr := range []string{"127.0.0.1:5432", "127.1:5432", "[::1]:5432", "[::ffff:127.0.0.1]:5432", "[::1%lo0]:5432", "localhost:5432", "evil.example.com:5432", "169.254.169.254:80"} {
		_, err := p.DialContext(context.Background(), "tcp", addr)
		var de *DeniedError
		if !errors.As(err, &de) || !strings.Contains(err.Error(), "not allowed by this server's network policy") {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if _, err := p.DialContext(context.Background(), "unix", "/var/run/postgresql/.s.PGSQL.5432"); err == nil {
		t.Error("unix socket allowed")
	}
}

func TestDialAllowedTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	p := mustPolicy(t, []string{"127.0.0.0/8"}, nil)
	c, err := p.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("allowed dial failed: %v", err)
	}
	c.Close()
}

func TestDialSkipsDeniedAddressesOfAName(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p := mustPolicy(t, []string{"127.0.0.2/32"}, nil) // only 127.0.0.2 is allowed …
	p.Resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := p.DialContext(context.Background(), "tcp", "db.example.com:"+port); err == nil {
		t.Fatal("dialled a denied address") // … so neither resolved address may be dialled
	}
}
