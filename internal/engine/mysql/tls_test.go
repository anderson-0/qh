package mysql

import (
	"context"
	"strings"
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
