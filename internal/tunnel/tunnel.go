// Package tunnel reaches databases through an SSH bastion: one SSH client
// per pool, one direct-tcpip channel per database connection.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sshkey"
)

// Config describes a tunnel. Credentials are already resolved.
type Config struct {
	Addr       string // bastion host:port
	User       string
	HostKey    string // SHA256 fingerprint; "" rejects every key, reporting the one presented
	PrivateKey string // PEM, at most 16 KiB; empty for password authentication
	Passphrase string
	Password   string
	// MaxKDFRounds refuses an encrypted OpenSSH key whose bcrypt KDF asks
	// for more rounds, before any are run (server mode: the key is a
	// tenant's). 0 means no limit.
	MaxKDFRounds int
	// DialBastion opens the TCP connection to the bastion (server mode: the
	// netguard policy). Nil uses a plain dialer.
	DialBastion func(ctx context.Context, network, addr string) (net.Conn, error)
	// CheckTarget, when set (server mode), must allow every address the
	// target resolves to, except loopback (the bastion's own). The bastion
	// is then sent the checked IP, never the name.
	CheckTarget func(netip.Addr) bool
	// Resolve looks up a database host name for CheckTarget; nil uses the
	// system resolver. Tests replace it.
	Resolve   func(ctx context.Context, host string) ([]netip.Addr, error)
	KeepAlive time.Duration // default 30s; three missed replies close the client
}

// Tunnel is safe for concurrent use.
type Tunnel struct {
	cfg        Config
	ccfg       *ssh.ClientConfig
	mu         sync.Mutex
	client     *ssh.Client
	connecting *attempt // non-nil while one caller dials and handshakes
	closed     bool
	stop       chan struct{}
}

// attempt is one in-flight connection try shared by concurrent callers.
type attempt struct {
	done  chan struct{}
	err   error // set before done is closed
	retry bool  // the connector's own context ended, so others should try again
}

type hostKeyError struct{ typ, got string }

func (e *hostKeyError) Error() string { return "host key " + e.typ + " " + e.got }

// hostKeyAlgorithms is OpenSSH's preference order. x/crypto's default puts
// ECDSA and RSA before Ed25519, so a bastion holding several host keys would
// present a key other than the Ed25519 one a user normally pins.
//
// Plain ssh-rsa is left out on purpose: it signs with SHA-1, which OpenSSH
// has disabled by default since 8.8. An RSA host key still works, because
// it is negotiated as rsa-sha2-512 or rsa-sha2-256 (same key, SHA-2
// signatures); only bastions too old to offer those (OpenSSH before 7.2)
// cannot be reached.
var hostKeyAlgorithms = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

const handshakeTimeout = 15 * time.Second

