package mongodb

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/redact"
	"github.com/anderson-0/qh/internal/testutil"
)

// same is the clean function of a connection without secrets.
func same(s string) string { return s }

// Failure messages name the case and counts only, never a value.
func TestCredentialsMongoDB(t *testing.T) {
	for _, c := range []struct {
		name, dsn string
		want      []string
	}{
		{"userinfo", "mongodb://u:pw-1@h/db", []string{"pw-1"}},
		{"userinfo encoded", "mongodb://u:p%40ss@h1:27017,h2/db", []string{"p%40ss", "p@ss"}},
		{"srv", "mongodb+srv://u:p%20w@c.example.net/app", []string{"p%20w", "p w"}},
		// The driver does not trim: its password is the space.
		{"userinfo decodes to a space", "mongodb://u:%20@h/db", []string{"%20", " "}},
		{"user name holding ?", "mongodb://u?zz=x:pw-1@127.0.0.1/db?appName=a", []string{"pw-1"}},
		{"empty password", "mongodb://u:@h/db", nil},
		{"no password", "mongodb://u@h/db?authSource=admin", nil},
		{"no credentials", "mongodb://localhost", nil},
		{"key file password", "mongodb://h/db?tlsCertificateKeyFile=/k.pem&tlsCertificateKeyFilePassword=k%2B1", []string{"k%2B1", "k+1"}},
		{"plus decodes to a space", "mongodb://h/db?tlsCertificateKeyFilePassword=a+b", []string{"a+b", "a b"}},
		{"ssl alias after ;", "mongodb://h/db?appName=a;SSLClientCertificateKeyPassword=k-2", []string{"k-2"}},
		{"encoded key", "mongodb://h/db?tlsCertificateKeyFilePa%73sword=k-3", []string{"k-3"}},
		{"empty key file password", "mongodb://h/db?tlsCertificateKeyFilePassword=&appName=a", nil},
		{"session token", "mongodb://h/db?authMechanism=MONGODB-AWS&authMechanismProperties=AWS_SESSION_TOKEN:t%2Fk1,SERVICE_NAME:x",
			[]string{"AWS_SESSION_TOKEN:t%2Fk1,SERVICE_NAME:x", "AWS_SESSION_TOKEN:t/k1,SERVICE_NAME:x", "t/k1"}},
		{"session token key case", "mongodb://h/db?authMechanismProperties=aws_session_token:tk-2", []string{"aws_session_token:tk-2", "tk-2"}},
		{"session token holding :", "mongodb://h/db?authMechanismProperties=AWS_SESSION_TOKEN:a:b", []string{"AWS_SESSION_TOKEN:a:b", "a:b"}},
		{"other properties only", "mongodb://h/db?authMechanismProperties=SERVICE_NAME:mongodb", []string{"SERVICE_NAME:mongodb"}},
		{"empty session token", "mongodb://h/db?authMechanismProperties=AWS_SESSION_TOKEN:", []string{"AWS_SESSION_TOKEN:"}},
		{"each value once", "mongodb://u:pw-1@h/db?tlsCertificateKeyFilePassword=pw-1&sslClientCertificateKeyPassword=k-4;tlsCertificateKeyFilePassword=k-4",
			[]string{"pw-1", "k-4"}},
	} {
		got, err := Engine{}.Credentials(c.dsn)
		if err != nil {
			t.Errorf("%s: unexpected error", c.name)
			continue
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %d values, want %d (values withheld)", c.name, len(got), len(c.want))
		}
	}
}

func TestCredentialsMongoDBInvalid(t *testing.T) {
	for i, dsn := range []string{
		"", "postgres://u:pw-1@h/db", "MONGODB://u:pw-1@h/db", " mongodb://u:pw-1@h/db", "mongodb://", "mongodb://u:pw-1@",
		"mongodb://u:pw-1%zz@h/db", "mongodb://u:pw-1:x@h/db", "mongodb://u:pw/1@h/db", "mongodb://u:pw-1@x@h/db",
		"mongodb://h:65536/db", "mongodb+srv://u:pw-1@h:27017/db", "mongodb://h/db?tlsCertificateKeyFilePassword",
		"mongodb://h/db?tlsCertificateKeyFilePassword=pw-1%zz",
	} {
		got, err := Engine{}.Credentials(dsn)
		if !isFixedInvalid(err) {
			t.Errorf("case %d: expected the fixed invalid error, without a cause", i)
		}
		if got != nil {
			t.Errorf("case %d: credentials returned with an error", i)
		}
	}
}

