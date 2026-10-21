package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
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

// errDial is wrapped by the errors the fake transport returns for its failing
// addresses.
var errDial = errors.New("dial failed")

// fakeNet stands in for a policy's resolver and transport and records every
// call to them.
type fakeNet struct {
	lookup  func(host string) ([]netip.Addr, error)
	fail    []string     // transport addresses that fail with errDial
	lookups []string     // hosts looked up
	answers []netip.Addr // every address a lookup returned
	dials   []string     // transport calls, as "network addr"
}

// fake installs a fakeNet on p. A nil lookup fails every lookup. Transport
// calls to the fail addresses return an error that wraps errDial and names
// the address; the others return one end of a pipe whose other end is closed.
func fake(p *Policy, lookup func(host string) ([]netip.Addr, error), fail ...string) *fakeNet {
	f := &fakeNet{lookup: lookup, fail: fail}
	p.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		f.lookups = append(f.lookups, host)
		if f.lookup == nil {
			return nil, errors.New("unexpected lookup of " + host)
		}
		ips, err := f.lookup(host)
		f.answers = append(f.answers, ips...)
		return ips, err
	}
	p.dial = func(_ context.Context, network, addr string) (net.Conn, error) {
		f.dials = append(f.dials, network+" "+addr)
		if slices.Contains(f.fail, addr) {
			return nil, fmt.Errorf("%w: %s", errDial, addr)
		}
		c, other := net.Pipe()
		other.Close()
		return c, nil
	}
	return f
}

// check fails unless the recorded lookups and transport calls are exactly
// lookups and dials.
func (f *fakeNet) check(t *testing.T, name string, lookups, dials []string) {
	t.Helper()
	if !slices.Equal(f.lookups, lookups) {
		t.Errorf("%s: lookups %q, want %q", name, f.lookups, lookups)
	}
	if !slices.Equal(f.dials, dials) {
		t.Errorf("%s: transport calls %q, want %q", name, f.dials, dials)
	}
}

// checkPinned fails unless every transport call went to an IP address that a
// lookup returned and p allows, and none names host.
func (f *fakeNet) checkPinned(t *testing.T, name string, p *Policy, host string) {
	t.Helper()
	for _, call := range f.dials {
		_, addr, _ := strings.Cut(call, " ")
		ap, err := netip.ParseAddrPort(addr)
		if err != nil || strings.Contains(addr, host) {
			t.Errorf("%s: transport call %q is not to an IP address", name, call)
			continue
		}
		got := ap.Addr().WithZone("")
		if !slices.ContainsFunc(f.answers, func(a netip.Addr) bool {
			return p.Allowed(a) && a.Unmap().WithZone("") == got
		}) {
			t.Errorf("%s: transport call %q is not to an allowed answer of %v", name, call, f.answers)
		}
	}
}

// answer returns a lookup that always answers ips.
func answer(ips ...string) func(string) ([]netip.Addr, error) {
	return func(string) ([]netip.Addr, error) { return addrs(ips...), nil }
}

func addrs(ips ...string) []netip.Addr {
	out := make([]netip.Addr, len(ips))
	for i, ip := range ips {
		out[i] = netip.MustParseAddr(ip)
	}
	return out
}

// tryDial dials through p and closes any connection it returns.
func tryDial(t *testing.T, p *Policy, network, addr string) error {
	t.Helper()
	c, err := p.DialContext(context.Background(), network, addr)
	if c != nil {
		c.Close()
	}
	if (c == nil) == (err == nil) {
		t.Errorf("%q %s: conn %v with error %v", network, addr, c, err)
	}
	return err
}

