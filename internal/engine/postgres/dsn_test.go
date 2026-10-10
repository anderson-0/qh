package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anderson-0/qh/internal/engine"
)

func TestBuildDSNRoundTrip(t *testing.T) {
	for _, f := range []engine.DSNFields{
		{Host: "db.example.com", Port: 5433, Database: "app", User: "reader", Password: "p@ss/w:rd%?#",
			Params: map[string]string{"sslmode": "require", "application_name": "a b"}},
		{Host: "::1", Database: "my db", User: "u"},
	} {
		dsn, err := Engine{}.BuildDSN(f)
		if err != nil {
			t.Fatal(err)
		}
		info, err := Engine{}.Inspect(dsn)
		if err != nil {
			t.Fatal(err)
		}
		port := f.Port
		if port == 0 {
			port = 5432
		}
		if info.Host != f.Host || info.Port != port || info.Database != f.Database || info.User != f.User || info.Password != f.Password {
			t.Fatalf("%q round trip: %+v", dsn, info)
		}
		if f.Password != "" && strings.Contains(info.Redacted, "rd%") {
			t.Fatalf("redacted string shows the password: %q", info.Redacted)
		}
		if f.Params != nil {
			cfg, err := pgconn.ParseConfig(dsn)
			if err != nil || cfg.RuntimeParams["application_name"] != "a b" {
				t.Fatalf("params: %v %v", cfg.RuntimeParams, err)
			}
		}
	}
}