func TestCredentialsMongoDBCanary(t *testing.T) {
	const canary = "canary-pw-7f3!"
	for i, dsn := range []string{
		"mongodb://u:" + canary + "%zz@h/db",
		"mongodb://u:" + canary + ":x@h/db",
		"mongodb://u:" + canary + "/x@h/db",
		"mongodb://u:" + canary + "@h@x/db",
		"mongodb://u:" + canary + "@",
		"mongodb://u:" + canary + "@h:99999/db",
		"mongodb://h/db?tlsCertificateKeyFilePassword=" + canary + "%zz",
		"mongodb://h/db?" + canary,
		"postgres://u:" + canary + "@h/db",
	} {
		creds, cerr := Engine{}.Credentials(dsn)
		_, ierr := Engine{}.Inspect(dsn)
		_, oerr := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: dsn})
		for name, err := range map[string]error{"Credentials": cerr, "Inspect": ierr, "Open": oerr} {
			if !isFixedInvalid(err) {
				t.Errorf("case %d: %s: expected the fixed invalid error, without a cause", i, name)
				continue
			}
			e := qerr.From(err)
			if strings.Contains(err.Error(), canary) || strings.Contains(e.Message, canary) || strings.Contains(e.Hint, canary) {
				t.Errorf("case %d: %s error contains the canary", i, name)
			}
		}
		if creds != nil {
			t.Errorf("case %d: credentials returned with an error", i)
		}
	}
}