// checkErr fails unless err matches want: nil, a *DeniedError or
// *net.DNSError equal to want, or another error that err wraps. Only a
// *DeniedError want accepts a *DeniedError.
func checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	var de *DeniedError
	switch w := want.(type) {
	case nil:
		if err != nil {
			t.Errorf("%s: %v, want success", name, err)
		}
	case *DeniedError:
		if !errors.As(err, &de) || *de != *w {
			t.Errorf("%s: %v, want %v", name, err, w)
		}
	case *net.DNSError:
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) || *dnsErr != *w || errors.As(err, &de) {
			t.Errorf("%s: %v, want %v", name, err, w)
		}
	default:
		if !errors.Is(err, w) || errors.As(err, &de) {
			t.Errorf("%s: %v, want %v", name, err, w)
		}
	}
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

// The three ranges that embed IPv4 addresses are denied by default, up to
// their edges.
func TestDefaultDenyBoundaries(t *testing.T) {
	p := mustPolicy(t, nil, nil)
	for _, ip := range []string{
		"64:ff9b::", "64:ff9b::ffff:ffff", "64:ff9b::7f00:1", "64:ff9b::127.0.0.1", "64:ff9b::a9fe:a9fe",
		"64:ff9b::cb00:7105", // a translated public address is denied too
		"::", "::1", "::7f00:1", "::127.0.0.1", "::ffff:ffff",
		"64:ff9b:1::", "64:ff9b:1::7f00:1", "64:ff9b:1:ffff:ffff:ffff:ffff:ffff",
	} {
		if p.Allowed(netip.MustParseAddr(ip)) {
			t.Errorf("Allowed(%s) = true, want false", ip)
		}
	}
	for _, ip := range []string{
		"64:ff9a:ffff:ffff:ffff:ffff:ffff:ffff", "64:ff9b::1:0:0",
		"64:ff9b:0:ffff:ffff:ffff:ffff:ffff", // between the two NAT64 ranges
		"64:ff9b:2::", "::1:0:0",
	} {
		if !p.Allowed(netip.MustParseAddr(ip)) {
			t.Errorf("Allowed(%s) = false, want true", ip)
		}
	}
}

// IPv4-mapped addresses are checked as the IPv4 addresses they map, not
// against ::/96, and are dialled in IPv4 form.
func TestMappedAddresses(t *testing.T) {
	p := mustPolicy(t, nil, nil)
	cases := map[string]bool{"::ffff:127.0.0.1": false, "::ffff:169.254.169.254": false, "::ffff:192.0.2.10": true}
	for ip, want := range cases {
		if got := p.Allowed(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", ip, got, want)
		}
	}
	f := fake(p, nil)
	checkErr(t, "mapped literal", tryDial(t, p, "tcp", "[::ffff:192.0.2.10]:5432"), nil)
	f.check(t, "mapped literal", nil, []string{"tcp 192.0.2.10:5432"})
}

// Allowed checks an address without its zone. DialContext keeps the zone,
// which never changes the address it dials.
func TestZones(t *testing.T) {
	p := mustPolicy(t, nil, nil)
	for _, ip := range []string{"64:ff9b::7f00:1%eth0", "::7f00:1%eth0", "64:ff9b:1::1%eth0", "::ffff:127.0.0.1%eth0", "::1%lo0", "fe80::1%en0"} {
		if p.Allowed(netip.MustParseAddr(ip)) {
			t.Errorf("Allowed(%s) = true, want false", ip)
		}
	}
	link := mustPolicy(t, []string{"fe80::/10"}, nil)
	f := fake(link, nil)
	checkErr(t, "zone", tryDial(t, link, "tcp", "[fe80::1%en0]:5432"), nil)
	f.check(t, "zone", nil, []string{"tcp [fe80::1%en0]:5432"})

	f = fake(link, nil)
	checkErr(t, "zone with a colon", tryDial(t, link, "tcp", "[fe80::1%a:b]:5432"), nil)
	f.check(t, "zone with a colon", nil, []string{"tcp [fe80::1%a:b]:5432"})
	if len(f.dials) == 1 {
		_, addr, _ := strings.Cut(f.dials[0], " ")
		ap, err := netip.ParseAddrPort(addr)
		if err != nil || ap.Addr().WithZone("") != netip.MustParseAddr("fe80::1") || ap.Port() != 5432 {
			t.Errorf("transport address %q re-parses to %v, %v", addr, ap, err)
		}
	}
}

