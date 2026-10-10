package engine

import (
	"crypto/tls"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestTLSValidate(t *testing.T) {
	f := testutil.NewTLSFixture(t)
	bad := []struct {
		tls  TLS
		want string
	}{
		{TLS{}, "tls.mode is required"},
		{TLS{Mode: "prefer"}, `unknown tls.mode "prefer"`},
		{TLS{Mode: TLSDisable, CA: f.CA}, "tls.mode disable takes no other tls settings"},
		{TLS{Mode: TLSRequire, CA: f.CA}, "tls.ca and tls.server_name need mode verify-ca or verify-full"},
		{TLS{Mode: TLSRequire, ServerName: "db.internal"}, "tls.ca and tls.server_name need mode verify-ca or verify-full"},
		{TLS{Mode: TLSRequire, Cert: f.ClientCert}, "tls.cert and tls.key must be given together"},
		{TLS{Mode: TLSVerifyFull, CA: "not pem"}, "tls.ca contains no PEM certificate"},
		{TLS{Mode: TLSRequire, Cert: f.ClientCert, Key: f.ServerKey}, "tls.cert and tls.key are not a valid PEM certificate and key pair"},
	}
	for _, c := range bad {
		err := c.tls.Validate()
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: "+c.want) {
			t.Errorf("%q: got %v, want %q", c.tls.Mode, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("error leaks key material: %v", err)
		}
	}
	good := []TLS{
		{Mode: TLSDisable},
		{Mode: TLSRequire},
		{Mode: TLSRequire, Cert: f.ClientCert, Key: f.ClientKey},
		{Mode: TLSVerifyCA, CA: f.CA},
		{Mode: TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey, ServerName: "db.internal"},
	}
	for _, g := range good {
		if err := g.Validate(); err != nil {
			t.Errorf("%s: %v", g.Mode, err)
		}
	}
}

func TestTLSInfo(t *testing.T) {
	f := testutil.NewTLSFixture(t)
	got := (&TLS{Mode: TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey, ServerName: "db"}).Info()
	if *got != (TLSInfo{Mode: TLSVerifyFull, CA: true, ClientCert: true, ServerName: "db"}) {
		t.Fatalf("%+v", got)
	}
	if (*TLS)(nil).Info() != nil {
		t.Fatal("nil TLS has info")
	}
}

func dialTLS(addr string, cfg *tls.Config) error {
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	return c.Close()
}

func TestTLSConfigModes(t *testing.T) {
	f, other := testutil.NewTLSFixture(t), testutil.NewTLSFixture(t)
	addr, certs := f.TLSServer(t)
	cfg := func(tl TLS, host string) *tls.Config {
		t.Helper()
		c, err := tl.Config(host)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ok := func(name string, c *tls.Config) int {
		t.Helper()
		if err := dialTLS(addr, c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return <-certs
	}
	fails := func(name string, c *tls.Config) {
		t.Helper()
		if dialTLS(addr, c) == nil {
			t.Fatalf("%s: connected", name)
		}
	}
	ok("require ignores the certificate", cfg(TLS{Mode: TLSRequire}, "anything.example"))
	ok("verify-full with the right CA and name", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA}, "localhost"))
	fails("verify-full with a wrong name", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA}, "db.example.com"))
	fails("verify-full with a wrong CA", cfg(TLS{Mode: TLSVerifyFull, CA: other.CA}, "localhost"))
	ok("server_name overrides the host", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA, ServerName: "localhost"}, "10.0.0.1"))
	ok("verify-ca ignores the name", cfg(TLS{Mode: TLSVerifyCA, CA: f.CA}, "db.example.com"))
	fails("verify-ca with a wrong CA", cfg(TLS{Mode: TLSVerifyCA, CA: other.CA}, "localhost"))
	if n := ok("client certificate", cfg(TLS{Mode: TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey}, "localhost")); n != 1 {
		t.Fatalf("server saw %d client certificates", n)
	}
	if c, err := (&TLS{Mode: TLSDisable}).Config("h"); c != nil || err != nil {
		t.Fatalf("disable: %v %v", c, err)
	}
	if c := cfg(TLS{Mode: TLSRequire}, "h"); c.MinVersion != tls.VersionTLS12 {
		t.Fatalf("min version %x", c.MinVersion)
	}
}
