package engine

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/anderson-0/qh/internal/qerr"
)

// TLS modes.
const (
	TLSDisable    = "disable"     // plain connection
	TLSRequire    = "require"     // encrypted; the certificate is not checked
	TLSVerifyCA   = "verify-ca"   // the chain must verify against CA (or the system roots)
	TLSVerifyFull = "verify-full" // verify-ca, and the certificate must name the host or ServerName
)

// TLS is a connection's TLS settings. When set, it replaces any TLS settings
// in the connection string. Key is secret: it is sealed with the connection
// string and never shown.
type TLS struct {
	Mode       string `json:"mode"`
	CA         string `json:"ca,omitempty"`   // PEM certificates to trust instead of the system roots
	Cert       string `json:"cert,omitempty"` // PEM client certificate
	Key        string `json:"key,omitempty"`  // PEM private key for Cert
	ServerName string `json:"server_name,omitempty"`
}

// TLSInfo is what may be shown about a connection's TLS settings.
type TLSInfo struct {
	Mode       string `json:"mode"`
	CA         bool   `json:"ca,omitempty"`
	ClientCert bool   `json:"client_cert,omitempty"`
	ServerName string `json:"server_name,omitempty"`
}

// Info returns the displayable summary of t, or nil when t is nil.
func (t *TLS) Info() *TLSInfo {
	if t == nil {
		return nil
	}
	return &TLSInfo{Mode: t.Mode, CA: t.CA != "", ClientCert: t.Cert != "", ServerName: t.ServerName}
}

func tlsInvalid(msg string) error {
	return qerr.New(qerr.Invalid, msg).
		WithHint("tls.mode is disable, require, verify-ca or verify-full; tls.ca, tls.cert and tls.key are PEM text")
}

// Validate checks t without connecting. Its errors never contain key material.
func (t *TLS) Validate() error {
	switch t.Mode {
	case TLSDisable, TLSRequire, TLSVerifyCA, TLSVerifyFull:
	case "":
		return tlsInvalid("tls.mode is required")
	default:
		return tlsInvalid(fmt.Sprintf("unknown tls.mode %q", t.Mode))
	}
	if t.Mode == TLSDisable && (t.CA != "" || t.Cert != "" || t.Key != "" || t.ServerName != "") {
		return tlsInvalid("tls.mode disable takes no other tls settings")
	}
	if t.Mode == TLSRequire && (t.CA != "" || t.ServerName != "") {
		return tlsInvalid("tls.ca and tls.server_name need mode verify-ca or verify-full; require does not check the certificate")
	}
	if (t.Cert == "") != (t.Key == "") {
		return tlsInvalid("tls.cert and tls.key must be given together")
	}
	_, err := t.Config("validate.invalid")
	return err
}

// Config builds the crypto/tls configuration for connecting to host. It
// returns nil for mode disable. Errors never contain key material.
func (t *TLS) Config(host string) (*tls.Config, error) {
	if t.Mode == TLSDisable {
		return nil, nil
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if t.ServerName != "" {
		c.ServerName = t.ServerName
	}
	if t.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.CA)) {
			return nil, tlsInvalid("tls.ca contains no PEM certificate")
		}
		c.RootCAs = pool
	}
	if t.Cert != "" {
		pair, err := tls.X509KeyPair([]byte(t.Cert), []byte(t.Key))
		if err != nil {
			return nil, tlsInvalid("tls.cert and tls.key are not a valid PEM certificate and key pair")
		}
		c.Certificates = []tls.Certificate{pair}
	}
	switch t.Mode {
	case TLSRequire:
		c.InsecureSkipVerify = true
	case TLSVerifyCA:
		// crypto/tls checks the chain and the name together, or neither, so
		// skip its check and verify the chain alone.
		c.InsecureSkipVerify = true
		roots := c.RootCAs
		c.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error { return verifyChain(raw, roots) }
	}
	return c, nil
}

// verifyChain checks that the server's certificate chains to roots (the
// system roots when nil), without checking its name.
func verifyChain(raw [][]byte, roots *x509.CertPool) error {
	if len(raw) == 0 {
		return errors.New("tls: the server sent no certificate")
	}
	certs := make([]*x509.Certificate, len(raw))
	for i, b := range raw {
		c, err := x509.ParseCertificate(b)
		if err != nil {
			return err
		}
		certs[i] = c
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
	return err
}