// An allow range takes precedence over the deny ranges for exactly the
// addresses it covers. An IPv4 allow does not cover translated forms.
func TestAllowPrecedence(t *testing.T) {
	cases := []struct {
		allow, deny []string
		ips         map[string]bool
	}{
		{allow: []string{"64:ff9b::/96"}, ips: map[string]bool{"64:ff9b::7f00:1": true, "::7f00:1": false, "64:ff9b:1::1": false}},
		{allow: []string{"64:ff9b::cb00:7105/128"}, ips: map[string]bool{"64:ff9b::cb00:7105": true, "64:ff9b::cb00:7106": false}},
		// 203.0.113.0/24 translated into 64:ff9b::/96
		{allow: []string{"64:ff9b::cb00:7100/120"}, ips: map[string]bool{"64:ff9b::cb00:7163": true, "64:ff9b::cb00:7200": false, "64:ff9b::7f00:1": false}},
		{allow: []string{"64:ff9b:1::/48"}, ips: map[string]bool{"64:ff9b:1::1": true}},
		{allow: []string{"::/96"}, ips: map[string]bool{"::7f00:1": true, "::1": true}},
		{allow: []string{"127.0.0.0/8"}, ips: map[string]bool{"127.0.0.1": true, "64:ff9b::7f00:1": false, "::7f00:1": false}},
		{allow: []string{"::ffff:127.0.0.1/128"}, ips: map[string]bool{"127.0.0.1": true, "64:ff9b::7f00:1": false, "::7f00:1": false}},
		{allow: []string{"64:ff9b::cb00:7105/128"}, deny: []string{"64:ff9b::/96"}, ips: map[string]bool{"64:ff9b::cb00:7105": true, "64:ff9b::cb00:7106": false}},
	}
	for _, c := range cases {
		p := mustPolicy(t, c.allow, c.deny)
		for ip, want := range c.ips {
			if got := p.Allowed(netip.MustParseAddr(ip)); got != want {
				t.Errorf("allow %v, deny %v: Allowed(%s) = %v, want %v", c.allow, c.deny, ip, got, want)
			}
		}
	}
	p := mustPolicy(t, []string{"64:ff9b::/96"}, nil)
	f := fake(p, nil)
	checkErr(t, "allowed NAT64 literal", tryDial(t, p, "tcp", "[64:ff9b::cb00:7105]:5432"), nil)
	f.check(t, "allowed NAT64 literal", nil, []string{"tcp [64:ff9b::cb00:7105]:5432"})
}

// Only tcp, tcp4 and tcp6 are dialled. Any other network is refused before
// its address is parsed or looked up.
func TestDialRefusesOtherNetworks(t *testing.T) {
	groups := []struct {
		networks []string
		want     func(addr string) string
	}{
		{[]string{"unix", "unixgram", "unixpacket"}, func(addr string) string {
			return "connections to unix socket " + addr + " are not allowed by this server's network policy"
		}},
		{[]string{"", "udp", "udp4", "udp6", "ip", "ip4:icmp", "ip6:58", "TCP", "Tcp4", " tcp", "tcp ", "tcp\x00", "tcp7"}, func(string) string {
			return "connections to non-TCP networks are not allowed by this server's network policy"
		}},
	}
	for _, g := range groups {
		for _, network := range g.networks {
			for _, addr := range []string{"192.0.2.10:3306", "db.example.com:3306", "not-an-address"} {
				name := fmt.Sprintf("%q %s", network, addr)
				p := mustPolicy(t, nil, nil)
				f := fake(p, answer("192.0.2.10"))
				err := tryDial(t, p, network, addr)
				var de *DeniedError
				if want := g.want(addr); !errors.As(err, &de) || err.Error() != want {
					t.Errorf("%s: %v, want %q", name, err, want)
				}
				f.check(t, name, nil, nil)
			}
		}
	}
}

