// Package tunnel reaches databases through an SSH bastion: one SSH client
// per pool, one direct-tcpip channel per database connection.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
)

// Config describes a tunnel. Credentials are already resolved.
type Config struct {
	Addr       string // bastion host:port
	User       string
	HostKey    string // SHA256 fingerprint; "" rejects every key, reporting the one presented
	PrivateKey string // PEM; empty for password authentication
	Passphrase string
	Password   string
	// DialBastion opens the TCP connection to the bastion (server mode: the
	// netguard policy). Nil uses a plain dialer.
	DialBastion func(ctx context.Context, network, addr string) (net.Conn, error)
	// CheckTarget, when set (server mode), must allow every address the
	// target resolves to, except loopback (the bastion's own). The bastion
	// is then sent the checked IP, never the name.
	CheckTarget func(netip.Addr) bool
	KeepAlive   time.Duration // default 30s; three missed replies close the client
}

// Tunnel is safe for concurrent use.
type Tunnel struct {
	cfg    Config
	ccfg   *ssh.ClientConfig
	mu     sync.Mutex
	client *ssh.Client
	closed bool
	stop   chan struct{}
}

type hostKeyError struct{ got string }

func (e *hostKeyError) Error() string { return "host key " + e.got }

// New prepares a tunnel without connecting.
func New(cfg Config) (*Tunnel, error) {
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.DialBastion == nil {
		var d net.Dialer
		cfg.DialBastion = d.DialContext
	}
	var auth ssh.AuthMethod
	if cfg.PrivateKey != "" {
		var key any
		var err error
		if cfg.Passphrase != "" {
			key, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			key, err = ssh.ParseRawPrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, qerr.New(qerr.ConnectionFailed, "ssh: the private key cannot be read (wrong passphrase or not a PEM private key)")
		}
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			return nil, qerr.New(qerr.ConnectionFailed, "ssh: unsupported private key type")
		}
		auth = ssh.PublicKeys(signer)
	} else {
		auth = ssh.Password(cfg.Password)
	}
	t := &Tunnel{cfg: cfg, stop: make(chan struct{})}
	t.ccfg = &ssh.ClientConfig{
		User: cfg.User, Auth: []ssh.AuthMethod{auth}, Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(key); got != cfg.HostKey {
				return &hostKeyError{got: got}
			}
			return nil
		},
	}
	return t, nil
}

func (t *Tunnel) failed(msg string) *qerr.Error { return qerr.New(qerr.ConnectionFailed, "ssh: "+msg) }

// Connect opens the SSH connection now, so errors name the failing stage.
func (t *Tunnel) Connect(ctx context.Context) error {
	_, err := t.get(ctx)
	return err
}

func (t *Tunnel) get(ctx context.Context) (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, t.failed("the tunnel is closed")
	}
	if t.client != nil {
		return t.client, nil
	}
	nc, err := t.cfg.DialBastion(ctx, "tcp", t.cfg.Addr)
	if err != nil {
		var de *netguard.DeniedError
		if errors.As(err, &de) {
			return nil, err
		}
		return nil, t.failed("cannot reach the bastion " + t.cfg.Addr + ": " + err.Error())
	}
	// ClientConfig.Timeout does not cover NewClientConn, so bound the
	// handshake here when the caller gave no deadline.
	dl, ok := ctx.Deadline()
	if !ok {
		dl = time.Now().Add(t.ccfg.Timeout)
	}
	nc.SetDeadline(dl)
	c, chans, reqs, err := ssh.NewClientConn(nc, t.cfg.Addr, t.ccfg)
	if err != nil {
		nc.Close()
		return nil, t.handshakeErr(err)
	}
	nc.SetDeadline(time.Time{})
	t.client = ssh.NewClient(c, chans, reqs)
	go t.keepAlive(t.client)
	return t.client, nil
}

func (t *Tunnel) handshakeErr(err error) error {
	var hk *hostKeyError
	switch {
	case errors.As(err, &hk) && t.cfg.HostKey == "":
		host, port, _ := net.SplitHostPort(t.cfg.Addr)
		return t.failed(fmt.Sprintf("unknown host key for %s: it presented %s", t.cfg.Addr, hk.got)).
			WithHint(fmt.Sprintf("Verify it (ssh-keyscan -p %s %s | ssh-keygen -lf -), then set it with --ssh-host-key %s", port, host, hk.got))
	case errors.As(err, &hk):
		return t.failed(fmt.Sprintf("host key mismatch for %s (got %s, expected %s)", t.cfg.Addr, hk.got, t.cfg.HostKey)).
			WithHint("If the bastion's key really changed, verify the new one and edit the connection")
	case strings.Contains(err.Error(), "unable to authenticate"):
		return t.failed(fmt.Sprintf("authentication failed for %s@%s", t.cfg.User, t.cfg.Addr))
	}
	return t.failed("handshake with the bastion " + t.cfg.Addr + " failed")
}

func (t *Tunnel) drop(c *ssh.Client) {
	t.mu.Lock()
	if t.client == c {
		t.client = nil
	}
	t.mu.Unlock()
	c.Close()
}

func (t *Tunnel) keepAlive(c *ssh.Client) {
	tick := time.NewTicker(t.cfg.KeepAlive)
	defer tick.Stop()
	gone := make(chan struct{})
	go func() { c.Wait(); close(gone) }()
	missed := 0
	for {
		select {
		case <-t.stop:
			return
		case <-gone:
			t.drop(c)
			return
		case <-tick.C:
			reply := make(chan error, 1)
			go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); reply <- err }()
			select {
			case err := <-reply:
				if err != nil {
					missed++
				} else {
					missed = 0
				}
			case <-time.After(t.cfg.KeepAlive):
				missed++
			}
			if missed >= 3 {
				t.drop(c)
				return
			}
		}
	}
}

// target applies the server-mode policy to addr and returns what the
// bastion is asked to reach.
func (t *Tunnel) target(ctx context.Context, addr string) (string, error) {
	if t.cfg.CheckTarget == nil {
		return addr, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", t.failed("invalid database address " + addr)
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else if ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil || len(ips) == 0 {
		return "", t.failed("cannot resolve " + host + " to check it against this server's network policy").
			WithHint("Use the database's IP address")
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsLoopback() && !t.cfg.CheckTarget(ip) {
			return "", &netguard.DeniedError{Target: addr}
		}
	}
	return net.JoinHostPort(ips[0].Unmap().String(), port), nil
}

// Dial opens a connection to addr through the bastion. A dropped SSH
// connection is reopened once.
func (t *Tunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	target, err := t.target(ctx, addr)
	if err != nil {
		return nil, err
	}
	var last error
	for range 2 {
		c, err := t.get(ctx)
		if err != nil {
			return nil, err
		}
		conn, err := c.DialContext(ctx, "tcp", target)
		if err == nil {
			return conn, nil
		}
		var oce *ssh.OpenChannelError
		if errors.As(err, &oce) {
			return nil, t.failed("the bastion could not reach " + addr + ": " + oce.Message)
		}
		if ctx.Err() != nil {
			return nil, err
		}
		last = err
		t.drop(c)
	}
	return nil, t.failed("the connection to the bastion was lost: " + last.Error())
}

// Close closes the SSH connection; Dial fails afterwards.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.stop)
	if t.client != nil {
		t.client.Close()
		t.client = nil
	}
	return nil
}
