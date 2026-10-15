package mongodb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// These cases catch password insertion/replacement that changes the username,
// topology, database, or ordered options, or loses password bytes to escaping.
func TestSetPasswordMongoDB(t *testing.T) {
	setter, ok := any(Engine{}).(engine.PasswordDSNSetter)
	if !ok {
		t.Fatal("MongoDB engine does not implement PasswordDSNSetter")
	}
	for _, tc := range []struct {
		name, dsn, next, want string
	}{
		{"replace", "mongodb://app:old@db/shop", "next", "mongodb://app:next@db/shop"},
		{"insert", "mongodb://app@db/shop", "next", "mongodb://app:next@db/shop"},
		{"empty password", "mongodb://app:@db/shop", "next", "mongodb://app:next@db/shop"},
		{"pathless", "mongodb://app:old@db", "next", "mongodb://app:next@db"},
		{"multi host ipv6", "mongodb://app:old@db:27017,[::1]:27018,other/shop?replicaSet=rs0&tls=true", "next", "mongodb://app:next@db:27017,[::1]:27018,other/shop?replicaSet=rs0&tls=true"},
		{"encoded username", "mongodb://a%40b%3ac%2Bd:old@db/d%2Fb", "next", "mongodb://a%40b%3ac%2Bd:next@db/d%2Fb"},
		{"reserved unicode whitespace", "mongodb://app:old@db/shop", " @:/?+#%;雪\t\n ", "mongodb://app:%20%40%3A%2F%3F%2B%23%25%3B%E9%9B%AA%09%0A%20@db/shop"},
		{"replace encoded password", "mongodb://app:old%40%3A%2F%3F@db/shop", "next", "mongodb://app:next@db/shop"},
		{"ordered repeated options", "mongodb://app:old@db/shop?authSource=admin&&authMechanism=SCRAM-SHA-256;authSource=shop;&replicaSet=rs0;tls=true&appName=a%2Bb", "next", "mongodb://app:next@db/shop?authSource=admin&&authMechanism=SCRAM-SHA-256;authSource=shop;&replicaSet=rs0;tls=true&appName=a%2Bb"},
		{"unrelated credentials", "mongodb://app:old@db/shop?tlsCertificateKeyFilePassword=other&authMechanismProperties=SERVICE_NAME:other", "next", "mongodb://app:next@db/shop?tlsCertificateKeyFilePassword=other&authMechanismProperties=SERVICE_NAME:other"},
		{"userinfo query delimiter", "mongodb://app?name=x:old@db/shop", "next", "mongodb://app?name=x:next@db/shop"},
		{"clear", "mongodb://app:old@db/shop?authSource=admin", "", "mongodb://app:@db/shop?authSource=admin"},
		{"clear absent password", "mongodb://app@db/shop", "", "mongodb://app@db/shop"},
		{"clear absent credentials", "mongodb://db/shop", "", "mongodb://db/shop"},
		{"clear empty user password", "mongodb://:old@db/shop", "", "mongodb://:@db/shop"},
		{"clear empty user", "mongodb://@db/shop", "", "mongodb://@db/shop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := parseURI(tc.dsn)
			if err != nil {
				t.Fatal("invalid test fixture")
			}
			got, err := setter.SetPassword(tc.dsn, tc.next)
			if err != nil {
				t.Fatal("SetPassword unexpectedly failed")
			}
			if got != tc.want {
				t.Fatal("credential rewrite did not preserve expected URI bytes")
			}
			after, err := parseURI(got)
			if err != nil {
				t.Fatal("rewritten URI is invalid")
			}
			if after.user != before.user || after.db != before.db || after.srv != before.srv || after.hasPath != before.hasPath || !reflect.DeepEqual(after.hosts, before.hosts) {
				t.Fatal("target or username changed")
			}
			if after.password != tc.next || !reflect.DeepEqual(after.options, before.options) {
				t.Fatal("password or option preservation failed")
			}
		})
	}
}

// Driver parsing would resolve this SRV name or read the nonexistent TLS files.
// Both must remain opaque to the password transformation.
func TestSetPasswordMongoDBPure(t *testing.T) {
	setter, ok := any(Engine{}).(engine.PasswordDSNSetter)
	if !ok {
		t.Fatal("MongoDB engine does not implement PasswordDSNSetter")
	}
	for _, scheme := range []string{"mongodb", "mongodb+srv"} {
		t.Run(scheme, func(t *testing.T) {
			dsn := scheme + "://a%40b:old@qh-password-edit.invalid/shop?tls=true&tlsCAFile=/qh-password-edit-nonexistent/ca.pem;tlsCertificateKeyFile=/qh-password-edit-nonexistent/client.pem&authSource=admin&authSource=shop"
			before, err := parseURI(dsn)
			if err != nil {
				t.Fatal("invalid pure test fixture")
			}
			got, err := setter.SetPassword(dsn, "next")
			if err != nil {
				t.Fatal("pure rewrite unexpectedly failed")
			}
			after, err := parseURI(got)
			if err != nil || after.password != "next" {
				t.Fatal("pure rewrite produced an invalid password")
			}
			if after.user != before.user || after.db != before.db || after.srv != before.srv || !reflect.DeepEqual(after.hosts, before.hosts) || !reflect.DeepEqual(after.options, before.options) {
				t.Fatal("pure rewrite changed connection settings")
			}
		})
	}
}

// Invalid syntax and edits without a username must fail with a fixed error,
// without reflecting the old or supplied credentials.
func TestSetPasswordMongoDBInvalid(t *testing.T) {
	setter, ok := any(Engine{}).(engine.PasswordDSNSetter)
	if !ok {
		t.Fatal("MongoDB engine does not implement PasswordDSNSetter")
	}
	for _, tc := range []struct{ name, dsn string }{
		{"scheme", "postgres://app:synthetic-old@db/shop"},
		{"missing host", "mongodb://app:synthetic-old@"},
		{"bad username escape", "mongodb://app%zz:synthetic-old@db/shop"},
		{"bad password escape", "mongodb://app:synthetic-old%zz@db/shop"},
		{"extra colon", "mongodb://app:synthetic-old:extra@db/shop"},
		{"extra at", "mongodb://app:synthetic-old@extra@db/shop"},
		{"invalid port", "mongodb://app:synthetic-old@db:65536/shop"},
		{"srv port", "mongodb+srv://app:synthetic-old@db:27017/shop"},
		{"srv hosts", "mongodb+srv://app:synthetic-old@db,other/shop"},
		{"bad option", "mongodb://app:synthetic-old@db/shop?invalid"},
		{"bad option escape", "mongodb://app:synthetic-old@db/shop?x=%zz"},
		{"absent credentials", "mongodb://db/shop"},
		{"empty user", "mongodb://@db/shop"},
		{"empty user with password", "mongodb://:synthetic-old@db/shop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := setter.SetPassword(tc.dsn, "synthetic-next")
			if err == nil || got != "" {
				t.Fatal("invalid edit did not fail without a result")
			}
			if qerr.From(err).Category != qerr.Invalid || qerr.From(err).Message != "invalid mongodb connection string" {
				t.Fatal("invalid edit did not return the fixed invalid error")
			}
			for _, secret := range []string{tc.dsn, "synthetic-old", "synthetic-next"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("invalid edit error exposed input")
				}
			}
		})
	}
}
