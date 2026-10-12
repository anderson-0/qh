package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
)

type env struct {
	srv   *httptest.Server
	svc   *core.Service
	st    store.Store
	admin string // admin key (server mode only)
}

// newEnv starts the API in server mode (auth on) or local mode.
func newEnv(t *testing.T, server bool, mutate func(*Config)) *env {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, secret.KeySize)
	rand.Read(key)
	keys, _ := secret.NewKeyring(key)
	pools := pool.New(core.OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	svc := core.New(st, keys, pools, catalog.New(10*time.Minute))
	cfg := Config{Service: svc, Store: st, Version: "test"}
	e := &env{svc: svc, st: st}
	if server {
		cfg.Auth = auth.NewAuthenticator(st)
		if e.admin, _, err = auth.Bootstrap(context.Background(), st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.srv = httptest.NewServer(New(cfg))
	t.Cleanup(e.srv.Close)
	return e
}

// key creates a workspace key with the given scopes.
func (e *env) key(t *testing.T, workspace string, scopes ...string) string {
	t.Helper()
	tok, _, err := auth.CreateKey(context.Background(), e.st, auth.KeyInput{Workspace: workspace, Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type reply struct {
	status int
	header http.Header
	body   string
}

func (r reply) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

// do sends a request. body may be nil, a string (sent raw) or any value (sent as JSON).
func (e *env) do(t *testing.T, method, path, token string, body any, headers ...string) reply {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return reply{status: res.StatusCode, header: res.Header, body: string(b)}
}
