package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestTLSSettings(t *testing.T) {
	dsn := startDB(t, "mysql:8.4") // MySQL 8 generates a self-signed server certificate
	cipher := func(tl *engine.TLS) (string, error) {
		spec := engine.ConnSpec{ID: "tls", Version: 1, Engine: "mysql", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, TLS: tl}
		c, err := Engine{}.Open(context.Background(), spec)
		if err != nil {
			return "", err
		}
		defer c.Close()
		res, err := c.Query(context.Background(), "SHOW SESSION STATUS LIKE 'Ssl_cipher'",
			engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 10 * time.Second})
		if err != nil {
			return "", err
		}
		return res.Rows[0][1].Text, nil
	}
	if c, err := cipher(&engine.TLS{Mode: engine.TLSRequire}); err != nil || c == "" {
		t.Fatalf("require: cipher %q, %v", c, err)
	}
	if c, err := cipher(&engine.TLS{Mode: engine.TLSDisable}); err != nil || c != "" {
		t.Fatalf("disable: cipher %q, %v", c, err)
	}
	f := testutil.NewTLSFixture(t)
	if _, err := cipher(&engine.TLS{Mode: engine.TLSVerifyCA, CA: f.CA}); err == nil || !strings.HasPrefix(err.Error(), "error[connection_failed]") {
		t.Fatalf("verify-ca with a foreign CA: %v", err)
	}
}

// Exercise preparation directly until protected execution is routed in Task 3.
func TestControlUsesConfiguredConnector(t *testing.T) {
	dsn := startDB(t, "mysql:8.4")
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint("reject=", reject), func(t *testing.T) {
			cfg, err := parseConfig(dsn)
			if err != nil {
				t.Fatal("fixture config")
			}
			password := cfg.Passwd
			cfg.Passwd = "invalid-fixture-password"
			var dials atomic.Int32
			spec := engine.ConnSpec{DSN: cfg.FormatDSN(), Password: password, Mode: engine.ModeRW, PoolSize: 1, TLS: &engine.TLS{Mode: engine.TLSRequire}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
				n := dials.Add(1)
				if reject && n == 2 {
					return nil, errors.New("private-control-detail")
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}}
			opened, err := Engine{}.Open(context.Background(), spec)
			if err != nil {
				t.Fatal("open failed")
			}
			defer opened.Close()
			c := opened.(*conn)
			if dials.Load() != 1 {
				t.Fatal("control dial was eager")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			call, err := c.writes.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			sc, err := c.acquire(call.Context())
			if err != nil {
				call.Cleanup(false)
				t.Fatal(err)
			}
			err = call.Prepare(sc, c.setupSQL(10, time.Second, false))
			defer call.Cleanup(false)
			if reject {
				if err == nil || strings.Contains(err.Error(), "private-control-detail") || strings.Contains(err.Error(), password) {
					t.Fatal("unsafe preparation failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, session := range []*sql.Conn{call.query, call.control} {
				var key, cipher string
				if err := session.QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(&key, &cipher); err != nil || cipher == "" {
					t.Fatal("session not encrypted")
				}
			}
			if dials.Load() != 2 {
				t.Fatalf("dial count = %d", dials.Load())
			}
		})
	}
}
