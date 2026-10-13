package mongodb

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func TestInspect(t *testing.T) {
	info, err := Engine{}.Inspect("mongodb://bob:s3cret@h1:27017,h2:27018/shop?replicaSet=rs0&authSource=admin")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "h1,h2" || info.Port != 27017 || info.Database != "shop" || info.User != "bob" || info.Password != "s3cret" {
		t.Fatalf("info = %+v", info)
	}
	if info.Redacted != "mongodb://bob:***@h1:27017,h2:27018/shop?replicaSet=rs0&authSource=admin" {
		t.Fatalf("redacted = %q", info.Redacted)
	}
	info, err = Engine{}.Inspect("mongodb+srv://u:p%40ss@cluster0.example.net/app")
	if err != nil || info.Host != "cluster0.example.net" || info.Port != 0 || info.Password != "p@ss" || info.Database != "app" {
		t.Fatalf("srv: %+v %v", info, err)
	}
	info, err = Engine{}.Inspect("mongodb://localhost")
	if err != nil || info.Port != 27017 || info.Database != "" || info.Redacted != "mongodb://localhost" {
		t.Fatalf("plain: %+v %v", info, err)
	}
	for _, bad := range []string{"postgres://x/y", "mongodb://", "mongodb://h:notaport/x", "mongodb://h/x?%zz"} {
		if _, err := (Engine{}).Inspect(bad); err == nil || qerr.From(err).Category != qerr.Invalid || strings.Contains(err.Error(), bad) {
			t.Errorf("Inspect(%q) = %v", bad, err)
		}
	}
}

func TestCheckServerDSN(t *testing.T) {
	ok := []string{"mongodb://u:p@h/db?tls=true&authSource=admin", "mongodb+srv://u:p@c.example.net/db?retryWrites=true"}
	for _, d := range ok {
		if err := (Engine{}).CheckServerDSN(d); err != nil {
			t.Errorf("CheckServerDSN(%q) = %v", d, err)
		}
	}
	refused := []string{
		"mongodb://h/db?tlsCAFile=/etc/ssl/ca.pem",
		"mongodb://h/db?TLSCertificateKeyFile=/k.pem",
		"mongodb://h/db?tlsCertificateKeyFilePassword=x",
		"mongodb://h/db?tlsInsecure=true",
		"mongodb://h/db?authMechanism=MONGODB-AWS",
		"mongodb://u@h/db?authMechanism=GSSAPI",
		"mongodb://h/db?authMechanism=mongodb-oidc",
	}
	for _, d := range refused {
		err := (Engine{}).CheckServerDSN(d)
		if err == nil || qerr.From(err).Category != qerr.Invalid || strings.Contains(err.Error(), "/etc/ssl") || strings.Contains(err.Error(), d) {
			t.Errorf("CheckServerDSN(%q) = %v", d, err)
		}
	}
}

