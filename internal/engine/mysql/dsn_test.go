package mysql

import (
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
)

func TestBuildDSNRoundTrip(t *testing.T) {
	for _, f := range []engine.DSNFields{
		{Host: "db.example.com", Port: 3307, Database: "app", User: "reader", Password: "p@ss/w:rd%?#", Params: map[string]string{"timeout": "5s"}},
		{Host: "::1", Database: "shop", User: "u"},
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
			port = 3306
		}
		if info.Host != f.Host || info.Port != port || info.Database != f.Database || info.User != f.User || info.Password != f.Password {
			t.Fatalf("%q round trip: %+v", dsn, info)
		}
		if f.Params != nil {
			cfg, err := parseRaw(dsn)
			if err != nil || cfg.Timeout != 5*time.Second {
				t.Fatalf("params: %v %v", cfg, err)
			}
		}
	}
}
