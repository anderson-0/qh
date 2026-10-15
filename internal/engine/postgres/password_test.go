package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/jackc/pgx/v5/pgconn"
)

type passwordSetter interface {
	SetPassword(string, string) (string, error)
}

func postgresPasswordSetter(t *testing.T) passwordSetter {
	t.Helper()
	setter, ok := any(Engine{}).(passwordSetter)
	if !ok {
		t.Fatal("postgres lacks password editing")
	}
	return setter
}

// Removing a non-password setting, changing duplicate precedence, or retaining
// any superseded password must break these real-driver comparisons.
func TestSetPasswordPostgres(t *testing.T) {
	setter := postgresPasswordSetter(t)
	controlledPostgresEnvironment(t)
	fixtures := []struct{ name, dsn string }{
		{"url pathless", "postgres://db?application_name=agent"},
		{"url pathless user", "postgresql://reader:former-one@db"},
		{"url default components", "postgres://"},
		{"query encoded trimmed key", "postgres://reader:former-one@db/app? pass%77ord =former-two&Password=runtime-value"},
		{"url absent credentials", "postgres://db/app?application_name=agent"},
		{"url user only", "postgres://reader@db/app?application_name=agent"},
		{"url empty credentials", "postgres://:@db/app"},
		{"url existing", "postgres://reader:former-one@db/app"},
		{"long scheme", "postgresql://reader:former-one@db/app"},
		{"query duplicates", "postgres://reader:former-one@db/app?password=former-two&pass%77ord=former-three&sslpassword=key-passphrase&application_name=first&application_name=last"},
		{"query precedence", "postgres://first:former-one@db/app?user=second&user=&dbname=other&database=last&sslmode=disable&ssl=true&ssl=false&sslmode=require&ssl=true"},
		{"query plus is literal", "postgres://reader@db/a+b?application_name=a+b&options=-c%20search_path%3Dpublic"},
		{"query trailing separator", "postgres://reader@db/app?password=former-one&application_name=agent&"},
		{"query empty", "postgres://reader@db/app?"},
		{"encoded names", "postgres://us%20er:former-one@db/a%20b?application_name=hello%20world"},
		{"socket", "postgres://reader:former-one@%2Ftmp/app"},
		{"ipv6 list", "postgres://reader:former-one@[::1]:5433,[::2],db:5434/app?connect_timeout=3&target_session_attrs=primary"},
		{"host list defaults", "postgres://db1,db2,db3/app"},
		{"host list query overrides", "postgres://db1:5433,db2:5434/app?host=db3,db4&port=5435,5436"},
		{"bracket query character", "postgres://reader:former-one@[host?text]/app?application_name=agent"},
		{"keyword absent", "host=db dbname=app user=reader application_name=agent"},
		{"keyword empty", "host=db user='' password=''"},
		{"keyword duplicates", "host=first host=last user=reader user='' password=former-one password=former-two application_name=first application_name=last sslpassword=key-passphrase"},
		{"keyword escaped", `host=db user='a\'b\\c' application_name='a\'b\\c' password='former-one'`},
		{"keyword unquoted escapes", `host=db application_name=a\ b\'c\\d password=former-one`},
		{"keyword whitespace", "\thost \t= db\r\npassword = 'former-one'\vapplication_name = 'a b'\f"},
		{"keyword trailing slash", `host=db password=former-one application_name=tail\`},
		{"keyword trailing escaped space", "host=db password=former-one application_name=tail\\ "},
		{"keyword trailing password slash", `host=db password=former-one\`},
		{"keyword final empty", "host=db password=former-one application_name="},
		{"keyword adjacent quoted", "host='db'user='reader'password='former-one'"},
		{"keyword empty input", ""},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			before := parsePasswordFixture(t, fixture.dsn)
			for _, password := range []string{"replacement", "", " p@ss:/?#&=+%'\\\n\t密碼 "} {
				got, err := setter.SetPassword(fixture.dsn, password)
				if err != nil {
					t.Fatal("rewrite failed")
				}
				after := parsePasswordFixture(t, got)
				if after.Password != password {
					t.Fatal("replacement password did not round trip")
				}
				for _, old := range []string{"former-one", "former-two", "former-three"} {
					if strings.Contains(got, old) {
						t.Fatal("superseded password slot remains")
					}
				}
				if !reflect.DeepEqual(passwordConfigWithoutSecrets(before), passwordConfigWithoutSecrets(after)) {
					t.Fatal("non-password settings changed")
				}
			}
		})
	}
}

// Resolving configuration would fail these nonexistent paths and would leak
// environment settings into a serialized DSN. The setter must only edit text.
func TestSetPasswordPostgresPure(t *testing.T) {
	setter := postgresPasswordSetter(t)
	controlledPostgresEnvironment(t)
	t.Setenv("PGHOST", "environment-host")
	t.Setenv("PGUSER", "environment-user")
	t.Setenv("PGPASSWORD", "environment-password")
	t.Setenv("PGAPPNAME", "environment-app")
	fixtures := []struct {
		dsn  string
		keep []string
	}{
		{"service=missing servicefile='/qh-test-missing/service' sslcert='/qh-test-missing/client.crt' sslkey='/qh-test-missing/client.key' sslrootcert='/qh-test-missing/root.crt' passfile='/qh-test-missing/pass' sslpassword=key-passphrase", []string{"service=", "servicefile=", "/qh-test-missing/service", "/qh-test-missing/client.crt", "/qh-test-missing/client.key", "/qh-test-missing/root.crt", "/qh-test-missing/pass", "key-passphrase"}},
		{"postgres://reader@db/app?service=missing&servicefile=%2Fqh-test-missing%2Fservice&sslcert=%2Fqh-test-missing%2Fclient.crt&sslkey=%2Fqh-test-missing%2Fclient.key&sslrootcert=%2Fqh-test-missing%2Froot.crt&sslpassword=key-passphrase", []string{"service=missing", "servicefile=%2Fqh-test-missing%2Fservice", "sslcert=%2Fqh-test-missing%2Fclient.crt", "sslkey=%2Fqh-test-missing%2Fclient.key", "sslrootcert=%2Fqh-test-missing%2Froot.crt", "sslpassword=key-passphrase"}},
		{"postgres://", []string{"postgres://"}},
		{"", nil},
	}
	for i, fixture := range fixtures {
		for _, password := range []string{"replacement", ""} {
			got, err := setter.SetPassword(fixture.dsn, password)
			if err != nil {
				t.Fatalf("fixture %d: pure rewrite failed", i)
			}
			for _, keep := range fixture.keep {
				if !strings.Contains(got, keep) {
					t.Fatalf("fixture %d: explicit setting lost", i)
				}
			}
			for _, forbidden := range []string{"environment-", "connect_timeout", "5432", "sslmode", "timezone"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("fixture %d: omitted setting materialized", i)
				}
			}
		}
	}
}

// Malformed syntax and unrepresentable NUL credentials must never return a
// credential-bearing parser error or a partially rewritten DSN.
func TestSetPasswordPostgresInvalid(t *testing.T) {
	setter := postgresPasswordSetter(t)
	for _, dsn := range []string{
		"host=db password='former-one", "host db password=former-one", "=former-one", "host=db\x00 password=former-one",
		"postgres://reader:former-one@[::1/app", "postgres://reader:former-one@[]/app", "postgres://reader:former-one@[::1]x/app",
		"postgres://reader:former-one@db/app?password=former-two=extra", "postgres://reader:former-one@db/app?password", "postgres://reader:former-one@db/app?password=former-two&&user=x",
		"postgres://reader:former-one@db/app?pass%00word=x", "postgres://reader:former-one@db/app?password=%zz", "postgres://reader:former-one@db%zz/app", "postgres://reader:former-one@db/a%00b", "postgres://reader:former-one@db/a b",
	} {
		got, err := setter.SetPassword(dsn, "replacement")
		assertPasswordInvalid(t, got, err)
	}
	for _, dsn := range []string{"host=db password=former-one", "postgres://reader:former-one@db/app"} {
		got, err := setter.SetPassword(dsn, "replacement\x00secret")
		assertPasswordInvalid(t, got, err)
	}
}

func assertPasswordInvalid(t *testing.T, got string, err error) {
	t.Helper()
	var qe *qerr.Error
	if got != "" || !errors.As(err, &qe) || qe.Category != qerr.Invalid || qe.Err != nil || err.Error() != "error[invalid]: invalid postgres connection string" {
		t.Fatal("expected fixed invalid error without input")
	}
}

func controlledPostgresEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") {
			t.Setenv(key, "")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PGPASSFILE", filepath.Join(home, "no-passfile"))
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGUSER", "fixture-user")
}

func parsePasswordFixture(t *testing.T, dsn string) *pgconn.Config {
	t.Helper()
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal("controlled DSN fixture failed to parse")
	}
	return cfg
}

func passwordConfigWithoutSecrets(cfg *pgconn.Config) pgconn.Config {
	copy := *cfg
	copy.Password = ""
	// Driver-supplied callbacks are distinct closures on each parse, while all
	// data fields (including TLS and fallback settings) remain comparable.
	value := reflect.ValueOf(&copy).Elem()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.CanSet() && field.Kind() == reflect.Func {
			field.SetZero()
		}
	}
	return copy
}