// acceptedDSNs are connection strings that this package's tests show
// Inspect accepting (TestInspect, the server-mode and alias tests, the
// client option, redaction and parser tests, the concern tests and the
// SetPassword fixtures and results).
var acceptedDSNs = []string{
	// TestInspect, TestInspectSemicolonOptions, TestUserinfoSplitMatchesDriver
	"mongodb://bob:s3cret@h1:27017,h2:27018/shop?replicaSet=rs0&authSource=admin",
	"mongodb+srv://u:p%40ss@cluster0.example.net/app",
	"mongodb://localhost",
	"mongodb://u:pw@h/db?replicaSet=rs0;authSource=admin",
	"mongodb://u?zz=x:pw@127.0.0.1/db?appName=a",
	"mongodb://u?tlsCAFile=x@h/db?tlsCAF%C4%B0le=/nonexistent/ca.pem",
	// TestCheckServerDSN, TestCheckServerDSNAliases, TestServerModeRefusesFileOptions
	"mongodb://u:p@h/db?tls=true&authSource=admin",
	"mongodb+srv://u:p@c.example.net/db?retryWrites=true",
	"mongodb://h/db?tlsCAFile=/etc/ssl/ca.pem",
	"mongodb://h/db?TLSCertificateKeyFile=/k.pem",
	"mongodb://h/db?tlsCertificateKeyFilePassword=x",
	"mongodb://h/db?tlsInsecure=true",
	"mongodb://h/db?authMechanism=MONGODB-AWS",
	"mongodb://u@h/db?authMechanism=GSSAPI",
	"mongodb://h/db?authMechanism=mongodb-oidc",
	"mongodb://h/db?sslCertificateAuthorityFile=/ca.pem",
	"mongodb://h/db?sslClientCertificateKeyFile=/k.pem",
	"mongodb://h/db?sslClientCertificateKeyPassword=x",
	"mongodb://h/db?sslInsecure=true",
	"mongodb://h/db?tlsCertificateFile=/c.pem",
	"mongodb://h/db?tlsPrivateKeyFile=/k.pem",
	"mongodb://h/db?a=1;tlsCAFile=/ca.pem",
	"mongodb://h/db?tls%43AFile=/ca.pem",
	"mongodb://h/db?authMechanism=MONGODB-AWS;authSource=$external",
	"mongodb://h/db?tlsCAFile=/etc/passwd",
	// TestClientOptions, TestRedactsKeyFilePassword, TestRedactsAuthMechanismProperties
	"mongodb://h/db?tlsCAFile=/nonexistent/ca.pem&appName=x",
	"mongodb://h/db",
	"mongodb://u@h/db",
	"mongodb://u:pw@h/db?tlsCertificateKeyFile=/k.pem&tlsCertificateKeyFilePassword=keypw;SSLClientCertificateKeyPassword=k2&appName=a",
	"mongodb://h/db?authMechanism=MONGODB-AWS&authMechanismProperties=AWS_SESSION_TOKEN:secret&appName=a",
	// TestServerAllowlist
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
	"mongodb://h/db?tlsAllowInvalidCertificates=true",
	"mongodb://h/db?tlsAllowInvalidHostnames=true",
	"mongodb://h/db?tlsDisableOCSPEndpointCheck=true",
	"mongodb://h/db?authMechanismProperties=SERVICE_NAME:x",
	"mongodb://h/db?connect=direct",
	"mongodb://h/db?someFutureOption=1",
	"mongodb://h/db?appName=a&TLSCAFILE=/x",
	"mongodb://h/db?tlsCAF%C4%B0le=/etc/passwd",
	"mongodb://u@h/db?authMechanism=scram-sha-256",
	"mongodb://u@h/db?authMechanism=GSSAP%C4%B0",
	"mongodb://u@h/db?authMechanism=SCRAM-SHA-256&authMechanism=GSSAPI",
	// serverBypasses
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
	// TestParserAgreesWithDriver
	"mongodb://u:p@h1:1,h2/db?a=1;b=2",
	"mongodb://u:p%40ss@h/d%2Fb",
	"mongodb://h%2Cevil/db",
	"mongodb://[::1]:27017/db?x=%41",
	"mongodb://h?appName=x@y/db",
	"mongodb://u@h/db?TLSCAFILE=/x&tlsCAFile=/y",
	// concern tests
	"mongodb://h/db?readPreference=secondaryPreferred&readConcernLevel=majority&w=majority&journal=true&wtimeoutMS=100",
	"mongodb://h/db?w=2",
	// SetPassword fixtures and results
	"mongodb://app:old@db/shop",
	"mongodb://app:next@db/shop",
	"mongodb://app@db/shop",
	"mongodb://app:@db/shop",
	"mongodb://app:old@db",
	"mongodb://app:next@db",
	"mongodb://app:old@db:27017,[::1]:27018,other/shop?replicaSet=rs0&tls=true",
	"mongodb://a%40b%3ac%2Bd:old@db/d%2Fb",
	"mongodb://app:%20%40%3A%2F%3F%2B%23%25%3B%E9%9B%AA%09%0A%20@db/shop",
	"mongodb://app:old%40%3A%2F%3F@db/shop",
	"mongodb://app:old@db/shop?authSource=admin&&authMechanism=SCRAM-SHA-256;authSource=shop;&replicaSet=rs0;tls=true&appName=a%2Bb",
	"mongodb://app:old@db/shop?tlsCertificateKeyFilePassword=other&authMechanismProperties=SERVICE_NAME:other",
	"mongodb://app?name=x:old@db/shop",
	"mongodb://app:old@db/shop?authSource=admin",
	"mongodb://app:@db/shop?authSource=admin",
	"mongodb://db/shop",
	"mongodb://:old@db/shop",
	"mongodb://:@db/shop",
	"mongodb://@db/shop",
	"mongodb://a%40b:old@qh-password-edit.invalid/shop?tls=true&tlsCAFile=/qh-password-edit-nonexistent/ca.pem;tlsCertificateKeyFile=/qh-password-edit-nonexistent/client.pem&authSource=admin&authSource=shop",
	"mongodb+srv://a%40b:old@qh-password-edit.invalid/shop?tls=true&tlsCAFile=/qh-password-edit-nonexistent/ca.pem;tlsCertificateKeyFile=/qh-password-edit-nonexistent/client.pem&authSource=admin&authSource=shop",
}