func TestDialAcceptsTCPNetworks(t *testing.T) {
	for _, network := range []string{"tcp", "tcp4", "tcp6"} {
		p := mustPolicy(t, nil, nil)
		f := fake(p, nil)
		checkErr(t, network, tryDial(t, p, network, "192.0.2.10:3306"), nil)
		f.check(t, network, nil, []string{network + " 192.0.2.10:3306"})
	}
}

// A denied IP literal is refused with the host as written, without a lookup
// or a transport call.
func TestDialRefusesDeniedLiterals(t *testing.T) {
	cases := []struct{ addr, target string }{
		{"127.0.0.1:5432", "127.0.0.1"},
		{"127.1:5432", "127.1"},
		{"[::1]:5432", "::1"},
		{"[::ffff:127.0.0.1]:5432", "::ffff:127.0.0.1"},
		{"[::1%lo0]:5432", "::1%lo0"},
		{"169.254.169.254:80", "169.254.169.254"},
		{"[64:ff9b::7f00:1]:5432", "64:ff9b::7f00:1"},
		{"[64:ff9b::127.0.0.1]:5432", "64:ff9b::127.0.0.1"},
		{"[::7f00:1]:5432", "::7f00:1"},
		{"[::127.0.0.1]:5432", "::127.0.0.1"},
		{"[64:ff9b:1::a9fe:a9fe]:80", "64:ff9b:1::a9fe:a9fe"},
		{"[64:ff9b::7f00:1%eth0]:5432", "64:ff9b::7f00:1%eth0"},
	}
	for _, c := range cases {
		p := mustPolicy(t, nil, nil)
		f := fake(p, nil)
		checkErr(t, c.addr, tryDial(t, p, "tcp", c.addr), &DeniedError{Target: c.target})
		f.check(t, c.addr, nil, nil)
	}
}

