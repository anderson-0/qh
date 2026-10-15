package mysql

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	gomysql "github.com/go-sql-driver/mysql"
)

func mysqlPasswordSetter(t *testing.T, e Engine) engine.PasswordDSNSetter {
	t.Helper()
	setter, ok := any(e).(engine.PasswordDSNSetter)
	if !ok {
		t.Fatal("mysql lacks password editing")
	}
	return setter
}

// A formatter that drops empty-user passwords or normalizes any native suffix,
// and a rewrite that changes username or option precedence, must fail here.
func TestSetPasswordMySQL(t *testing.T) {
	fixtures := []struct{ name, credentials, suffix string }{
		{"tcp", "reader:former-one@", "tcp(db:3307)/app"},
		{"ipv6", "reader:former-one@", "tcp([::1]:3307)/app"},
		{"unix", "reader:former-one@", "unix(/tmp/qh-test.sock)/app"},
		{"reserved credentials", "us er密碼:former:/@one@", "tcp(db)/app"},
		{"user only", "reader@", "tcp(db)/app"},
		{"no username", "", "tcp(db)/app"},
		{"empty user", "@", "tcp(db)/app"},
		{"empty credentials", ":@", "tcp(db)/app"},
		{"empty user password", ":former-one@", "tcp(db)/app"},
		{"default transport", "", "/"},
		{"empty transport", "reader:former-one@", "/app"},
		{"trailing whitespace", "reader:former-one@", "tcp(db)/app  \t"},
		{"escaped database", "reader:former-one@", "tcp(db)/a%2Fb%20%25%E5%AF%86"},
		{"options", "reader:former-one@", "tcp(db)/app?charset=utf8mb4,utf8&loc=Europe%2FParis&tls=true&parseTime=true&clientFoundRows=1&multiStatements=false&allowCleartextPasswords=true&timeout=2s&sql_mode=%27ANSI%27&time_zone=%27Europe%2FParis%27&custom=a+b%26c"},
		{"duplicates", "reader:former-one@", "tcp(db)/app?timeout=1s&timeout=3s&parseTime=true&parseTime=false&sql_mode=first&sql_mode=last&charset=utf8&charset=utf8mb4,utf8"},
	}
	for _, e := range []Engine{{}, {name: "mariadb"}} {
		t.Run(e.Name(), func(t *testing.T) {
			setter := mysqlPasswordSetter(t, e)
			for _, fixture := range fixtures {
				t.Run(fixture.name, func(t *testing.T) {
					dsn := fixture.credentials + fixture.suffix
					if nativePasswordFixtureSuffix(dsn) != fixture.suffix {
						t.Fatal("incorrect fixture credential boundary")
					}
					before := parseNativePasswordFixture(t, dsn)
					for _, next := range []string{"replacement", "", " p@ss:/?#&=+%'\\\n\t密碼 "} {
						got, err := setter.SetPassword(dsn, next)
						if err != nil {
							t.Fatal("rewrite failed")
						}
						gotSuffix := nativePasswordFixtureSuffix(got)
						suffix := fixture.suffix
						if gotSuffix != suffix {
							t.Fatal("non-credential suffix changed")
						}
						parsed := parseNativePasswordFixture(t, got)
						if err == nil && parsed.Passwd != next {
							t.Fatal("requested password was silently dropped")
						}
						if err == nil && parsed.User != before.User {
							t.Fatal("username changed")
						}
						if !reflect.DeepEqual(mysqlConfigWithoutPassword(before), mysqlConfigWithoutPassword(parsed)) {
							t.Fatal("non-password settings changed")
						}
						if strings.Contains(got, "former") {
							t.Fatal("superseded password remains")
						}
					}
				})
			}
			// An empty native DSN means driver defaults; adding the structural slash
			// is necessary to carry credentials without inventing a username.
			for _, next := range []string{"replacement", ""} {
				got, err := setter.SetPassword("", next)
				if err != nil {
					t.Fatal("default DSN rewrite failed")
				}
				parsed := parseNativePasswordFixture(t, got)
				if parsed.User != "" || parsed.Passwd != next {
					t.Fatal("default DSN credentials changed")
				}
				if !reflect.DeepEqual(mysqlConfigWithoutPassword(parseNativePasswordFixture(t, "")), mysqlConfigWithoutPassword(parsed)) {
					t.Fatal("default DSN settings changed")
				}
			}
			for _, scheme := range []string{"mysql", "mariadb"} {
				for _, tail := range []string{
					"://reader:former-one@db:3307/app?charset=utf8mb4,utf8&loc=Europe%2FParis&tls=true&parseTime=true&timeout=1s&timeout=3s&sql_mode=first&sql_mode=last&time_zone=Europe/Paris",
					"://us%20er%E5%AF%86:former-one@[::1]:3307/a%2Fb%20%25?custom=a+b%26c#fragment",
					"://reader@db/app", "://db/app", "://@db/app", "://:@db/app", "://:former-one@db/app", "://",
				} {
					dsn := scheme + tail
					before, err := parseRaw(dsn)
					if err != nil {
						t.Fatal("URL fixture failed to parse")
					}
					original, _ := url.Parse(dsn)
					for _, next := range []string{"replacement", "", " p@ss:/?#&=+%'\\\n\t密碼 "} {
						got, err := setter.SetPassword(dsn, next)
						if err != nil {
							t.Fatal("URL rewrite failed")
						}
						parsed, err := parseRaw(got)
						if err != nil {
							t.Fatal("rewritten URL failed to parse")
						}
						if parsed.Passwd != next {
							t.Fatal("requested URL password was silently dropped")
						}
						if parsed.User != before.User {
							t.Fatal("URL username changed")
						}
						if !reflect.DeepEqual(mysqlConfigWithoutPassword(before), mysqlConfigWithoutPassword(parsed)) {
							t.Fatal("URL settings changed")
						}
						rewritten, err := url.Parse(got)
						if err != nil {
							t.Fatal("rewritten URL syntax invalid")
						}
						original.User, rewritten.User = nil, nil
						if !reflect.DeepEqual(original, rewritten) {
							t.Fatal("non-userinfo URL components changed")
						}
						if strings.Contains(got, "former-one") {
							t.Fatal("superseded URL password remains")
						}
					}
				}
			}
		})
	}
}