func TestBuildDSN(t *testing.T) {
	got, err := Engine{}.BuildDSN(engine.DSNFields{Host: "db.example.com", Port: 27017, User: "app", Password: "p w", Database: "shop"})
	if err != nil || !strings.HasPrefix(got, "mongodb://app:") || !strings.HasSuffix(got, "@db.example.com:27017/shop") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestEngineClassify(t *testing.T) {
	for q, want := range map[string]engine.Access{
		`db.a.find()`:                     engine.Read,
		`db.a.deleteMany({})`:             engine.Write,
		`db.a.createIndex({x: 1})`:        engine.DDL,
		`db.a.aggregate([{$merge: "b"}])`: engine.Write,
	} {
		if got, err := (Engine{}).Classify(q); err != nil || got != want {
			t.Errorf("Classify(%q) = %v %v", q, got, err)
		}
	}
	if _, err := (Engine{}).Classify(`db.a.findd()`); qerr.From(err).Category != qerr.Invalid {
		t.Errorf("bad method: %v", err)
	}
	if got := (Engine{}).ReferencedObjects(`db.orders.find({a: 1})`); len(got) != 1 || got[0] != "orders" {
		t.Errorf("ReferencedObjects = %v", got)
	}
}

// The driver also reads files for the ssl* aliases and for
// tlsCertificateFile/tlsPrivateKeyFile, unescapes option keys, and splits
// options on ";" as well as "&".
func TestCheckServerDSNAliases(t *testing.T) {
	for _, d := range []string{
		"mongodb://h/db?sslCertificateAuthorityFile=/ca.pem",
		"mongodb://h/db?sslClientCertificateKeyFile=/k.pem",
		"mongodb://h/db?sslClientCertificateKeyPassword=x",
		"mongodb://h/db?sslInsecure=true",
		"mongodb://h/db?tlsCertificateFile=/c.pem",
		"mongodb://h/db?tlsPrivateKeyFile=/k.pem",
		"mongodb://h/db?a=1;tlsCAFile=/ca.pem",
		"mongodb://h/db?tls%43AFile=/ca.pem",
		"mongodb://h/db?authMechanism=MONGODB-AWS;authSource=$external",
	} {
		err := (Engine{}).CheckServerDSN(d)
		if err == nil || qerr.From(err).Category != qerr.Invalid || strings.Contains(err.Error(), d) {
			t.Errorf("CheckServerDSN(%q) = %v", d, err)
		}
	}
}

func TestInspectSemicolonOptions(t *testing.T) {
	info, err := Engine{}.Inspect("mongodb://u:pw@h/db?replicaSet=rs0;authSource=admin")
	if err != nil || info.Database != "db" || info.Password != "pw" {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestClientOptions(t *testing.T) {
	// spec.TLS replaces the connection string's TLS settings before the
	// driver reads them, so a CA file named there is never opened.
	opts, _, err := clientOptions(engine.ConnSpec{DSN: "mongodb://h/db?tlsCAFile=/nonexistent/ca.pem&appName=x", TLS: &engine.TLS{Mode: engine.TLSDisable}})
	if err != nil || opts.TLSConfig != nil {
		t.Fatalf("TLS disable: %v %v", opts, err)
	}
	if _, _, err := clientOptions(engine.ConnSpec{DSN: "mongodb://h/db", Password: "pw"}); qerr.From(err).Category != qerr.Invalid {
		t.Fatalf("password without a user: %v", err)
	}
	opts, _, err = clientOptions(engine.ConnSpec{DSN: "mongodb://u@h/db", Password: "s3cret-pw"})
	if err != nil || opts.Auth.Password != "s3cret-pw" || opts.Auth.Username != "u" {
		t.Fatalf("password: %+v %v", opts.Auth, err)
	}
	// With a dialer, the driver's HTTP requests (OCSP responders) go through
	// it too.
	dialled := false
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialled = true
		return nil, errors.New("refused by policy")
	}
	opts, _, err = clientOptions(engine.ConnSpec{DSN: "mongodb://h/db", Dial: dial, Server: true})
	if err != nil || opts.Dialer == nil || opts.HTTPClient == nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := opts.HTTPClient.Get("http://ocsp.example.invalid/"); err == nil || !dialled {
		t.Fatalf("HTTP client bypassed the dialer: %v", err)
	}
}

func TestRedactsKeyFilePassword(t *testing.T) {
	info, err := Engine{}.Inspect("mongodb://u:pw@h/db?tlsCertificateKeyFile=/k.pem&tlsCertificateKeyFilePassword=keypw;SSLClientCertificateKeyPassword=k2&appName=a")
	if err != nil || strings.Contains(info.Redacted, "keypw") || strings.Contains(info.Redacted, "k2") ||
		info.Redacted != "mongodb://u:***@h/db?tlsCertificateKeyFile=/k.pem&tlsCertificateKeyFilePassword=***;SSLClientCertificateKeyPassword=***&appName=a" {
		t.Fatalf("%q %v", info.Redacted, err)
	}
}
