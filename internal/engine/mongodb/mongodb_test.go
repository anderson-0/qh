package mongodb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/testutil"
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

// A refused key of up to 64 bytes is named; a longer one is not echoed,
// since cutting it could leave part of a secret or of a character.
func TestCheckServerDSNLongKey(t *testing.T) {
	const canary = "canary-pw-7f3!"
	const long = "error[invalid]: this server does not accept an option longer than 64 bytes in a mongodb connection string. " + serverOptionsHint
	key64 := strings.Repeat("k", 64)
	for i, tc := range []struct{ dsn, want string }{
		{"mongodb://h/db?" + key64 + "=1",
			"error[invalid]: this server does not accept the option " + key64 + " in a mongodb connection string. " + serverOptionsHint},
		{"mongodb://h/db?" + key64 + "x=1", long},
		{"mongodb://u:" + canary + "@h/db?" + strings.Repeat("k", 60) + canary + "=1", long},
		{"mongodb://h/db?" + strings.Repeat("k", 63) + "雪=1", long},
	} {
		err := (Engine{}).CheckServerDSN(tc.dsn)
		if err == nil || err.Error() != tc.want || errors.Unwrap(err) != nil {
			t.Errorf("case %d: not the expected fixed text", i)
		}
		if err != nil {
			testutil.RequireNoSecret(t, fmt.Sprintf("case %d", i), err.Error(), canary[:4])
		}
		// clientOptions applies the same check in server mode.
		if _, _, err := clientOptions(engine.ConnSpec{DSN: tc.dsn, Server: true}); err == nil || err.Error() != tc.want {
			t.Errorf("case %d: clientOptions: not the expected fixed text", i)
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

// The driver lowercases option keys with strings.ToLower, and U+0130 (İ)
// lowers to "i" without EqualFold matching it. It also takes the userinfo up
// to the first "@" in the whole string, allowing "?" in it. Each of these
// reached the driver with a refused option before the allowlist.
var serverBypasses = []string{
	"mongodb://127.0.0.1/db?tlsCAFİle=/etc/passwd",
	"mongodb://127.0.0.1/db?tlsCAF%C4%B0le=/etc/passwd",
	"mongodb://127.0.0.1/db?authMechanİsm=MONGODB-AWS",
	"mongodb://127.0.0.1/db?authMechan%C4%B0sm=MONGODB-AWS",
	"mongodb://127.0.0.1/db?tlsİnsecure=true",
	"mongodb://127.0.0.1/db?tls%C4%B0nsecure=true",
	"mongodb://127.0.0.1/db?tlsCertificateKeyFİle=/k.pem",
	"mongodb://127.0.0.1/db?tlsCertificateKeyF%C4%B0le=/k.pem",
	"mongodb://u?zz=x:pw@127.0.0.1/db?tlsCAFile=/etc/passwd",
	"mongodb://u?zz=x@127.0.0.1/db?authMechanism=MONGODB-OIDC&authMechanismProperties=ENVIRONMENT:k8s",
	"mongodb://u?zz=x@127.0.0.1/db?authMechanism=MONGODB-OIDC",
}

func TestServerModeBypasses(t *testing.T) {
	for _, d := range serverBypasses {
		err := (Engine{}).CheckServerDSN(d)
		if err == nil || qerr.From(err).Category != qerr.Invalid || strings.Contains(err.Error(), d) || strings.Contains(err.Error(), "/etc/passwd") {
			t.Errorf("CheckServerDSN(%q) = %v", d, err)
		}
		// clientOptions applies the same check before the driver sees the
		// connection string.
		if _, _, err := clientOptions(engine.ConnSpec{DSN: d, Server: true}); err == nil || !strings.Contains(err.Error(), "does not accept") {
			t.Errorf("clientOptions(%q) = %v", d, err)
		}
	}
}

func TestServerAllowlist(t *testing.T) {
	ok := []string{
		"mongodb://u:p@h1:27017,h2/db?authSource=admin&authMechanism=SCRAM-SHA-256&replicaSet=rs0&tls=true&retryWrites=true" +
			"&retryReads=false&w=majority&wTimeoutMS=100&journal=true&readPreference=secondary&readPreferenceTags=dc:ny" +
			"&maxStalenessSeconds=120&readConcernLevel=majority&appName=x&compressors=zstd,zlib&zlibCompressionLevel=6" +
			"&zstdCompressionLevel=3&connectTimeoutMS=1&socketTimeoutMS=1&serverSelectionTimeoutMS=1&heartbeatFrequencyMS=600" +
			"&localThresholdMS=1&maxPoolSize=2&minPoolSize=1&maxConnecting=2&maxIdleTimeMS=1&waitQueueTimeoutMS=1" +
			"&directConnection=false&loadBalanced=false&timeoutMS=1&serverMonitoringMode=poll;SSL=true",
		"mongodb+srv://u:p@c.example.net/db?srvMaxHosts=2&srvServiceName=mongodb",
		"mongodb://u@h/db?authMechanism=SCRAM-SHA-1",
		"mongodb://u@h/db?authMechanism=PLAIN",
		"mongodb://h/db?authMechanism=MONGODB-X509",
		"mongodb://u:p@h",
	}
	for _, d := range ok {
		if err := (Engine{}).CheckServerDSN(d); err != nil {
			t.Errorf("CheckServerDSN(%q) = %v", d, err)
		}
	}
	for d, key := range map[string]string{
		"mongodb://h/db?tlsAllowInvalidCertificates=true":                   "tlsAllowInvalidCertificates",
		"mongodb://h/db?tlsAllowInvalidHostnames=true":                      "tlsAllowInvalidHostnames",
		"mongodb://h/db?tlsInsecure=true":                                   "tlsInsecure",
		"mongodb://h/db?tlsDisableOCSPEndpointCheck=true":                   "tlsDisableOCSPEndpointCheck",
		"mongodb://h/db?authMechanismProperties=SERVICE_NAME:x":             "authMechanismProperties",
		"mongodb://h/db?connect=direct":                                     "connect",
		"mongodb://h/db?someFutureOption=1":                                 "someFutureOption",
		"mongodb://h/db?appName=a&TLSCAFILE=/x":                             "TLSCAFILE",
		"mongodb://h/db?tlsCAF%C4%B0le=/etc/passwd":                         "tlsCAFİle",
		"mongodb://u@h/db?authMechanism=scram-sha-256":                      "authMechanism",
		"mongodb://u@h/db?authMechanism=GSSAP%C4%B0":                        "authMechanism",
		"mongodb://u@h/db?authMechanism=SCRAM-SHA-256&authMechanism=GSSAPI": "authMechanism",
	} {
		err := (Engine{}).CheckServerDSN(d)
		if err == nil || qerr.From(err).Category != qerr.Invalid || !strings.Contains(qerr.From(err).Message, key) ||
			strings.Contains(err.Error(), d) || !strings.Contains(qerr.From(err).Hint, "tls") {
			t.Errorf("CheckServerDSN(%q) = %v, want a refusal naming %s", d, err, key)
		}
	}
}

func TestUserinfoSplitMatchesDriver(t *testing.T) {
	info, err := Engine{}.Inspect("mongodb://u?zz=x:pw@127.0.0.1/db?appName=a")
	if err != nil || info.Host != "127.0.0.1" || info.User != "u?zz=x" || info.Password != "pw" ||
		info.Redacted != "mongodb://u?zz=x:***@127.0.0.1/db?appName=a" {
		t.Fatalf("%+v %v", info, err)
	}
	for _, bad := range []string{
		"mongodb://u/x:p@h/db",  // "/" in the user name
		"mongodb://u:p/x@h/db",  // "/" in the password
		"mongodb://u:a:b@h/db",  // ":" in the password
		"mongodb://u:p@ss@h/db", // "@" in the hosts
		"mongodb://h?x=1",       // no "/" before the options
		"mongodb://h/db?appName=x@y",
	} {
		if _, err := (Engine{}).Inspect(bad); err == nil || qerr.From(err).Category != qerr.Invalid {
			t.Errorf("Inspect(%q) = %v", bad, err)
		}
	}
	// TLS stripping sees the same options as the driver.
	opts, _, err := clientOptions(engine.ConnSpec{DSN: "mongodb://u?tlsCAFile=x@h/db?tlsCAF%C4%B0le=/nonexistent/ca.pem", TLS: &engine.TLS{Mode: engine.TLSDisable}})
	if err != nil || opts.TLSConfig != nil {
		t.Fatalf("TLS strip: %v", err)
	}
}

// qh's parser must agree with the driver's on every connection string both
// accept, and must refuse those the driver refuses.
func TestParserAgreesWithDriver(t *testing.T) {
	for _, d := range append([]string{
		"mongodb://u:p@h1:1,h2/db?a=1;b=2",
		"mongodb://u:p%40ss@h/d%2Fb",
		"mongodb://h%2Cevil/db",
		"mongodb://[::1]:27017/db?x=%41",
		"mongodb://h?appName=x@y/db",
		"mongodb://u@h/db?TLSCAFILE=/x&tlsCAFile=/y",
		"mongodb://u@h/db?authMechanism=GSSAP%C4%B0",
		"mongodb://localhost",
		"mongodb://u:p@ss@h/db",
		"mongodb://h?x=1",
		"mongodb://h/db?x",
		"mongodb://h/db?=x",
		"mongodb://u/x:p@h/db",
		"mongodb://u:p/x@h/db",
		"mongodb://u:a:b@h/db",
		"mongodb://::1/db",
		"mongodb://h:0/db",
		"mongodb://h:65536/db",
		"mongodb://h/db?appName=x@y",
		"mongodb://h/db?a=%zz",
	}, serverBypasses...) {
		ours, ourErr := parseURI(d)
		cs, theirErr := connstring.Parse(d)
		if theirErr != nil {
			if ourErr == nil {
				t.Errorf("%q: the driver refuses it (%v), qh accepts it", d, theirErr)
			}
			continue
		}
		if ourErr != nil {
			t.Errorf("%q: qh refuses it (%v), the driver accepts it", d, ourErr)
			continue
		}
		var keys []string
		for _, o := range ours.options {
			if !contains(keys, o.lkey) {
				keys = append(keys, o.lkey)
			}
		}
		var theirKeys []string
		for k := range cs.Options {
			theirKeys = append(theirKeys, k)
		}
		slices.Sort(keys)
		slices.Sort(theirKeys)
		if !slices.Equal(ours.hosts, cs.Hosts) || ours.user != cs.Username || ours.password != cs.Password ||
			ours.hasPassword != cs.PasswordSet || ours.db != cs.Database || !slices.Equal(keys, theirKeys) {
			t.Errorf("%q:\n qh     %v %q %q %v %q %v\n driver %v %q %q %v %q %v", d,
				ours.hosts, ours.user, ours.password, ours.hasPassword, ours.db, keys,
				cs.Hosts, cs.Username, cs.Password, cs.PasswordSet, cs.Database, theirKeys)
		}
	}
}

func TestMapErr(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want qerr.Category
	}{
		"max time":         {mongo.CommandError{Code: 50, Name: "MaxTimeMSExpired", Message: "operation exceeded time limit"}, qerr.Timeout},
		"auth":             {mongo.CommandError{Code: 18, Name: "AuthenticationFailed", Message: "Authentication failed."}, qerr.ConnectionFailed},
		"server selection": {fmt.Errorf("server selection error: %w, current topology: { Type: Unknown }", context.DeadlineExceeded), qerr.ConnectionFailed},
		"deadline":         {fmt.Errorf("reading: %w", context.DeadlineExceeded), qerr.Timeout},
		"write":            {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 11000, Message: "E11000 duplicate key"}}}, qerr.QueryError},
		"write max time":   {mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 50, Message: "operation exceeded time limit"}}}, qerr.Timeout},
		"command":          {mongo.CommandError{Code: 2, Name: "BadValue", Message: "bad"}, qerr.QueryError},
		"qerr":             {qerr.New(qerr.Invalid, "x"), qerr.Invalid},
	} {
		got := qerr.From(mapErr(tc.err, same))
		if got.Category != tc.want {
			t.Errorf("%s: %v, want %s", name, got, tc.want)
		}
	}
	if !strings.Contains(qerr.From(mapErr(mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 11000, Message: "E11000 duplicate key"}}}, same)).Message, "E11000") {
		t.Error("write error message lost")
	}
	if mapErr(nil, same) != nil {
		t.Error("mapErr(nil) != nil")
	}
}

func TestRedactsAuthMechanismProperties(t *testing.T) {
	info, err := Engine{}.Inspect("mongodb://h/db?authMechanism=MONGODB-AWS&authMechanismProperties=AWS_SESSION_TOKEN:secret&appName=a")
	if err != nil || strings.Contains(info.Redacted, "secret") ||
		info.Redacted != "mongodb://h/db?authMechanism=MONGODB-AWS&authMechanismProperties=***&appName=a" {
		t.Fatalf("%q %v", info.Redacted, err)
	}
}
