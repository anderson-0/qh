package mysql

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/qerr"
)

var engines = []Engine{{}, {name: "mariadb"}}

// Failure messages name the case and counts only, never a value.
func TestCredentialsMySQL(t *testing.T) {
	for _, c := range []struct {
		name, dsn string
		want      []string
	}{
		{"native", "u:p@tcp(h:3306)/db", []string{"p"}},
		{"native reserved bytes", "u:p@:/x@tcp(h)/db", []string{"p@:/x"}},
		{"native empty user", ":pw@/db", []string{"pw"}},
		{"native user only", "u@tcp(h)/db", nil},
		{"native empty password", "u:@tcp(h)/db", nil},
		{"native no credentials", "tcp(h)/db", nil},
		{"native default", "/", nil},
		{"native blank", " ", nil},
		{"native unix socket", "u:pw@unix(/tmp/mysql.sock)/db", []string{"pw"}},
		{"native leading whitespace", " \tu:pw@tcp(h)/db", []string{"pw"}},
		{"native unknown zone", "u:pw@tcp(h)/db?loc=Invalid%2FZone", []string{"pw"}},
		{"url encoded", "mysql://u:p%40ss@db/app", []string{"p%40ss", "p@ss"}},
		{"url raw @", "mysql://u:p@ss@db/app", []string{"p@ss"}},
		{"url colon in password", "mysql://u:a:b@db/app?x=y", []string{"a:b"}},
		{"url query and fragment", "mysql://u:pw@db?x=y#f", []string{"pw"}},
		{"mariadb", "mariadb://u:pw@db:3307/app", []string{"pw"}},
		{"url user only", "mysql://u@db/app", nil},
		{"url empty password", "mysql://u:@db/app", nil},
		{"url no userinfo", "mysql://db/app", nil},
		{"url opaque", "mysql:u:p@h/db", nil},
		{"url leading whitespace", "  mysql://u:pw@db/app", []string{"pw"}},
		{"url unknown zone", "mysql://u:pw@db/app?loc=Invalid/Zone", []string{"pw"}},
		// url.Parse rejects it, so the driver reads the native form.
		{"unparseable url", "mysql://u:pw%zz@tcp(h)/db", []string{"//u:pw%zz"}},
	} {
		for _, e := range engines {
			got, err := e.Credentials(c.dsn)
			if err != nil {
				t.Errorf("%s %s: unexpected error", e.Name(), c.name)
				continue
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("%s %s: got %d values, want %d (values withheld)", e.Name(), c.name, len(got), len(c.want))
			}
		}
	}
}

func TestCredentialsMySQLInvalid(t *testing.T) {
	for _, dsn := range []string{
		// The native form needs a slash before the database.
		"u:pw@tcp(h)", "no-slash", "u:pw@", "u:pw@unix(s)?x=y",
	} {
		for _, e := range engines {
			got, err := e.Credentials(dsn)
			assertMySQLFixedInvalid(t, err)
			if got != nil {
				t.Error("credentials returned with an error")
			}
		}
	}
}

func TestCredentialsMySQLCanary(t *testing.T) {
	const canary = "canary-pw-7f3!"
	for i, dsn := range []string{
		"u:" + canary + "@tcp(db)",
		"mysql://u:" + canary + "%zz@db/app",
		"mariadb://u:" + canary + "@[::1/app",
		"mysql://u:" + canary + "@db:bad/app",
	} {
		for _, e := range engines {
			// Credentials may find the password in input that Inspect rejects;
			// only an error it does return is checked.
			creds, cerr := e.Credentials(dsn)
			_, ierr := e.Inspect(dsn)
			if ierr == nil {
				t.Errorf("case %d: Inspect accepted malformed input", i)
			}
			for name, err := range map[string]error{"Credentials": cerr, "Inspect": ierr} {
				if err == nil {
					continue
				}
				assertMySQLFixedInvalid(t, err)
				e := qerr.From(err)
				if strings.Contains(err.Error(), canary) || strings.Contains(e.Message, canary) || strings.Contains(e.Hint, canary) {
					t.Errorf("case %d: %s error contains the canary", i, name)
				}
			}
			if cerr != nil && creds != nil {
				t.Errorf("case %d: credentials returned with an error", i)
			}
		}
	}
}