// A name whose only answer is denied is refused with the name, after one
// lookup and without a transport call.
func TestDialRefusesDeniedNames(t *testing.T) {
	for _, host := range []string{"localhost", "evil.example.com"} {
		p := mustPolicy(t, nil, nil)
		f := fake(p, answer("127.0.0.1"))
		checkErr(t, host, tryDial(t, p, "tcp", host+":5432"), &DeniedError{Target: host})
		f.check(t, host, []string{host}, nil)
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

// Every answer for a name is checked. Denied and invalid answers are
// skipped, and the allowed ones are dialled in order until one connects.
func TestDialDNSAnswers(t *testing.T) {
	const host = "db.example.com"
	errLookup := errors.New("lookup failed")
	cases := []struct {
		name    string
		answers []netip.Addr
		err     error    // the lookup error
		fail    []string // transport addresses that fail
		want    error
		text    string // the error text, when set
		dials   []string
	}{
		{name: "all denied", answers: addrs("169.254.169.254", "127.0.0.1", "64:ff9b::7f00:1"),
			want: &DeniedError{Target: host}, text: "connections to db.example.com are not allowed by this server's network policy"},
		{name: "empty answer", answers: []netip.Addr{},
			want: &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}, text: "lookup db.example.com: no such host"},
		{name: "lookup error", err: errLookup, want: errLookup},
		{name: "mixed", answers: addrs("169.254.169.254", "192.0.2.10"), dials: []string{"tcp 192.0.2.10:5432"}},
		{name: "DNS64", answers: addrs("64:ff9b::c000:20a", "192.0.2.10"), dials: []string{"tcp 192.0.2.10:5432"}},
		{name: "first allowed answer connects", answers: addrs("192.0.2.10", "192.0.2.11"), dials: []string{"tcp 192.0.2.10:5432"}},
		{name: "fallback", answers: addrs("192.0.2.10", "127.0.0.1", "192.0.2.11"), fail: []string{"192.0.2.10:5432"},
			dials: []string{"tcp 192.0.2.10:5432", "tcp 192.0.2.11:5432"}},
		{name: "allowed answer fails", answers: addrs("192.0.2.10", "127.0.0.1"), fail: []string{"192.0.2.10:5432"},
			want: errDial, dials: []string{"tcp 192.0.2.10:5432"}},
		{name: "every allowed answer fails", answers: addrs("192.0.2.10", "192.0.2.11"), fail: []string{"192.0.2.10:5432", "192.0.2.11:5432"},
			want: errDial, text: "dial failed: 192.0.2.11:5432", dials: []string{"tcp 192.0.2.10:5432", "tcp 192.0.2.11:5432"}},
		{name: "mapped", answers: addrs("::ffff:127.0.0.1", "::ffff:192.0.2.10"), dials: []string{"tcp 192.0.2.10:5432"}},
		{name: "invalid answer", answers: []netip.Addr{{}, netip.MustParseAddr("192.0.2.10")}, dials: []string{"tcp 192.0.2.10:5432"}},
		{name: "only an invalid answer", answers: []netip.Addr{{}}, want: &DeniedError{Target: host}},
	}
	for _, c := range cases {
		p := mustPolicy(t, nil, nil)
		f := fake(p, func(string) ([]netip.Addr, error) { return c.answers, c.err }, c.fail...)
		err := tryDial(t, p, "tcp", host+":5432")
		checkErr(t, c.name, err, c.want)
		if c.text != "" && (err == nil || err.Error() != c.text) {
			t.Errorf("%s: %v, want %q", c.name, err, c.text)
		}
		f.check(t, c.name, []string{host}, c.dials)
		f.checkPinned(t, c.name, p, host)
	}
}

// DialContext dials the addresses it checked, never the host as given, and
// looks a name up once even when it falls back to a later answer.
func TestDialPinsCheckedAddresses(t *testing.T) {
	p := mustPolicy(t, nil, nil)
	calls := 0
	f := fake(p, func(string) ([]netip.Addr, error) {
		calls++
		if calls == 1 {
			return addrs("192.0.2.10", "192.0.2.11"), nil
		}
		return addrs("127.0.0.1"), nil
	}, "192.0.2.10:5432")
	checkErr(t, "name", tryDial(t, p, "tcp", "db.example.com:5432"), nil)
	f.check(t, "name", []string{"db.example.com"}, []string{"tcp 192.0.2.10:5432", "tcp 192.0.2.11:5432"})
	f.checkPinned(t, "name", p, "db.example.com")

	f = fake(p, nil)
	checkErr(t, "shorthand literal", tryDial(t, p, "tcp", "192.0.522:5432"), nil)
	f.check(t, "shorthand literal", nil, []string{"tcp 192.0.2.10:5432"})
}

// IPv4-mapped ranges (::ffff:a.b.c.d/n) apply to the IPv4 addresses they
// map, since Allowed compares unmapped addresses.
func TestIPv4MappedRanges(t *testing.T) {
	deny := mustPolicy(t, nil, []string{"::ffff:10.0.0.0/104"})
	for _, ip := range []string{"10.1.2.3", "::ffff:10.1.2.3"} {
		if deny.Allowed(netip.MustParseAddr(ip)) {
			t.Errorf("mapped deny range does not cover %s", ip)
		}
	}
	if !deny.Allowed(netip.MustParseAddr("11.0.0.1")) {
		t.Error("mapped deny range is too wide")
	}
	allow := mustPolicy(t, []string{"::ffff:127.0.0.1/128"}, nil)
	if !allow.Allowed(netip.MustParseAddr("127.0.0.1")) || allow.Allowed(netip.MustParseAddr("127.0.0.2")) {
		t.Error("mapped allow range does not apply to its IPv4 address")
	}
	for _, cidr := range []string{"::ffff:0:0/95", "::ffff:10.0.0.0/64"} {
		if _, err := New(nil, []string{cidr}); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: invalid CIDR \""+cidr+"\"") {
			t.Errorf("%s: %v", cidr, err)
		}
	}
}

func TestAllowedRefusesInvalidAddress(t *testing.T) {
	if mustPolicy(t, nil, nil).Allowed(netip.Addr{}) {
		t.Fatal("the zero Addr is allowed")
	}
}