// A connection string Inspect accepts must not fail extraction (D2), and
// the password Inspect reports must be extracted. Inspect and Credentials
// share parseURI, so the comparison with driverSecrets, the secrets the
// driver's own parser reads, is what guards the extractor.
func TestCredentialsMongoDBCorpus(t *testing.T) {
	built, err := Engine{}.BuildDSN(engine.DSNFields{Host: "db.example.com", Port: 27017, User: "app", Password: "p w/@:?%", Database: "shop"})
	if err != nil {
		t.Fatal("BuildDSN failed")
	}
	for i, dsn := range append(slices.Clone(acceptedDSNs), built) {
		info, err := Engine{}.Inspect(dsn)
		if err != nil {
			t.Errorf("corpus %d: Inspect rejects it; remove it from acceptedDSNs", i)
			continue
		}
		creds, err := Engine{}.Credentials(dsn)
		if err != nil {
			t.Errorf("corpus %d: Credentials rejects a string Inspect accepts", i)
			continue
		}
		if info.Password != "" && !slices.Contains(creds, info.Password) {
			t.Errorf("corpus %d: the password Inspect reports was not extracted", i)
		}
		if secrets, ok := driverSecrets(dsn); ok {
			for j, s := range secrets {
				if s != "" && !slices.Contains(creds, s) {
					t.Errorf("corpus %d: driver secret #%d was not extracted", i, j)
				}
			}
		}
	}
}