// Resolving options would load this unavailable zone or require registered TLS
// and public keys. A pure rewrite preserves them without resolving anything.
func TestSetPasswordMySQLPure(t *testing.T) {
	suffix := "tcp(db)/app?loc=QH_TEST_UNAVAILABLE%2FZone&tls=qh-test-unregistered&serverPubKey=qh-test-unregistered&charset=utf8mb4,utf8&custom=%2Fqh-test-missing%2Ffile"
	for _, e := range []Engine{{}, {name: "mariadb"}} {
		setter := mysqlPasswordSetter(t, e)
		for _, dsn := range []string{"reader:former-one@" + suffix, "mysql://reader:former-one@db/app?loc=QH_TEST_UNAVAILABLE%2FZone&tls=qh-test-unregistered&serverPubKey=qh-test-unregistered", "mariadb://db/app?loc=QH_TEST_UNAVAILABLE%2FZone"} {
			for _, next := range []string{"replacement", ""} {
				got, err := setter.SetPassword(dsn, next)
				if err != nil {
					t.Fatal("pure rewrite resolved an option")
				}
				if strings.HasPrefix(dsn, "reader:") {
					if nativePasswordFixtureSuffix(got) != suffix {
						t.Fatal("pure rewrite changed suffix")
					}
				} else {
					before, _ := url.Parse(dsn)
					after, err := url.Parse(got)
					if err != nil || before.RawQuery != after.RawQuery {
						t.Fatal("pure URL rewrite changed options")
					}
					actual, _ := after.User.Password()
					if actual != next {
						t.Fatal("pure URL rewrite dropped password")
					}
				}
			}
		}
	}
}

// Malformed credential/transport boundaries and URL escapes must return a
// fixed invalid error, never a parser error containing the fixture secret.
func TestSetPasswordMySQLInvalid(t *testing.T) {
	for _, e := range []Engine{{}, {name: "mariadb"}} {
		setter := mysqlPasswordSetter(t, e)
		for _, dsn := range []string{
			"reader:former-one@tcp(db)", "reader:former-one@tcp(db/app", "reader:former-one@tcp(db)/app?custom=bad/raw",
			"reader:former-one@tcp(db)/app%zz", "mysql://reader:former-one%zz@db/app", "mariadb://reader:former-one@[::1/app", "mysql://reader:former-one@db:bad/app",
			"reader:former-one@tcp(db)/app\x00",
		} {
			got, err := setter.SetPassword(dsn, "replacement")
			assertMySQLPasswordInvalid(t, got, err)
		}
		got, err := setter.SetPassword("reader:former-one@tcp(db)/app", "replacement\x00secret")
		assertMySQLPasswordInvalid(t, got, err)
	}
}

func nativePasswordFixtureSuffix(dsn string) string {
	slash := strings.LastIndexByte(dsn, '/')
	if slash < 0 {
		return ""
	}
	return dsn[strings.LastIndexByte(dsn[:slash], '@')+1:]
}

func parseNativePasswordFixture(t *testing.T, dsn string) *gomysql.Config {
	t.Helper()
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("native DSN fixture failed to parse")
	}
	return cfg
}

func mysqlConfigWithoutPassword(cfg *gomysql.Config) gomysql.Config {
	copy := *cfg
	copy.Passwd = ""
	return copy
}

func assertMySQLPasswordInvalid(t *testing.T, got string, err error) {
	t.Helper()
	var qe *qerr.Error
	if got != "" || !errors.As(err, &qe) || qe.Category != qerr.Invalid || qe.Err != nil || qe.Message != "invalid mysql connection string" {
		t.Fatal("expected fixed invalid error without input")
	}
}
