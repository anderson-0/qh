// Package sshtest runs an in-process SSH server that forwards direct-tcpip
// channels, for tunnel tests.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Options configures the server. Exactly one of Password and AuthorizedKey
// is normally set. Forward dials a requested target; nil uses net.Dial.
type Options struct {
	User          string
	Password      string
	AuthorizedKey ssh.PublicKey
	Forward       func(target string) (net.Conn, error)
}

// Server is a running test SSH server.
type Server struct {
	Addr        string
	Fingerprint string
	mu          sync.Mutex
	targets     []string
	conns       map[*ssh.ServerConn]bool
	accepted    int
}

// Key returns a new ed25519 private key as PEM (encrypted when passphrase is
// set) and its public key.
func Key(t testing.TB, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var b *pem.Block
	if passphrase == "" {
		b, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		b, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(b)), sp
}

// Start listens on 127.0.0.1 and serves until the test ends.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{}
	if o.Password != "" {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == o.User && string(pw) == o.Password {
				return nil, nil
			}
			return nil, io.EOF
		}
	}
	if o.AuthorizedKey != nil {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == o.User && string(k.Marshal()) == string(o.AuthorizedKey.Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		}
	}
	cfg.AddHostKey(signer)
	forward := o.Forward
	if forward == nil {
		forward = func(target string) (net.Conn, error) { return net.Dial("tcp", target) }
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), conns: map[*ssh.ServerConn]bool{}}
	t.Cleanup(func() { ln.Close(); s.DropClients() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(nc, cfg, forward)
		}
	}()
	return s
}

func (s *Server) serve(nc net.Conn, cfg *ssh.ServerConfig, forward func(string) (net.Conn, error)) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	s.mu.Lock()
	s.conns[sc] = true
	s.accepted++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.conns, sc); s.mu.Unlock() }()
	go func() {
		for r := range reqs {
			if r.WantReply {
				r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nch := range chans {
		if nch.ChannelType() != "direct-tcpip" {
			nch.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
			nch.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		target := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
		s.mu.Lock()
		s.targets = append(s.targets, target)
		s.mu.Unlock()
		up, err := forward(target)
		if err != nil {
			nch.Reject(ssh.ConnectionFailed, "connection refused")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			up.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { io.Copy(ch, up); ch.CloseWrite(); ch.Close() }()
		go func() { io.Copy(up, ch); up.Close() }()
	}
	sc.Wait()
}

// Targets returns the targets clients asked the server to reach.
func (s *Server) Targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

// Conns returns how many SSH connections were accepted in total.
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// DropClients closes every open SSH connection (a bastion restart).
func (s *Server) DropClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}
