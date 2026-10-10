package postgres

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestPoolConfigAppliesTLS(t *testing.T) {
	f := testutil.NewTLSFixture(t)
	spec := engine.ConnSpec{
		DSN: "postgres://u:p@db1.example.com:5432,db2.example.com:5433/app?sslmode=prefer",
		TLS: &engine.TLS{Mode: engine.TLSVerifyFull, CA: f.CA, Cert: f.ClientCert, Key: f.ClientKey},
	}
	cfg, err := poolConfig(spec)
	if err != nil {
		t.Fatal(err)
	}
	cc := cfg.ConnConfig
	if cc.TLSConfig == nil || cc.TLSConfig.ServerName != "db1.example.com" || len(cc.TLSConfig.Certificates) != 1 {
		t.Fatalf("main host TLS: %+v", cc.TLSConfig)
	}
	if len(cc.Fallbacks) != 1 || cc.Fallbacks[0].Host != "db2.example.com" || cc.Fallbacks[0].Port != 5433 ||
		cc.Fallbacks[0].TLSConfig == nil || cc.Fallbacks[0].TLSConfig.ServerName != "db2.example.com" {
		t.Fatalf("fallbacks: %+v", cc.Fallbacks)
	}

	spec.DSN, spec.TLS = "postgres://u:p@db1.example.com/app?sslmode=require", &engine.TLS{Mode: engine.TLSDisable}
	if cfg, err = poolConfig(spec); err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.TLSConfig != nil || len(cfg.ConnConfig.Fallbacks) != 0 {
		t.Fatalf("disable kept TLS: %+v %+v", cfg.ConnConfig.TLSConfig, cfg.ConnConfig.Fallbacks)
	}

	// Server mode strips default certificate files, but keeps the client
	// certificate given through the TLS settings.
	spec.DSN = "postgres://u:p@db1.example.com/app"
	spec.TLS = &engine.TLS{Mode: engine.TLSRequire, Cert: f.ClientCert, Key: f.ClientKey}
	spec.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("not dialled") }
	if cfg, err = poolConfig(spec); err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.TLSConfig == nil || len(cfg.ConnConfig.TLSConfig.Certificates) != 1 {
		t.Fatalf("server mode lost the client certificate: %+v", cfg.ConnConfig.TLSConfig)
	}
}

func TestServerDSNRefusesQueryInsideBrackets(t *testing.T) {
	for _, dsn := range []string{"postgres://u@[::1?]/db", "postgresql://u:p@[db.example.com?x]/db", "postgres://h1,[::1?]/db"} {
		err := Engine{}.CheckServerDSN(dsn)
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: invalid postgres connection string") {
			t.Errorf("%s: %v", dsn, err)
		}
	}
	if err := (Engine{}).CheckServerDSN("postgres://u@[::1]:5432/db?sslmode=disable"); err != nil {
		t.Fatalf("plain IPv6 host refused: %v", err)
	}
}