// A seeded property check over random URI-like strings: whenever Inspect
// accepts one, Credentials accepts it too and finds the password Inspect
// reports. When the driver's own parser accepts it as well, every secret
// the driver reads from it is found.
func TestCredentialsMongoDBProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	pick := func(s []string) string { return s[r.IntN(len(s))] }
	userinfo := []string{"", "", "u@", "u:@", "u:pw@", "u:p%40w@", "u:%20@", "u:+@", ":pw@", "u?x=1:pw@", "%75:p%2Bw@",
		"u:p%zz@", "u:a:b@", "u/x:p@", "u:p/w@", "u:pw@x@", "@", "u:pw-!@"}
	hosts := []string{"h", "h", "h:27017", "h1,h2:27018", "[::1]", "[::1]:1", "h%2Cx", "h:0", "", "h,", "h?x=1"}
	paths := []string{"", "/", "/", "/db", "/db", "/d%2Fb", "/%zz"}
	keys := []string{"tlsCertificateKeyFilePassword", "sslClientCertificateKeyPassword", "SSLCLIENTCERTIFICATEKEYPASSWORD",
		"tlsCertificateKeyFilePa%73sword", "tlsCertificateKeyFİlePassword", "authMechanismProperties", "AUTHMECHANISMPROPERTIES",
		"appName", "authSource", "x", "tlsCertificateKeyFilePassword", "authMechanismProperties", "%zz", ""}
	values := []string{"", "pw", "p%40w", "%20", "+", "a+b", "%zz", "x@y", "a=b", "AWS_SESSION_TOKEN:tk", "aws_session_token:t%2Fk",
		"AWS_SESSION_TOKEN:", "SERVICE_NAME:x,AWS_SESSION_TOKEN:t2", "SERVICE_NAME:x", "AWS_SESSION_TOKEN:a:b",
		"AWS_SESSION_TOKEN:a,b", "AWS_SESSION_TOKEN:%2C", ",", ":",
		// Real tokens are base64: "+" and "/" percent-encoded, or a "+"
		// that decodes to a space, and "=" padding.
		"AWS_SESSION_TOKEN:IQo%2BJb3%2FZ2luX2Vj%3D%3D", "SERVICE_NAME:x,AWS_SESSION_TOKEN:IQo+Jb3/Z2lu==",
		"AWS_SESSION_TOKEN:FwoGZX%2Bi,SERVICE_REALM:r"}
	tokenValues := []string{"AWS_SESSION_TOKEN:tk", "aws_session_token:t%2Fk", "AWS_SESSION_TOKEN:a:b",
		"AWS_SESSION_TOKEN:IQo%2BJb3%2FZ2luX2Vj%3D%3D", "SERVICE_NAME:x,AWS_SESSION_TOKEN:IQo+Jb3/Z2lu==",
		"AWS_SESSION_TOKEN:FwoGZX%2Bi,SERVICE_REALM:r", "AWS_SESSION_TOKEN:%20", "AWS_SESSION_TOKEN:t1,AWS_SESSION_TOKEN:t2"}
	const n = 4000
	accepted, withPassword, driverChecked, driverFound, tokens := 0, 0, 0, 0, 0
	for i := 0; i < n; i++ {
		var b strings.Builder
		b.WriteString(pick([]string{"mongodb://", "mongodb://", "mongodb://", "mongodb+srv://"}))
		b.WriteString(pick(userinfo) + pick(hosts) + pick(paths))
		if r.IntN(4) != 0 {
			b.WriteByte('?')
			for k := r.IntN(4); k >= 0; k-- {
				key := pick(keys)
				b.WriteString(key)
				if r.IntN(20) != 0 {
					b.WriteByte('=')
				}
				if strings.EqualFold(key, "authMechanismProperties") && r.IntN(3) != 0 {
					b.WriteString(pick(tokenValues)) // most properties carry a session token
				} else {
					b.WriteString(pick(values))
				}
				if k > 0 {
					b.WriteString(pick([]string{"&", ";", "&&"}))
				}
			}
		}
		dsn := b.String()
		info, err := Engine{}.Inspect(dsn)
		if err != nil {
			continue
		}
		accepted++
		creds, err := Engine{}.Credentials(dsn)
		if err != nil {
			t.Errorf("input %d: Credentials rejects a string Inspect accepts", i)
			continue
		}
		if info.Password != "" {
			withPassword++
			if !slices.Contains(creds, info.Password) {
				t.Errorf("input %d: the password Inspect reports was not extracted", i)
			}
		}
		if secrets, ok := driverSecrets(dsn); ok {
			driverChecked++
			for j, s := range secrets {
				if s == "" {
					continue
				}
				driverFound++
				if j == 1 {
					tokens++
				}
				if !slices.Contains(creds, s) {
					t.Errorf("input %d: driver secret #%d was not extracted", i, j)
				}
			}
		}
	}
	t.Logf("%d accepted, %d with a password, %d also parsed by the driver, %d driver secrets (%d session tokens), of %d",
		accepted, withPassword, driverChecked, driverFound, tokens, n)
	if accepted < n/5 || withPassword < n/20 || driverChecked < n/10 || driverFound < n/10 || tokens < n/50 {
		t.Fatal("weak generator")
	}
}

// driverSecrets returns the secrets the driver's parser reads from dsn: the
// password, the AWS session token and the key file password, in that order. It skips
// mongodb+srv strings, whose parsing looks up DNS records, and strings the
// driver refuses.
func driverSecrets(dsn string) ([]string, bool) {
	if !strings.HasPrefix(dsn, "mongodb://") {
		return nil, false
	}
	cs, err := connstring.Parse(dsn)
	if err != nil {
		return nil, false
	}
	out := []string{cs.Password, cs.AuthMechanismProperties["AWS_SESSION_TOKEN"]}
	if cs.SSLClientCertificateKeyPasswordSet && cs.SSLClientCertificateKeyPassword != nil {
		out = append(out, cs.SSLClientCertificateKeyPassword())
	}
	return out, true
}

func isFixedInvalid(err error) bool {
	var qe *qerr.Error
	return errors.As(err, &qe) && qe.Category == qerr.Invalid && qe.Err == nil && errors.Unwrap(err) == nil &&
		err.Error() == badURI().Error()
}

