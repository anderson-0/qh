package engine

import (
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secretref"
	"github.com/anderson-0/qh/internal/sshkey"
)

// SSH is a connection's tunnel through a bastion host. The private key,
// passphrase and password are secret: they are sealed with the connection
// string and never shown. The *Ref fields are secret references resolved
// when the pool opens.
type SSH struct {
	Host          string `json:"host"`
	Port          int    `json:"port,omitempty"`
	User          string `json:"user"`
	HostKey       string `json:"host_key"`
	PrivateKey    string `json:"private_key,omitempty"`
	PrivateKeyRef string `json:"private_key_ref,omitempty"`
	Passphrase    string `json:"passphrase,omitempty"`
	PassphraseRef string `json:"passphrase_ref,omitempty"`
	Password      string `json:"password,omitempty"`
	PasswordRef   string `json:"password_ref,omitempty"`
}

// SSHInfo is what may be shown about a tunnel.
type SSHInfo struct {
	Host    string            `json:"host"`
	Port    int               `json:"port"`
	User    string            `json:"user"`
	HostKey string            `json:"host_key"`
	Auth    string            `json:"auth"` // key or password
	Refs    map[string]string `json:"refs,omitempty"`
}

var hostKeyRe = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func sshInvalid(msg string) error {
	return qerr.New(qerr.Invalid, msg).
		WithHint("ssh needs host, user, host_key (SHA256:… from ssh-keygen -lf) and either a private key or a password")
}

func (s *SSH) port() int {
	if s.Port == 0 {
		return 22
	}
	return s.Port
}

// Addr is the bastion's host:port.
func (s *SSH) Addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.port())) }

// Validate checks s without connecting. An empty HostKey is allowed: the
// connection test then reports the bastion's fingerprint. Errors never
// contain key material or passwords.
func (s *SSH) Validate() error { return s.validate(false) }

// ValidateServer is Validate for a tenant's settings in server mode. It
// applies the server's key limits (KDF rounds, RSA size) and does not
// decrypt an encrypted key: the tunnel decrypts it once, when connecting,
// and reports a wrong passphrase then.
func (s *SSH) ValidateServer() error { return s.validate(true) }

func (s *SSH) validate(server bool) error {
	if strings.TrimSpace(s.Host) == "" {
		return sshInvalid("ssh.host is required")
	}
	if s.Port < 0 || s.Port > 65535 {
		return sshInvalid("ssh.port must be between 1 and 65535")
	}
	if strings.TrimSpace(s.User) == "" {
		return sshInvalid("ssh.user is required")
	}
	if s.HostKey != "" && !hostKeyRe.MatchString(s.HostKey) {
		return sshInvalid("ssh.host_key must look like SHA256:… (43 base64 characters, as printed by ssh-keygen -lf)")
	}
	for _, f := range []struct{ name, lit, ref string }{
		{"private_key", s.PrivateKey, s.PrivateKeyRef},
		{"passphrase", s.Passphrase, s.PassphraseRef},
		{"password", s.Password, s.PasswordRef},
	} {
		if f.lit != "" && f.ref != "" {
			return sshInvalid(fmt.Sprintf("ssh.%s and ssh.%s_ref cannot both be set", f.name, f.name))
		}
		if f.ref != "" {
			if _, err := secretref.Parse(f.ref); err != nil {
				return err
			}
		}
	}
	key := s.PrivateKey != "" || s.PrivateKeyRef != ""
	pw := s.Password != "" || s.PasswordRef != ""
	if key == pw {
		return sshInvalid("ssh needs exactly one of private_key (or private_key_ref) and password (or password_ref)")
	}
	if !key && (s.Passphrase != "" || s.PassphraseRef != "") {
		return sshInvalid("ssh.passphrase applies only with a private key")
	}
	if s.PrivateKey != "" {
		return checkKey(s.PrivateKey, s.Passphrase, s.PassphraseRef != "", server)
	}
	return nil
}

// checkKey parses key without decrypting it, then (outside server mode)
// decrypts it with pass. Parsing first never runs the KDF.
func checkKey(key, pass string, passRef, server bool) error {
	rounds, rsaBits := 0, 0
	if server {
		rounds, rsaBits = sshkey.ServerMaxKDFRounds, sshkey.ServerMaxRSABits
	}
	if err := sshkey.Check(key, rounds); err != nil {
		return qerr.New(qerr.Invalid, "ssh: "+err.Error())
	}
	k, err := ssh.ParseRawPrivateKey([]byte(key))
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil:
		if pass != "" {
			return sshInvalid("ssh.passphrase is set but the private key is not encrypted")
		}
		if err := sshkey.CheckParsed(k, rsaBits, server); err != nil {
			return qerr.New(qerr.Invalid, "ssh: "+err.Error())
		}
		return nil
	case errors.As(err, &missing):
		// An encrypted key is only decrypted when connecting, but its type
		// is known now: refuse DSA here rather than at the first use.
		if server && isDSA(key, missing) {
			return qerr.New(qerr.Invalid, "ssh: DSA keys are not accepted; use an Ed25519, ECDSA or RSA key")
		}
		if passRef {
			return nil
		}
		if pass == "" {
			return sshInvalid("the ssh private key is encrypted; give ssh.passphrase or ssh.passphrase_ref")
		}
		if server {
			return nil
		}
		if _, err := ssh.ParseRawPrivateKeyWithPassphrase([]byte(key), []byte(pass)); err != nil {
			return sshInvalid("ssh.passphrase does not decrypt the private key")
		}
		return nil
	}
	return sshInvalid("ssh.private_key is not a PEM private key in OpenSSH or PKCS#8 format")
}

// Info returns the displayable summary of s, or nil when s is nil.
func (s *SSH) Info() *SSHInfo {
	if s == nil {
		return nil
	}
	i := &SSHInfo{Host: s.Host, Port: s.port(), User: s.User, HostKey: s.HostKey, Auth: "password"}
	if s.PrivateKey != "" || s.PrivateKeyRef != "" {
		i.Auth = "key"
	}
	for name, ref := range map[string]string{"private_key": s.PrivateKeyRef, "passphrase": s.PassphraseRef, "password": s.PasswordRef} {
		if ref != "" {
			if i.Refs == nil {
				i.Refs = map[string]string{}
			}
			i.Refs[name] = ref
		}
	}
	return i
}

// RefStrings returns the secret references s uses, in field order.
func (s *SSH) RefStrings() []string {
	var out []string
	for _, r := range []string{s.PrivateKeyRef, s.PassphraseRef, s.PasswordRef} {
		if r != "" {
			out = append(out, r)
		}
	}
	return out
}

// isDSA reports whether an encrypted private key is a DSA key: a legacy
// "DSA PRIVATE KEY" PEM block, or an OpenSSH key whose unencrypted public
// part is ssh-dss.
func isDSA(key string, missing *ssh.PassphraseMissingError) bool {
	if b, _ := pem.Decode([]byte(key)); b != nil && b.Type == "DSA PRIVATE KEY" {
		return true
	}
	return missing.PublicKey != nil && missing.PublicKey.Type() == ssh.KeyAlgoDSA //nolint:staticcheck // recognised only to refuse it
}
