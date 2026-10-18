package postgres

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/qerr"
)

// Failure messages name the case and counts only, never a value.
func TestCredentialsPostgres(t *testing.T) {
	for _, c := range []struct {
		name, dsn string
		want      []string
	}{
		{"userinfo encoded", "postgres://u:p%40ss@db/app", []string{"p%40ss", "p@ss"}},
		{"userinfo spaces", "postgres://u: pw-1 @db/app", []string{" pw-1 ", "pw-1"}},
		{"query password", "postgres://u@db/app?password=q%20w", []string{"q%20w", "q w"}},
		{"query sslpassword", "postgres://u@db/app?sslmode=require&sslpassword=k%2B1", []string{"k%2B1", "k+1"}},
		{"query key encoded", "postgres://u@db/app?pass%77ord=pw-2", []string{"pw-2"}},
		{"userinfo and query", "postgresql://u:pw-1@db/app?password=pw-2&application_name=x", []string{"pw-1", "pw-2"}},
		{"ipv6 hosts with ? in brackets", "postgres://u:pw-1@[::1?x]:5433,[::2],db:5434/app?password=pw-2", []string{"pw-1", "pw-2"}},
		{"url no password", "postgres://u@db/app?sslmode=disable", nil},
		{"url empty password", "postgres://u:@db/app?password=", nil},
		{"userinfo password decodes to empty", "postgres://u: @db/app", nil},
		{"query password decodes to empty", "postgres://u@db/app?password= ", nil},
		{"url bare", "postgres://", nil},
		{"keyword quoted", `host=db password='a b\'c'`, []string{`'a b\'c'`, `a b\'c`, "a b'c"}},
		{"keyword unquoted", "host=db password=x", []string{"x"}},
		{"keyword unquoted escape", `host=db password=a\ b`, []string{`a\ b`, "a b"}},
		{"keyword trailing backslash", `host=db password=pw-1\`, []string{`pw-1\`, "pw-1"}},
		{"keyword sslpassword", "host=db sslpassword='k1'", []string{"'k1'", "k1"}},
		{"keyword duplicates", "password=one host=db password=two", []string{"one", "two"}},
		{"keyword whitespace in key", "host=db my password=pw-3", []string{"pw-3"}},
		{"keyword adjacent quoted", "host='db'password='pw-4'user='u'", []string{"'pw-4'", "pw-4"}},
		{"keyword no password", "host=db user=u", nil},
		{"keyword empty password", "host=db password=''", nil},
		{"keyword password decodes to empty", `host=db password=\`, nil},
		{"empty", "", nil},
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

func TestCredentialsPostgresInvalid(t *testing.T) {
	for _, dsn := range []string{
		"host=db password='pw-1", "host=db password='pw-1\\'", "host db", "=pw-1", "host=db password=pw-1\x00",
		"postgres://u:pw-1@[::1/app", "postgres://u:pw-1%zz@db/app", "postgres://u@db/app?password=pw-1=x",
		"postgres://u@db/app?password", "postgres://u:pw-1@db/a b", "postgres://u:pw-1@db\x00/app",
		// pgx reads these as keyword/value, with the URL as a parameter name.
		" postgres://u:pw@h/db?sslmode=x", "POSTGRES://u:pw@h/db?sslmode=require",
	} {
		got, err := Engine{}.Credentials(dsn)
		assertFixedInvalid(t, err)
		if got != nil {
			t.Error("credentials returned with an error")
		}
	}
}

// The driver would take a password from PGPASSWORD or the passfile;
// Credentials reads neither.
func TestCredentialsPostgresNoEnvironment(t *testing.T) {
	controlledPostgresEnvironment(t)
	passfile := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(passfile, []byte("*:*:*:*:file-pw-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", passfile)
	t.Setenv("PGPASSWORD", "env-pw-1")
	for _, dsn := range []string{"host=db user=u", "postgres://u@db/app", "postgres://db/app?sslmode=disable", ""} {
		got, err := Engine{}.Credentials(dsn)
		if err != nil || len(got) != 0 {
			t.Errorf("%q: got %d values, err %v", dsn, len(got), err)
		}
		// The environment is live: the driver does read it.
		if info, err := (Engine{}).Inspect(dsn); err != nil || info.Password != "env-pw-1" {
			t.Errorf("%q: Inspect did not take the environment password", dsn)
		}
	}
	t.Setenv("PGPASSWORD", "")
	if info, err := (Engine{}).Inspect("host=db user=u"); err != nil || info.Password != "file-pw-2" {
		t.Error("Inspect did not take the passfile password")
	}
	if got, err := (Engine{}).Credentials("host=db user=u"); err != nil || len(got) != 0 {
		t.Errorf("passfile: got %d values, err %v", len(got), err)
	}
}

func TestCredentialsPostgresCanary(t *testing.T) {
	const canary = "canary-pw-7f3!"
	for i, dsn := range []string{
		"host=db password='" + canary,
		"host=db password=" + canary + "\x00",
		"host=db user=u " + canary,
		"postgres://u:" + canary + "@[::1/app",
		"postgres://u:" + canary + "%zz@db/app",
		"postgres://u@db/app?password=" + canary + "=x",
		"postgres://u:" + canary + "@db/a b",
	} {
		creds, cerr := Engine{}.Credentials(dsn)
		_, ierr := Engine{}.Inspect(dsn)
		for name, err := range map[string]error{"Credentials": cerr, "Inspect": ierr} {
			if err == nil {
				t.Errorf("case %d: %s accepted malformed input", i, name)
				continue
			}
			assertFixedInvalid(t, err)
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
// Inspect accepting (TestInspect, TestBuildDSNRoundTrip, the dial, server
// and TLS tests, and the SetPassword fixtures, which pgconn.ParseConfig
// accepts in the controlled environment).
var acceptedDSNs = []string{
	// TestInspect
	"postgres://bob:hunter22@db.example.com:6543/shop?sslmode=require",
	"postgres://bob:hunter22@db/shop?sslmode=require&sslpassword=keypass99&Password=other77",
	"host=db user=bob password=hunter22 dbname=shop",
	// TestBuildDSNRoundTrip
	"postgres://reader:p%40ss%2Fw%3Ard%25%3F%23@db.example.com:5433/app?application_name=a%20b&sslmode=require",
	"postgres://u@[::1]/my%20db",
	// dial, connection and override tests
	"postgres://u:hunter22@db.example.com:5432/db?connect_timeout=2",
	"postgres://u@/db?host=/tmp&connect_timeout=2",
	"postgres://u:hunter22@127.0.0.1:1/db?connect_timeout=2",
	"postgres://u:p@db.example.com/app?passfile=/nonexistent&sslmode=disable",
	"postgres://u:wrong@db.example.com/app",
	"postgres://u@db.example.com/app",
	// server-mode tests
	"postgres://u:pw@db.example.com:5433/app?sslmode=require&sslsni=1&connect_timeout=5&target_session_attrs=any&application_name=x&channel_binding=prefer&require_auth=password&min_protocol_version=3.0&max_protocol_version=3.0&sslnegotiation=postgres",
	"postgres://u@db.example.com/app?ssl=true&dbname=other",
	"host=db.example.com port=5432 dbname=app user=u password='p w' sslmode=verify-full",
	`host=db.example.com user=victim password='' sslmode=disable application_name=x\`,
	"postgres://u@db.example.com/app?sslmode=disable&",
	"postgres://u@db.example.com/app?",
	"postgres://u@db.example.com/app?pool_max_conns=50",
	`host=db.example.com user=u password=hunter22 passfile=/x application_name=x\`,
	`host=db.example.com user=u passfile=/x\`,
	"host=db.example.com user=u passfile=/x application_name=",
	"postgres://victim@tenant.example.com/app?sslmode=disable",
	"postgres://victim:tenant-pw@tenant.example.com/app?sslmode=disable",
	"host=tenant.example.com user=victim password=tenant-pw dbname=app sslmode=disable",
	"postgres://victim@127.0.0.1:5432/app?sslmode=disable&password=&application_name=",
	"host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name=",
	"host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name= ",
	"host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name=\t",
	`host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name=x\\`,
	`host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name=x\\\`,
	`host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name=x\'`,
	"host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name=x\\\t",
	`host=127.0.0.1 port=5432 user=victim password='' sslmode=disable application_name=\`,
	"host=127.0.0.1 port=5432 user=victim sslmode=disable application_name=x\\",
	"postgres://u@db.example.com/app?sslmode=require",
	"host=db.example.com user=u sslmode=prefer",
	// TLS tests
	"postgres://u:p@db1.example.com:5432,db2.example.com:5433/app?sslmode=prefer",
	"postgres://u:p@db1.example.com/app?sslmode=require",
	"postgres://u:p@db1.example.com/app",
	"postgres://u@[::1]:5432/db?sslmode=disable",
	"postgres://u@[::1?]/db",
	"postgresql://u:p@[db.example.com?x]/db",
	"postgres://h1,[::1?]/db",
	// SetPassword fixtures
	"postgres://db?application_name=agent",
	"postgresql://reader:former-one@db",
	"postgres://",
	"postgres://reader:former-one@db/app? pass%77ord =former-two&Password=runtime-value",
	"postgres://db/app?application_name=agent",
	"postgres://reader@db/app?application_name=agent",
	"postgres://:@db/app",
	"postgres://reader:former-one@db/app",
	"postgresql://reader:former-one@db/app",
	"postgres://reader:former-one@db/app?password=former-two&pass%77ord=former-three&sslpassword=key-passphrase&application_name=first&application_name=last",
	"postgres://first:former-one@db/app?user=second&user=&dbname=other&database=last&sslmode=disable&ssl=true&ssl=false&sslmode=require&ssl=true",
	"postgres://reader@db/a+b?application_name=a+b&options=-c%20search_path%3Dpublic",
	"postgres://reader@db/app?password=former-one&application_name=agent&",
	"postgres://reader@db/app?",
	"postgres://us%20er:former-one@db/a%20b?application_name=hello%20world",
	"postgres://reader:former-one@%2Ftmp/app",
	"postgres://reader:former-one@[::1]:5433,[::2],db:5434/app?connect_timeout=3&target_session_attrs=primary",
	"postgres://db1,db2,db3/app",
	"postgres://db1:5433,db2:5434/app?host=db3,db4&port=5435,5436",
	"postgres://reader:former-one@[host?text]/app?application_name=agent",
	"host=db dbname=app user=reader application_name=agent",
	"host=db user='' password=''",
	"host=first host=last user=reader user='' password=former-one password=former-two application_name=first application_name=last sslpassword=key-passphrase",
	`host=db user='a\'b\\c' application_name='a\'b\\c' password='former-one'`,
	`host=db application_name=a\ b\'c\\d password=former-one`,
	"\thost \t= db\r\npassword = 'former-one'\vapplication_name = 'a b'\f",
	`host=db password=former-one application_name=tail\`,
	"host=db password=former-one application_name=tail\\ ",
	`host=db password=former-one\`,
	"host=db password=former-one application_name=",
	"host='db'user='reader'password='former-one'",
	"",
}

// A connection string the driver accepts must not fail extraction (D2),
// and every password the driver reads from it must be extracted.
func TestCredentialsPostgresCorpus(t *testing.T) {
	controlledPostgresEnvironment(t)
	for i, dsn := range acceptedDSNs {
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
	}
}

// A seeded property check over random DSN-like strings: whenever Inspect
// accepts one, Credentials accepts it too and finds the password Inspect
// reports. One exception is by design: a keyword key holding "://" (a URL
// that pgx read as keyword/value) fails closed, so those inputs are skipped.
func TestCredentialsPostgresProperty(t *testing.T) {
	controlledPostgresEnvironment(t)
	dir := t.TempDir()
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGPASSFILE", filepath.Join(dir, "missing-passfile"))
	t.Setenv("PGSERVICEFILE", filepath.Join(dir, "missing-servicefile"))
	t.Setenv("PGSYSCONFDIR", dir)
	urlTokens := []string{"u", ":", "@", "/", "?", "&", "=", "password=", "Password=", "sslpassword=", "pass%77ord=", "user=",
		"[::1]", "[", "]", ",", "db", ":5432", " ", "%20", "%40", "%zz", "x", "'", `\`, "app", "pw",
		"u:pw@", "u:p%40w@", ":%20@", "?password=pw", "&password=p%40w", "?password=%20"}
	keywordTokens := []string{"host=db ", "user=u ", "dbname=d ", " ", "\t", "password=", "Password=", "sslpassword=", "my password=",
		"=", "'", `\`, "pw", "a b", "x", "'q'", "://", "@", ":", "%", " postgres://u:pw@h/db?a=",
		"password=pw ", "password='p w' ", `password=p\'w `, "password='' ", `password=\ `}
	r := rand.New(rand.NewPCG(7, 11))
	const n = 4000
	accepted, withPassword, urlKeys := 0, 0, 0
	for i := 0; i < n; i++ {
		var b strings.Builder
		tokens := keywordTokens
		if r.IntN(2) == 0 {
			b.WriteString([]string{"postgres://", "postgresql://"}[r.IntN(2)])
			tokens = urlTokens
		}
		for k := r.IntN(10); k >= 0; k-- {
			b.WriteString(tokens[r.IntN(len(tokens))])
		}
		dsn := b.String()
		info, err := Engine{}.Inspect(dsn)
		if err != nil {
			continue
		}
		if !isURLDSN(dsn) && hasURLKey(dsn) {
			urlKeys++
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
	}
	t.Logf("%d accepted, %d with a password, %d URL keys skipped, of %d", accepted, withPassword, urlKeys, n)
	if accepted < n/10 || withPassword < n/20 {
		t.Fatal("weak generator")
	}
}

// hasURLKey reports whether a keyword/value string has a key holding "://".
func hasURLKey(dsn string) bool {
	found := false
	_ = scanKeywords(dsn, func(key, _, _, _ string) {
		found = found || strings.Contains(key, "://")
	})
	return found
}

func assertFixedInvalid(t *testing.T, err error) {
	t.Helper()
	var qe *qerr.Error
	if !errors.As(err, &qe) || qe.Category != qerr.Invalid || qe.Err != nil || errors.Unwrap(err) != nil ||
		err.Error() != "error[invalid]: invalid postgres connection string" {
		t.Error("expected the fixed invalid error, without a cause")
	}
}