// acceptedDSNs are connection strings that this package's tests show
// Inspect (parseRaw) accepting: TestInspect, the BuildDSN and parseRaw
// tests, the dial and connection tests, the testcontainers form, and the
// SetPassword fixtures.
var acceptedDSNs = []string{
	// TestInspect
	"mysql://bob:hunter22@db.example.com:3307/shop?tls=true",
	"mariadb://bob@db/shop",
	"mysql://bob:pw@[::1]/shop",
	"bob:hunter22@tcp(db:3307)/shop?tls=true",
	"bob:pw@unix(/var/run/mysqld/mysqld.sock)/shop",
	// BuildDSN tests
	"mysql://reader:p%40ss%2Fw%3Ard%25%3F%23@db.example.com:3307/app?timeout=5s",
	"mysql://u@[::1]/shop",
	"mysql://db/?charset=utf8mb4,utf8",
	// parseRaw and parseConfig tests
	"mysql://u:p@h/db?multiStatements=true&allowAllFiles=true&parseTime=true&interpolateParams=true",
	"mysql://u:p@h/db?timeout=3s",
	"mysql://u:p@db.example.com:3307/x?tls=true",
	"mysql://u:p@[::1]/x",
	"mysql://u:p@h/db?sql_select_limit=5&Max_Execution_Time=0&transaction_read_only=0&sql_mode=ANSI",
	"mysql://u:p@h/db?time_zone=Europe%2FParis",
	"mysql://u:p@h/db?time_zone=Europe/Paris",
	"mysql://u:p@h/db?charset=utf8mb4,utf8",
	// dial and connection tests; testcontainers' user:password@tcp(host:port)/db
	"mysql://u:hunter22@db.example.com:3306/db?timeout=2s",
	"u@unix(/tmp/mysql.sock)/db?timeout=2s",
	"mysql://u:hunter22@127.0.0.1:1/db?timeout=2s",
	"mysql://u:hunter22@localhost:1/db?timeout=2s",
	"qh:secret-pw-123@tcp(localhost:32768)/qh",
	"qh:wrong-pw@tcp(localhost:32768)/qh",
	// SetPassword fixtures
	"reader:former-one@tcp(db:3307)/app",
	"reader:former-one@tcp([::1]:3307)/app",
	"reader:former-one@unix(/tmp/qh-test.sock)/app",
	"us er密碼:former:/@one@tcp(db)/app",
	"reader@tcp(db)/app",
	"tcp(db)/app",
	"@tcp(db)/app",
	":@tcp(db)/app",
	":former-one@tcp(db)/app",
	"/",
	"reader:former-one@/app",
	"reader:former-one@tcp(db)/app  \t",
	"reader:former-one@tcp(db)/a%2Fb%20%25%E5%AF%86",
	"reader:former-one@tcp(db)/app?charset=utf8mb4,utf8&loc=Europe%2FParis&tls=true&parseTime=true&clientFoundRows=1&multiStatements=false&allowCleartextPasswords=true&timeout=2s&sql_mode=%27ANSI%27&time_zone=%27Europe%2FParis%27&custom=a+b%26c",
	"reader:former-one@tcp(db)/app?timeout=1s&timeout=3s&parseTime=true&parseTime=false&sql_mode=first&sql_mode=last&charset=utf8&charset=utf8mb4,utf8",
	"",
	"mysql://reader:former-one@db:3307/app?charset=utf8mb4,utf8&loc=Europe%2FParis&tls=true&parseTime=true&timeout=1s&timeout=3s&sql_mode=first&sql_mode=last&time_zone=Europe/Paris",
	"mariadb://us%20er%E5%AF%86:former-one@[::1]:3307/a%2Fb%20%25?custom=a+b%26c#fragment",
	"mysql://reader@db/app",
	"mariadb://db/app",
	"mysql://@db/app",
	"mariadb://:@db/app",
	"mysql://:former-one@db/app",
	"mariadb://",
	// The opaque form: Inspect accepts it, and the driver gets no password.
	"mysql:u:p@h/db",
	// url.Parse rejects it, so parseRaw reads the native form.
	"mysql://u:pw%zz@tcp(h)/db",
}

// A connection string the driver accepts must not fail extraction (D2),
// and every password the driver reads from it must be extracted.
func TestCredentialsMySQLCorpus(t *testing.T) {
	for i, dsn := range acceptedDSNs {
		for _, e := range engines {
			info, err := e.Inspect(dsn)
			if err != nil {
				t.Errorf("corpus %d: Inspect rejects it; remove it from acceptedDSNs", i)
				continue
			}
			creds, err := e.Credentials(dsn)
			if err != nil {
				t.Errorf("corpus %d: Credentials rejects a string Inspect accepts", i)
				continue
			}
			if info.Password != "" && !slices.Contains(creds, info.Password) {
				t.Errorf("corpus %d: the password Inspect reports was not extracted", i)
			}
		}
	}
}

// A seeded property check over random DSN-like strings: whenever Inspect
// accepts one, Credentials accepts it too and finds the password Inspect
// reports.
func TestCredentialsMySQLProperty(t *testing.T) {
	prefixes := []string{"", "", "mysql://", "mariadb://", "mysql:", "MySQL://", " mysql://", "\tmariadb://"}
	tokens := []string{"u", ":", "@", "/", "pw", "tcp(h)", "tcp(h:3306)", "unix(/s)", "(", ")", "db", "?", "x=y", "&", " ",
		"%40", "%zz", "#", "[::1]", "[", "u:pw@", ":p@w@", "u:p/w@", "u:p%40w@", "@/", "/db"}
	r := rand.New(rand.NewPCG(7, 11))
	const n = 4000
	accepted, withPassword := 0, 0
	for i := 0; i < n; i++ {
		var b strings.Builder
		b.WriteString(prefixes[r.IntN(len(prefixes))])
		for k := r.IntN(10); k >= 0; k-- {
			b.WriteString(tokens[r.IntN(len(tokens))])
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
	}
	t.Logf("%d accepted, %d with a password, of %d", accepted, withPassword, n)
	if accepted < n/10 || withPassword < n/20 {
		t.Fatal("weak generator")
	}
}

func assertMySQLFixedInvalid(t *testing.T, err error) {
	t.Helper()
	var qe *qerr.Error
	if !errors.As(err, &qe) || qe.Category != qerr.Invalid || qe.Err != nil || errors.Unwrap(err) != nil ||
		qe.Message != "invalid mysql connection string" || qe.Hint != qerr.From(invalidDSN()).Hint {
		t.Error("expected the fixed invalid error, without a cause")
	}
}