// Without spec.Redact, the clean function holds the connection string, the
// password and every credential in the string.
func TestCleanerWithoutSpecRedact(t *testing.T) {
	dsn := "mongodb://u:p%40ss@h/db?authMechanismProperties=AWS_SESSION_TOKEN:tok-1"
	clean, err := cleaner(engine.ConnSpec{Engine: "mongodb", DSN: dsn, Password: "over-1"})
	if err != nil {
		t.Fatal("cleaner failed")
	}
	if got := clean("a " + dsn + " b p@ss c p%40ss d tok-1 e over-1 f"); got != "a *** b *** c *** d *** e *** f" {
		t.Errorf("got %d bytes, not the expected text", len(got))
	}
	if _, err := cleaner(engine.ConnSpec{Engine: "mongodb", DSN: "mongodb://u:pw-1%zz@h/db"}); !isFixedInvalid(err) {
		t.Error("an unparseable string must fail with the fixed invalid error")
	}
}

// mapErr cleans driver text before it cuts it at 500 bytes, so a secret
// that straddles the cut leaves no prefix behind.
func TestMapErrCleansBeforeCutting(t *testing.T) {
	secret := "S3cr3t-" + strings.Repeat("q7z!", 8) + "x" // 40 bytes
	if len(secret) != 40 {
		t.Fatal("bad fixture")
	}
	clean := redact.New(secret).Text
	// long returns head padded so that the secret starts at byte 480 of a
	// 600-byte text.
	long := func(head string) string {
		return head + strings.Repeat(".", 480-len(head)) + secret + strings.Repeat(".", 80)
	}
	selection := "server selection error: " + context.DeadlineExceeded.Error() + ", "
	for _, tc := range []struct {
		name, head, prefix string
		err                error
	}{
		{"query error", "", "", errors.New(long(""))},
		{"connection", "connection(h:27017) closed: ", "cannot reach MongoDB: ", errors.New(long("connection(h:27017) closed: "))},
		{"server selection", selection, "cannot reach MongoDB: ",
			fmt.Errorf("server selection error: %w, %s", context.DeadlineExceeded, long(selection)[len(selection):])},
	} {
		if len(tc.err.Error()) != 600 || strings.Index(tc.err.Error(), secret) != 480 {
			t.Fatalf("%s: bad fixture", tc.name)
		}
		got := qerr.From(mapErr(tc.err, clean)).Message
		want := tc.prefix + tc.head + strings.Repeat(".", 480-len(tc.head)) + redact.Marker + strings.Repeat(".", 17) + "…"
		if got != want {
			t.Errorf("%s: got %d bytes, not the expected text", tc.name, len(got))
		}
		for i := 0; i+8 <= len(secret); i++ {
			testutil.RequireNoSecret(t, tc.name, got, secret[i:i+8])
		}
		// Control: cleaning after the cut would leave the first 20 bytes.
		if raw := qerr.From(mapErr(tc.err, same)).Message; strings.Contains(raw, secret) || !strings.Contains(raw, secret[:20]) {
			t.Errorf("%s: the fixture does not straddle the cut", tc.name)
		}
	}
}

// conn.err redacts secrets of any length, in the message and the hint.
func TestConnErrRedactsShortPasswords(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec engine.ConnSpec
		err  error
		want string
	}{
		{"inline", engine.ConnSpec{DSN: "mongodb://u:k@h/db"},
			mongo.CommandError{Code: 2, Name: "BadValue", Message: "bad key k"},
			"error[query_error]: bad ***ey *** (BadValue, code 2)"},
		{"structured", engine.ConnSpec{DSN: "mongodb://u@h/db", Password: "w"},
			mongo.CommandError{Code: 18, Name: "AuthenticationFailed", Message: "Authentication failed."},
			"error[connection_failed]: authentication failed. Check the user name, pass***ord and authSource"},
		{"cut", engine.ConnSpec{DSN: "mongodb://u:k@h/db"},
			errors.New("connection(h) " + strings.Repeat("k", 600)),
			"error[connection_failed]: cannot reach MongoDB: connection(h) ***"},
	} {
		clean, err := cleaner(tc.spec)
		if err != nil {
			t.Fatalf("%s: cleaner failed", tc.name)
		}
		c := &conn{clean: clean}
		if got := c.err(tc.err).Error(); got != tc.want {
			t.Errorf("%s: got %d bytes, not the expected text", tc.name, len(got))
		}
	}
	// A conn that Open did not build cleans nothing and does not panic.
	if got := (&conn{}).err(errors.New("bad key k")).Error(); got != "error[query_error]: bad key k" {
		t.Errorf("bare conn: got %d bytes, not the expected text", len(got))
	}
}