// New prepares a tunnel without connecting.
func New(cfg Config) (*Tunnel, error) {
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.DialBastion == nil {
		d := net.Dialer{Timeout: handshakeTimeout}
		cfg.DialBastion = d.DialContext
	}
	if cfg.Resolve == nil {
		cfg.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	var auth ssh.AuthMethod
	if cfg.PrivateKey != "" {
		if err := sshkey.Check(cfg.PrivateKey, cfg.MaxKDFRounds); err != nil {
			return nil, qerr.New(qerr.ConnectionFailed, "ssh: "+err.Error())
		}
		var key any
		var err error
		if cfg.Passphrase != "" {
			key, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			key, err = ssh.ParseRawPrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			var pm *ssh.PassphraseMissingError
			if errors.As(err, &pm) {
				return nil, qerr.New(qerr.ConnectionFailed, "ssh: the private key is encrypted; give a passphrase")
			}
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
		User: cfg.User, Auth: []ssh.AuthMethod{auth}, Timeout: handshakeTimeout,
		HostKeyAlgorithms: hostKeyAlgorithms,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(key); got != cfg.HostKey {
				return &hostKeyError{typ: key.Type(), got: got}
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

// get returns the live SSH client, connecting if needed. The dial and
// handshake run outside the mutex; concurrent callers share one attempt and
// each honours its own context while waiting.
func (t *Tunnel) get(ctx context.Context) (*ssh.Client, error) {
	for {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, t.failed("the tunnel is closed")
		}
		if t.client != nil {
			c := t.client
			t.mu.Unlock()
			return c, nil
		}
		if at := t.connecting; at != nil {
			t.mu.Unlock()
			select {
			case <-at.done:
				if at.err != nil && !at.retry {
					return nil, at.err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		at := &attempt{done: make(chan struct{})}
		t.connecting = at
		t.mu.Unlock()

		c, err := t.connect(ctx)

		t.mu.Lock()
		t.connecting = nil
		if err == nil && t.closed {
			c.Close()
			c, err = nil, t.failed("the tunnel is closed")
		}
		if err == nil {
			t.client = c
			go t.keepAlive(c)
		} else {
			at.err, at.retry = err, ended(ctx)
		}
		t.mu.Unlock()
		close(at.done)
		return c, err
	}
}

// connect dials the bastion and completes the SSH handshake, bounded by ctx,
// a 15s limit and Close.
func (t *Tunnel) connect(ctx context.Context) (*ssh.Client, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-t.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	// Bound the dial whatever DialBastion is: server mode injects the
	// netguard dialer, which has no timeout of its own.
	dctx, dcancel := context.WithTimeout(ctx, handshakeTimeout)
	nc, err := t.cfg.DialBastion(dctx, "tcp", t.cfg.Addr)
	dcancel()
	if err != nil {
		if ended(ctx) {
			return nil, t.ctxErr(ctx)
		}
		var de *netguard.DeniedError
		if errors.As(err, &de) {
			return nil, err
		}
		return nil, t.failed("cannot reach the bastion " + t.cfg.Addr + ": " + err.Error())
	}
	dl := time.Now().Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	nc.SetDeadline(dl)
	// Interrupt the handshake when ctx ends. The watcher is joined before
	// the deadline is cleared so it can never expire a live connection.
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			nc.SetDeadline(time.Unix(1, 0))
		case <-done:
		}
	}()
	c, chans, reqs, err := ssh.NewClientConn(nc, t.cfg.Addr, t.ccfg)
	close(done)
	<-exited
	if err != nil {
		nc.Close()
		if ended(ctx) {
			return nil, t.ctxErr(ctx)
		}
		return nil, t.handshakeErr(err)
	}
	if ctx.Err() != nil {
		c.Close()
		return nil, t.ctxErr(ctx)
	}
	nc.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// ended reports whether ctx is done or past its deadline. Socket deadlines
// and context timers fire independently, so an I/O timeout at the deadline
// can be seen before ctx.Err is set.
func ended(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	d, ok := ctx.Deadline()
	return ok && !time.Now().Before(d)
}

// ctxErr reports why a connection attempt was cut short.
func (t *Tunnel) ctxErr(ctx context.Context) error {
	select {
	case <-t.stop:
		return t.failed("the tunnel is closed")
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

func (t *Tunnel) handshakeErr(err error) error {
	var hk *hostKeyError
	switch {
	case errors.As(err, &hk) && t.cfg.HostKey == "":
		host, port, _ := net.SplitHostPort(t.cfg.Addr)
		scan := "ssh-keyscan"
		if typ := keyscanType(hk.typ); typ != "" {
			scan += " -t " + typ
		}
		return t.failed(fmt.Sprintf("unknown host key for %s: it presented its %s key %s", t.cfg.Addr, hk.typ, hk.got)).
			WithHint(fmt.Sprintf("Verify it (%s -p %s %s | ssh-keygen -lf -), then set it with --ssh-host-key %s", scan, port, host, hk.got))
	case errors.As(err, &hk):
		return t.failed(fmt.Sprintf("host key mismatch for %s (got %s %s, expected %s)", t.cfg.Addr, hk.typ, hk.got, t.cfg.HostKey)).
			WithHint("If the bastion's key really changed, verify the new one and edit the connection")
	case strings.Contains(err.Error(), "unable to authenticate"):
		return t.failed(fmt.Sprintf("authentication failed for %s@%s", t.cfg.User, t.cfg.Addr))
	}
	return t.failed("handshake with the bastion " + t.cfg.Addr + " failed: " + err.Error())
}

// keyscanType maps the type of a presented host key to the name
// ssh-keyscan -t takes, or "" when there is none.
func keyscanType(keyType string) string {
	switch {
	case keyType == ssh.KeyAlgoED25519:
		return "ed25519"
	case strings.HasPrefix(keyType, "ecdsa-sha2-"):
		return "ecdsa"
	case keyType == ssh.KeyAlgoRSA, strings.HasPrefix(keyType, "rsa-sha2-"):
		return "rsa"
	}
	return ""
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

// splitAddr validates a database address: a non-empty host and a numeric port.
func splitAddr(addr string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return "", "", false
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return "", "", false
	}
	return host, port, true
}

// target applies the server-mode policy to host and returns what the
// bastion is asked to reach.
func (t *Tunnel) target(ctx context.Context, host, port string) (string, error) {
	if t.cfg.CheckTarget == nil {
		return net.JoinHostPort(host, port), nil
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		var lerr error
		ips, lerr = t.cfg.Resolve(ctx, host)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if lerr != nil || len(ips) == 0 {
			return "", t.failed("cannot resolve " + host + " to check it against this server's network policy").
				WithHint("Use the database's IP address")
		}
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsLoopback() && !t.cfg.CheckTarget(ip) {
			return "", &netguard.DeniedError{Target: net.JoinHostPort(host, port)}
		}
	}
	return net.JoinHostPort(ips[0].Unmap().String(), port), nil
}

// Dial opens a connection to addr through the bastion. A dropped SSH
// connection is reopened once. The returned conn supports deadlines.
func (t *Tunnel) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, t.failed("only TCP connections can go through an SSH tunnel")
	}
	host, port, ok := splitAddr(addr)
	if !ok {
		return nil, t.failed("invalid database address " + addr)
	}
	target, err := t.target(ctx, host, port)
	if err != nil {
		return nil, err
	}
	var last error
	for range 2 {
		c, err := t.get(ctx)
		if err != nil {
			return nil, err
		}
		ch, err := c.DialContext(ctx, "tcp", target)
		if err == nil {
			return newConn(ch), nil
		}
		var oce *ssh.OpenChannelError
		if errors.As(err, &oce) {
			return nil, t.failed("the bastion could not reach " + addr + ": " + oce.Message)
		}
		if ctx.Err() != nil {
			return nil, err
		}
		// The address is already validated, so this is a dead transport.
		last = err
		t.drop(c)
	}
	return nil, t.failed("the connection to the bastion was lost: " + last.Error())
}

// conn adapts an SSH channel to a deadline-capable net.Conn: x/crypto's
// channel conns reject Set*Deadline, which database drivers use to cancel
// queries. A net.Pipe (which supports deadlines) sits in front of it.
type conn struct {
	net.Conn // the pipe end handed to the caller
	ch       net.Conn
	server   net.Conn
	once     sync.Once
}

func newConn(ch net.Conn) net.Conn {
	client, server := net.Pipe()
	c := &conn{Conn: client, ch: ch, server: server}
	go func() { // channel -> caller
		io.Copy(server, ch)
		server.Close() // the caller reads EOF after the delivered data
	}()
	go func() { // caller -> channel
		io.Copy(ch, server)
		ch.Close()
	}()
	return c
}

func (c *conn) LocalAddr() net.Addr  { return c.ch.LocalAddr() }
func (c *conn) RemoteAddr() net.Addr { return c.ch.RemoteAddr() }

// Close closes both pipe ends and the channel, ending the copy goroutines.
func (c *conn) Close() error {
	var err error
	c.once.Do(func() {
		c.Conn.Close()
		c.server.Close()
		err = c.ch.Close()
	})
	return err
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
