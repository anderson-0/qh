// Package netguard decides which database addresses a qh server may dial.
// It checks the resolved IP and dials exactly that IP, so DNS tricks cannot
// redirect a connection to a denied address after the check.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// DefaultDeny covers loopback, unspecified, link-local (including cloud
// metadata at 169.254.169.254), the AWS and Alibaba metadata addresses, and
// three IPv6 ranges that embed IPv4 addresses: the NAT64 prefixes
// 64:ff9b::/96 and 64:ff9b:1::/48 and the IPv4-compatible ::/96. The
// embedded address is not interpreted; to reach one, allow its IPv6 range.
var DefaultDeny = []string{
	"127.0.0.0/8", "::1/128", "0.0.0.0/8", "::/128",
	"169.254.0.0/16", "fe80::/10", "fd00:ec2::254/128", "100.100.100.200/32",
	"64:ff9b::/96", "64:ff9b:1::/48", "::/96",
}

// Policy allows an address when it is inside an allow range, or outside
// every deny range.
type Policy struct {
	allow, deny []netip.Prefix
	// Resolve looks up a host name; tests replace it.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// dial opens the transport connection to a checked IP; tests replace it.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DeniedError reports a refused target.
type DeniedError struct{ Target string }

func (e *DeniedError) Error() string {
	return "connections to " + e.Target + " are not allowed by this server's network policy"
}

func parse(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, qerr.New(qerr.Invalid, fmt.Sprintf("invalid CIDR %q", s))
		}
		// Allowed compares unmapped addresses, so an IPv4-mapped range
		// (::ffff:10.0.0.0/104) must become the IPv4 range it maps.
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return nil, qerr.New(qerr.Invalid, fmt.Sprintf("invalid CIDR %q", s)).
					WithHint("An IPv4-mapped range needs a prefix length of at least 96; write the IPv4 range instead")
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// New builds a policy from allow ranges and extra deny ranges.
func New(allow, deny []string) (*Policy, error) {
	a, err := parse(allow)
	if err != nil {
		return nil, err
	}
	d, err := parse(append(append([]string{}, DefaultDeny...), deny...))
	if err != nil {
		return nil, err
	}
	return &Policy{
		allow: a, deny: d,
		Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial: (&net.Dialer{}).DialContext,
	}, nil
}

// Allowed reports whether ip may be dialled.
func (p *Policy) Allowed(ip netip.Addr) bool {
	// Drop the IPv6 zone (fe80::1%en0): netip.Prefix.Contains never matches a
	// zoned address, so it would otherwise slip past every range.
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap().WithZone("")
	for _, r := range p.allow {
		if r.Contains(ip) {
			return true
		}
	}
	for _, r := range p.deny {
		if r.Contains(ip) {
			return false
		}
	}
	return true
}

// DialContext dials addr through the policy. Only TCP (tcp, tcp4, tcp6) is
// accepted: any other network is refused before addr is parsed or looked
// up. A host name is looked up once, and the answers the policy allows are
// dialled in order, as IP addresses. Every refusal is a *DeniedError; an
// empty answer is a not-found *net.DNSError; if every allowed answer fails,
// the last dial error is returned.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		if strings.HasPrefix(network, "unix") {
			return nil, &DeniedError{Target: "unix socket " + addr}
		}
		return nil, &DeniedError{Target: "non-TCP networks"}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ip4, ok := legacyIPv4(host); ok {
		ips = []netip.Addr{ip4}
	} else if ips, err = p.Resolve(ctx, host); err != nil {
		return nil, err
	} else if len(ips) == 0 {
		// No address is not a refusal: report it as the resolver does.
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var lastErr error = &DeniedError{Target: host}
	for _, ip := range ips {
		if !p.Allowed(ip) {
			continue
		}
		c, err := p.dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// legacyIPv4 parses shorthand IPv4 forms such as 127.1, which some resolvers
// accept, so they are checked as addresses rather than looked up.
func legacyIPv4(host string) (netip.Addr, bool) {
	parts := strings.Split(host, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return netip.Addr{}, false
	}
	var nums []uint64
	for _, s := range parts {
		var n uint64
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil || fmt.Sprint(n) != s {
			return netip.Addr{}, false
		}
		nums = append(nums, n)
	}
	var v uint32
	switch len(nums) {
	case 2: // a.b → a.0.0.b
		if nums[0] > 255 || nums[1] > 0xffffff {
			return netip.Addr{}, false
		}
		v = uint32(nums[0])<<24 | uint32(nums[1])
	case 3: // a.b.c → a.b.0.c
		if nums[0] > 255 || nums[1] > 255 || nums[2] > 0xffff {
			return netip.Addr{}, false
		}
		v = uint32(nums[0])<<24 | uint32(nums[1])<<16 | uint32(nums[2])
	}
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}
