package testutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// TLSFixture is a throwaway CA, a server certificate for localhost and
// 127.0.0.1, and a client certificate, all PEM-encoded.
type TLSFixture struct {
	CA         string
	ServerCert string
	ServerKey  string
	ClientCert string
	ClientKey  string
}

func pemOf(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

// NewTLSFixture generates a fresh fixture.
func NewTLSFixture(t testing.TB) *TLSFixture {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "qh test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leaf := func(serial int64, cn string, usage x509.ExtKeyUsage, dns []string, ips []net.IP) (string, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
			DNSNames: dns, IPAddresses: ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kb, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pemOf("CERTIFICATE", der), pemOf("EC PRIVATE KEY", kb)
	}
	f := &TLSFixture{CA: pemOf("CERTIFICATE", caDER)}
	f.ServerCert, f.ServerKey = leaf(2, "localhost", x509.ExtKeyUsageServerAuth, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	f.ClientCert, f.ClientKey = leaf(3, "qh-client", x509.ExtKeyUsageClientAuth, nil, nil)
	return f
}

// TLSServer listens on 127.0.0.1 with the fixture's server certificate. For
// each connection whose handshake succeeds it sends the number of client
// certificates the client presented, then closes the connection.
func (f *TLSFixture) TLSServer(t testing.TB) (addr string, clientCerts <-chan int) {
	t.Helper()
	pair, err := tls.X509KeyPair([]byte(f.ServerCert), []byte(f.ServerKey))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(f.CA))
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan int, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tc := c.(*tls.Conn)
			if tc.Handshake() == nil {
				ch <- len(tc.ConnectionState().PeerCertificates)
			}
			c.Close()
		}
	}()
	return ln.Addr().String(), ch
}
