package httpapi

import (
	"context"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
)

func TestConnectionSSHAndRefsOverREST(t *testing.T) {
	e := newEnv(t, false, nil) // local mode: every reference allowed
	k := e.key(t, "default", auth.ScopeManage)
	fp := "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"
	r := e.do(t, "POST", "/api/v1/connections?skip_test=true", k, map[string]any{
		"name": "pg", "engine": "postgres", "dsn_ref": "env:QH_PG_DSN", "password_ref": "env:QH_PG_PW",
		"ssh": map[string]any{"host": "bastion", "user": "jump", "host_key": fp, "password": "ssh-secret"},
	})
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	for _, want := range []string{`"dsn_ref":"env:QH_PG_DSN"`, `"password_ref":"env:QH_PG_PW"`, `"host_key":"` + fp + `"`, `"auth":"password"`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("body lacks %s: %s", want, r.body)
		}
	}
	if strings.Contains(r.body, "ssh-secret") {
		t.Fatal("ssh password returned")
	}
	if r := e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, map[string]any{"ssh": nil}); r.status != 200 || strings.Contains(r.body, `"ssh"`) {
		t.Fatalf("clear ssh: %d %s", r.status, r.body)
	}
	if r := e.do(t, "PATCH", "/api/v1/connections/pg?skip_test=true", k, map[string]any{"ssh": map[string]any{"host": "b", "bogus": 1}}); r.status != 400 {
		t.Fatalf("unknown ssh field: %d %s", r.status, r.body)
	}
}

func TestServerRefusesUnlistedRef(t *testing.T) {
	// A Dial hook is what puts the service in server mode (no --allow-secret-ref).
	e := newEnv(t, true, func(cfg *Config) {
		key := make([]byte, secret.KeySize)
		rand.Read(key)
		keys, _ := secret.NewKeyring(key)
		pools := pool.New(core.OpenConn, pool.Options{})
		t.Cleanup(pools.Close)
		cfg.Service = core.New(cfg.Store, keys, pools, catalog.New(time.Minute), core.Options{
			Dial: func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed },
		})
	})
	k := e.key(t, "default", auth.ScopeManage)
	expectProblem(t, e.do(t, "POST", "/api/v1/connections?skip_test=true", k, map[string]any{
		"name": "pg", "engine": "postgres", "dsn_ref": "env:HOME"}), 403, "forbidden", "secret reference env:HOME is not allowed on this server")
}