// With spec.Redact set (core.OpenConn's full set), the engine uses it alone.
// A first pass with its own smaller set would turn "abcpw-!" into "***pw-!",
// which the full set could no longer match.
func TestCleanUsesTheFullSet(t *testing.T) {
	const ssh, db = "abcpw-!", "abc"
	spec := engine.ConnSpec{Engine: "mongodb", DSN: "mongodb://u:" + db + "@h/db", Redact: redact.New(ssh, db).Text}
	clean, err := cleaner(spec)
	if err != nil {
		t.Fatal("cleaner failed")
	}
	c := &conn{clean: clean}
	for _, in := range []error{
		errors.New("ssh: handshake as " + ssh + " refused"),
		errors.New("connection(h) " + strings.Repeat(".", 482) + ssh + strings.Repeat(".", 10)), // across the cut
	} {
		got := c.err(in).Error()
		if !strings.Contains(got, redact.Marker) {
			t.Error("nothing was redacted")
		}
		testutil.RequireNoSecret(t, "conn.err", got, "pw-!", "abc", "bcp", "cpw")
	}
	if got := c.err(errors.New("ssh: handshake as " + ssh + " refused")).Error(); got != "error[query_error]: ssh: handshake as *** refused" {
		t.Errorf("got %d bytes, not the expected text", len(got))
	}
	// Control: the engine's own set alone leaves the tail.
	if got := redact.New(spec.DSN, db).Text(ssh); got != "***pw-!" {
		t.Error("control: the partial set behaves differently than assumed")
	}
}

// clientOptions cleans the driver's validation text with the same choice of
// set: spec.Redact when set, else the engine's own.
func TestClientOptionsCleansValidationText(t *testing.T) {
	const pw = "tls-pw-1"
	for _, spec := range []engine.ConnSpec{
		{Engine: "mongodb", DSN: "mongodb://u:" + pw + "@h/db?tls=" + pw},
		{Engine: "mongodb", DSN: "mongodb://u@h/db?tls=" + pw, Redact: redact.New(pw).Text},
	} {
		_, _, err := clientOptions(spec)
		e := qerr.From(err)
		if err == nil || e.Category != qerr.Invalid || !strings.HasPrefix(e.Message, "invalid mongodb connection string: ") ||
			!strings.Contains(e.Message, redact.Marker) {
			t.Fatalf("expected a redacted validation error (redact set: %v)", spec.Redact != nil)
		}
		testutil.RequireNoSecret(t, "validation error", err.Error(), pw)
	}
}

// Catalog notes are part of a successful Introspect, which no caller
// redacts, so unlistedNote redacts them, before it cuts them.
func TestUnlistedNoteRedacts(t *testing.T) {
	secret := "S3cr3t-" + strings.Repeat("q7z!", 3) // 19 bytes
	c := &conn{clean: redact.New("pw", "a b", secret).Text}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"quoted password", mongo.CommandError{Code: 13, Name: "Unauthorized", Message: "not authorized for pw"},
			"not listed: query_error: not authorized for *** (Unauthorized, code 13)"},
		{"joined lines", mongo.CommandError{Code: 13, Name: "Unauthorized", Message: "split a\n b here"},
			"not listed: query_error: split *** here (Unauthorized, code 13)"},
		{"across the cut", errors.New(strings.Repeat("x", 90) + secret + " " + strings.Repeat("y", 20)),
			"not listed: query_error: " + strings.Repeat("x", 90) + "*** yyyyyy…"},
	} {
		got := c.unlistedNote(tc.err)
		if got != tc.want {
			t.Errorf("%s: got %d bytes, not the expected text", tc.name, len(got))
		}
		testutil.RequireNoSecret(t, tc.name, got, secret[:8], "a b")
	}
}
