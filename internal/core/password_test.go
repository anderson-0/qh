package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secretref"
	"github.com/anderson-0/qh/internal/store"
)

// Each fixture owns its engine, so blocking one editor never blocks another test.
// Only the external connection is simulated; sealing, store, cache and pooling
// are the production implementations.
type passwordEngine struct {
	postgres.Engine
	name        string
	mu          sync.Mutex
	opened      []engine.ConnSpec
	setterCalls atomic.Int64
}

var passwordEngineID atomic.Int64

func (e *passwordEngine) Name() string { return e.name }
func (e *passwordEngine) SetPassword(dsn, password string) (string, error) {
	e.setterCalls.Add(1)
	if strings.HasPrefix(dsn, "rewrite-failure:") {
		return "", qerr.Wrap(qerr.Invalid, "cannot edit "+dsn+" with "+password, errors.New(password))
	}
	return e.Engine.SetPassword(dsn, password)
}
func (e *passwordEngine) Inspect(dsn string) (engine.DSNInfo, error) {
	if strings.Contains(dsn, "application_name=inspect-failure") {
		return engine.DSNInfo{}, qerr.Wrap(qerr.Invalid, "cannot inspect "+dsn, errors.New(dsn)).WithHint("connection was " + dsn)
	}
	return e.Engine.Inspect(dsn)
}
func (e *passwordEngine) Open(_ context.Context, s engine.ConnSpec) (engine.Conn, error) {
	e.mu.Lock()
	e.opened = append(e.opened, s)
	e.mu.Unlock()
	info, err := e.Inspect(s.DSN)
	if err != nil {
		return nil, err
	}
	if info.Password == "rejected-password" {
		return nil, qerr.Wrap(qerr.ConnectionFailed, "cannot open "+s.DSN, errors.New(info.Password)).WithHint("password " + info.Password)
	}
	return &passwordConn{dsn: s.DSN}, nil
}
func (e *passwordEngine) specs() []engine.ConnSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]engine.ConnSpec(nil), e.opened...)
}

type passwordConn struct {
	docConn
	dsn    string
	closed atomic.Bool
}

func (c *passwordConn) Close() error { c.closed.Store(true); return nil }
func (c *passwordConn) Ping(context.Context) (string, error) {
	if c.closed.Load() {
		return "", errors.New("closed")
	}
	return "fixture 1", nil
}
func passwordFixture(t *testing.T) (*harness, *passwordEngine) {
	t.Helper()
	h := newHarness(t)
	e := &passwordEngine{name: fmt.Sprintf("qhtest-password-%d", passwordEngineID.Add(1))}
	engine.Register(e)
	passwordPGEnvironment(t)
	passwordCreate(t, h, ConnInput{Name: "pg", Engine: e.Name(), DSN: "postgres://app:initial-password@localhost/app?sslmode=disable", PoolSize: 1})
	return h, e
}
func passwordPGEnvironment(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PGPASSWORD", "PGSERVICE", "PGSERVICEFILE", "PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "missing-passfile"))
	t.Setenv("PGSERVICEFILE", filepath.Join(t.TempDir(), "missing-servicefile"))
}
func passwordCreate(t *testing.T, h *harness, in ConnInput) {
	t.Helper()
	if _, _, err := h.svc.CreateConnection(context.Background(), ws, in, false); err != nil {
		t.Fatal("fixture creation failed")
	}
}
func passwordRecord(t *testing.T, h *harness) (*store.Connection, ConnInput) {
	t.Helper()
	r, err := h.st.GetConnection(context.Background(), ws, "pg")
	if err != nil {
		t.Fatal("stored record missing")
	}
	in, err := h.svc.load(r)
	if err != nil {
		t.Fatal("cannot decrypt fixture")
	}
	return r, in
}
func passwordWant(t *testing.T, e engine.Engine, dsn, want string) {
	t.Helper()
	di, err := e.Inspect(dsn)
	if err != nil || di.Password != want {
		t.Fatal("inline password does not match expected value")
	}
}
func passwordUnchanged(t *testing.T, h *harness, before *store.Connection) {
	t.Helper()
	after, _ := passwordRecord(t, h)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed update changed stored settings, secret or version")
	}
}
func passwordPrivate(t *testing.T, v any, values ...string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal("cannot inspect public metadata")
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		escaped, _ := json.Marshal(v)
		for _, form := range []string{v, url.QueryEscape(v), url.PathEscape(v), string(escaped[1 : len(escaped)-1])} {
			if bytes.Contains(b, []byte(form)) {
				t.Fatal("credential exposed in public metadata")
			}
		}
	}
}
func TestPasswordPatchStored(t *testing.T) {
	for _, tc := range []struct {
		name        string
		next        *string
		replacement bool
	}{
		{name: "omitted"}, {name: "replace", next: ptr("replacement-password")}, {name: "exact whitespace", next: ptr("  replacement password\t ")}, {name: "clear", next: ptr("")}, {name: "new target", next: ptr("replacement-password"), replacement: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := passwordFixture(t)
			ctx := context.Background()
			// Non-password sealed and plaintext fields must survive the rewrite.
			tls := &engine.TLS{Mode: engine.TLSVerifyFull, ServerName: "db.internal"}
			ssh := &engine.SSH{Host: "bastion", User: "jump", Password: "tunnel-password", HostKey: fp}
			desc := "description"
			mode := engine.ModeRW
			rows, timeout, size := 71, 4567, 3
			ns := []string{"public", "audit"}
			if _, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{TLS: tls, SSH: ssh, Description: &desc, Mode: &mode, MaxRows: &rows, TimeoutMS: &timeout, PoolSize: &size, AllowedNamespaces: &ns}, false); err != nil {
				t.Fatal("cannot prepare settings")
			}
			before, old := passwordRecord(t, h)
			p := ConnPatch{Password: tc.next}
			if tc.replacement {
				p.DSN = ptr("postgres://other:target-password@127.0.0.1:6543/next?sslmode=disable&application_name=fixture")
			}
			info, tr, err := h.svc.UpdateConnection(ctx, ws, "pg", p, false)
			if err != nil || tr != nil || info.Version != before.Version+1 {
				t.Fatal("update contract failed")
			}
			rec, in := passwordRecord(t, h)
			eng, _ := engine.Get(in.Engine)
			want := "initial-password"
			if tc.next != nil {
				want = *tc.next
			}
			passwordWant(t, eng, in.DSN, want)
			if tc.next == nil && in.DSN != old.DSN {
				t.Fatal("omitted password rewrote the DSN")
			}
			if in.Password != "" || in.DSNRef != "" || in.PasswordRef != "" {
				t.Fatal("password escaped its inline DSN slot")
			}
			if tc.replacement {
				di, _ := eng.Inspect(in.DSN)
				if di.Host != "127.0.0.1" || di.Port != 6543 || di.Database != "next" || di.User != "other" {
					t.Fatal("password was applied to old target")
				}
			}
			old.DSN = ""
			in.DSN = ""
			if !reflect.DeepEqual(old, in) {
				t.Fatal("rewrite changed unrelated connection settings")
			}
			if tc.next != nil && *tc.next != "" && bytes.Contains(rec.Secret, []byte(*tc.next)) {
				t.Fatal("credential stored unsealed")
			}
			plain := *rec
			plain.Secret = nil
			values := []string{"initial-password", "replacement-password", "replacement password", "target-password", "tunnel-password"}
			passwordPrivate(t, plain, values...)
			passwordPrivate(t, info, values...)
			got, err := h.svc.GetConnection(ctx, ws, "pg")
			if err != nil {
				t.Fatal("get failed")
			}
			passwordPrivate(t, got, values...)
			list, err := h.svc.ListConnections(ctx, ws)
			if err != nil {
				t.Fatal("list failed")
			}
			passwordPrivate(t, list, values...)
		})
	}
}

type passwordResolver struct{ calls atomic.Int64 }

func (r *passwordResolver) Resolve(context.Context, secretref.Ref) (string, error) {
	r.calls.Add(1)
	return "", errors.New("unexpected reference resolution")
}
func TestPasswordPatchRefs(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		storedDSNRef, storedPasswordRef         bool
		patchDSN, patchDSNRef, patchPasswordRef *string
		ok                                      bool
	}{
		{name: "stored dsn", storedDSNRef: true}, {name: "stored password", storedPasswordRef: true}, {name: "stored both", storedDSNRef: true, storedPasswordRef: true},
		{name: "new dsn ref", patchDSNRef: ptr("  file:/nonexistent/qh-owned-ref  ")}, {name: "new password ref", patchPasswordRef: ptr("  file:/nonexistent/qh-owned-ref  ")},
		{name: "both new refs", patchDSNRef: ptr("env:QH_UNUSED_DSN"), patchPasswordRef: ptr("env:QH_UNUSED_PW")},
		{name: "remove password ref", storedPasswordRef: true, patchPasswordRef: ptr(""), ok: true},
		{name: "whitespace removes password ref", storedPasswordRef: true, patchPasswordRef: ptr(" \t "), ok: true},
		{name: "replace dsn ref", storedDSNRef: true, patchDSN: ptr("postgres://app:target-password@localhost/app?sslmode=disable"), ok: true},
		{name: "replace both refs", storedDSNRef: true, storedPasswordRef: true, patchDSN: ptr("postgres://app:target-password@localhost/app?sslmode=disable"), patchPasswordRef: ptr(""), ok: true},
		{name: "replace dsn leaves password ref", storedDSNRef: true, storedPasswordRef: true, patchDSN: ptr("postgres://app@localhost/app")},
		{name: "dsn ref removal needs target", storedDSNRef: true, patchDSNRef: ptr("")},
		{name: "explicit empty dsn", patchDSN: ptr("")},
		{name: "explicit whitespace dsn", patchDSN: ptr(" \t ")},
		{name: "dsn and ref conflict", patchDSN: ptr("postgres://app@localhost/app"), patchDSNRef: ptr("")},
		{name: "empty dsn and ref conflict", patchDSN: ptr(""), patchDSNRef: ptr("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, e := passwordFixture(t)
			ctx := context.Background()
			r := &passwordResolver{}
			h.svc.refs.With("file", r).With("env", r)
			rec, in := passwordRecord(t, h)
			if tc.storedDSNRef {
				in.DSN = ""
				in.DSNRef = "file:" + filepath.Join(t.TempDir(), "missing-dsn")
			}
			if tc.storedPasswordRef {
				in.PasswordRef = "file:" + filepath.Join(t.TempDir(), "missing-password")
			}
			if err := h.svc.fill(rec, in, engine.DSNInfo{}); err != nil {
				t.Fatal("cannot prepare references")
			}
			if err := h.st.UpdateConnection(ctx, rec); err != nil {
				t.Fatal("cannot store references")
			}
			before, _ := passwordRecord(t, h)
			next := "replacement-password"
			_, tr, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: &next, DSN: tc.patchDSN, DSNRef: tc.patchDSNRef, PasswordRef: tc.patchPasswordRef}, false)
			if tc.ok {
				if err != nil || tr != nil {
					t.Fatal("explicit reference removal failed")
				}
				_, after := passwordRecord(t, h)
				passwordWant(t, e, after.DSN, next)
			} else {
				if err == nil || qerr.From(err).Category != qerr.Invalid {
					t.Fatal("final reference combination was accepted")
				}
				passwordUnchanged(t, h, before)
				if e.setterCalls.Load() != 0 {
					t.Fatal("rejected reference or empty target reached password setter")
				}
			}
			if r.calls.Load() != 0 || len(e.specs()) != 0 {
				t.Fatal("reference edit resolved or connected before validation")
			}
		})
	}
}

func TestPasswordPatchRollback(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ConnPatch
		test bool
	}{
		{"authentication", ConnPatch{Password: ptr("rejected-password")}, true},
		{"rewrite", ConnPatch{Password: ptr("replacement-password"), DSN: ptr("rewrite-failure:synthetic")}, false},
		{"validation", ConnPatch{Password: ptr("replacement-password"), MaxRows: ptr(-1)}, true},
		{"inspect", ConnPatch{Password: ptr("replacement-password"), DSN: ptr("postgres://app:initial-password@localhost/app?sslmode=disable&application_name=inspect-failure")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, e := passwordFixture(t)
			ctx := context.Background()
			target, err := h.svc.resolve(ctx, ws, "pg")
			if err != nil {
				t.Fatal("resolve failed")
			}
			c, release, err := h.svc.pools.Get(ctx, target.spec)
			if err != nil {
				t.Fatal("initial pool failed")
			}
			defer release()
			before, _ := passwordRecord(t, h)
			if _, err := h.svc.cache.Get(ctx, before.ID, before.Version, func(context.Context) (*engine.Catalog, error) { return &engine.Catalog{}, nil }); err != nil {
				t.Fatal("cache prime failed")
			}
			_, _, err = h.svc.UpdateConnection(ctx, ws, "pg", tc.p, tc.test)
			if err == nil {
				t.Fatal("invalid replacement succeeded")
			}
			passwordPrivate(t, err.Error(), "rejected-password", "replacement-password", "initial-password")
			if errors.Unwrap(err) != nil {
				t.Fatal("error retains a secret-bearing cause")
			}
			passwordUnchanged(t, h, before)
			builds := 0
			if _, err := h.svc.cache.Get(ctx, before.ID, before.Version, func(context.Context) (*engine.Catalog, error) { builds++; return &engine.Catalog{}, nil }); err != nil || builds != 0 {
				t.Fatal("failed edit invalidated cached schema")
			}
			if _, err := c.Ping(ctx); err != nil {
				t.Fatal("failed update closed old pool")
			}
			again, done, err := h.svc.pools.Get(ctx, target.spec)
			if err != nil {
				t.Fatal("old pool unavailable")
			}
			defer done()
			if again != c {
				t.Fatal("failure replaced old pool")
			}
			if tc.name == "authentication" {
				specs := e.specs()
				if len(specs) != 2 {
					t.Fatal("password-only edit did not test replacement")
				}
				passwordWant(t, e, specs[1].DSN, "rejected-password")
			}
		})
	}
}
func TestPasswordPatchLifecycle(t *testing.T) {
	for _, tc := range []struct{ name, next string }{{"replace", "replacement-password"}, {"clear", ""}, {"whitespace", "  spaced-password \t"}} {
		for _, test := range []bool{false, true} {
			t.Run(tc.name+"/"+fmt.Sprint(test), func(t *testing.T) {
				h, e := passwordFixture(t)
				ctx := context.Background()
				target, err := h.svc.resolve(ctx, ws, "pg")
				if err != nil {
					t.Fatal("resolve failed")
				}
				old, release, err := h.svc.pools.Get(ctx, target.spec)
				if err != nil {
					t.Fatal("initial acquisition failed")
				}
				defer release()
				if _, err := h.svc.cache.Get(ctx, target.rec.ID, target.rec.Version, func(context.Context) (*engine.Catalog, error) { return &engine.Catalog{}, nil }); err != nil {
					t.Fatal("cache prime failed")
				}
				next := tc.next
				info, tr, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: &next}, test)
				if err != nil || info.Version != target.rec.Version+1 || (tr != nil) != test {
					t.Fatal("password test or version contract failed")
				}
				specs := e.specs()
				wantOpens := 1
				if test {
					wantOpens = 2
				}
				if len(specs) != wantOpens {
					t.Fatal("skip-test opened or password edit skipped test")
				}
				if test {
					passwordWant(t, e, specs[1].DSN, next)
					if specs[1].Password != "" {
						t.Fatal("transient runtime override used")
					}
				}
				builds := 0
				if _, err := h.svc.cache.Get(ctx, target.rec.ID, target.rec.Version, func(context.Context) (*engine.Catalog, error) { builds++; return &engine.Catalog{}, nil }); err != nil || builds != 1 {
					t.Fatal("successful update left cached schema")
				}
				if _, err := old.Ping(ctx); err != nil {
					t.Fatal("update forcibly closed in-flight old pool")
				}
				updated, err := h.svc.resolve(ctx, ws, "pg")
				if err != nil || updated.rec.Version != info.Version {
					t.Fatal("resolve reused old version")
				}
				passwordWant(t, e, updated.spec.DSN, next)
				newer, done, err := h.svc.pools.Get(ctx, updated.spec)
				if err != nil {
					t.Fatal("new pool unavailable")
				}
				defer done()
				if newer == old {
					t.Fatal("new version reused old pool")
				}
				passwordWant(t, e, newer.(*passwordConn).dsn, next)
				if old.(*passwordConn).closed.Load() {
					t.Fatal("old in-flight pool retired too early")
				}
				release()
				if !old.(*passwordConn).closed.Load() {
					t.Fatal("old pool not closed after last release")
				}
			})
		}
	}
}
func TestPasswordPatchPolicy(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		for _, name := range []string{"sqlite", "qhtest-doc"} {
			t.Run(name, func(t *testing.T) {
				h := newHarness(t)
				dsn := "doc://x"
				if name == "sqlite" {
					dsn = filepath.Join(t.TempDir(), "db")
				}
				passwordCreate(t, h, ConnInput{Name: "pg", Engine: name, DSN: dsn})
				before, _ := passwordRecord(t, h)
				_, _, err := h.svc.UpdateConnection(context.Background(), ws, "pg", ConnPatch{Password: ptr("replacement-password")}, false)
				if err == nil || qerr.From(err).Category != qerr.Invalid {
					t.Fatal("unsupported engine accepted password edit")
				}
				passwordUnchanged(t, h, before)
			})
		}
	})
	for _, tc := range []struct {
		name string
		p    ConnPatch
		opts Options
	}{
		{name: "tls conflict", p: ConnPatch{TLS: &engine.TLS{Mode: engine.TLSRequire}, ClearTLS: true}},
		{name: "ssh conflict", p: ConnPatch{SSH: &engine.SSH{}, ClearSSH: true}},
		{name: "invalid tls", p: ConnPatch{TLS: &engine.TLS{Mode: "invalid"}}},
		{name: "invalid ssh", p: ConnPatch{SSH: &engine.SSH{}}},
		{name: "deny ssh", p: ConnPatch{SSH: &engine.SSH{Host: "bastion", User: "jump", Password: "tunnel-password", HostKey: fp}}, opts: Options{DenySSH: true}},
		{name: "server dsn", p: ConnPatch{DSN: ptr("postgres://app:initial-password@localhost/app?passfile=/nonexistent/qh-owned-file")}, opts: Options{Dial: dialNowhere}},
		{name: "server tls reference", p: ConnPatch{DSN: ptr("postgres://app@localhost/app?sslcert=/nonexistent/qh-owned-cert")}, opts: Options{Dial: dialNowhere}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, e := passwordFixture(t)
			h.svc.opts = tc.opts
			before, _ := passwordRecord(t, h)
			tc.p.Password = ptr("replacement-password")
			_, _, err := h.svc.UpdateConnection(context.Background(), ws, "pg", tc.p, true)
			if err == nil {
				t.Fatal("password edit bypassed existing policy")
			}
			passwordUnchanged(t, h, before)
			if len(e.specs()) != 0 {
				t.Fatal("policy refusal reached connection")
			}
			passwordPrivate(t, err.Error(), "initial-password", "replacement-password", "tunnel-password")
		})
	}
	t.Run("network", func(t *testing.T) {
		h := newHarness(t)
		passwordPGEnvironment(t)
		passwordCreate(t, h, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://app:initial-password@127.0.0.1:5432/app?sslmode=disable"})
		var calls atomic.Int64
		h.svc.opts.Dial = func(context.Context, string, string) (net.Conn, error) {
			calls.Add(1)
			return nil, errors.New("refused by test network policy")
		}
		before, _ := passwordRecord(t, h)
		_, _, err := h.svc.UpdateConnection(context.Background(), ws, "pg", ConnPatch{Password: ptr("replacement-password")}, true)
		if err == nil || calls.Load() == 0 || !strings.Contains(err.Error(), "refused by test network policy") {
			t.Fatal("replacement bypassed network policy")
		}
		passwordPrivate(t, err.Error(), "replacement-password")
		passwordUnchanged(t, h, before)
	})
}

type passwordConflictStore struct {
	store.Store
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (s *passwordConflictStore) UpdateConnection(ctx context.Context, rec *store.Connection) error {
	pause := false
	s.once.Do(func() { pause = true })
	if pause {
		close(s.entered)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.UpdateConnection(ctx, rec)
}
func TestPasswordPatchConcurrentConflict(t *testing.T) {
	h, e := passwordFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := &passwordConflictStore{Store: h.st, entered: make(chan struct{}), resume: make(chan struct{})}
	h.svc.store = st
	inv := &invStore{SQLite: h.st}
	h.svc.inv = inv
	done := make(chan error, 1)
	finished := make(chan struct{})
	var resume sync.Once
	defer func() { cancel(); resume.Do(func() { close(st.resume) }); <-finished }()
	go func() {
		defer close(finished)
		_, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: ptr("losing-password")}, false)
		done <- err
	}()
	select {
	case <-st.entered:
	case <-ctx.Done():
		t.Fatal("first editor did not reach save")
	}
	winner, tr, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: ptr("winning-password")}, false)
	if err != nil || tr != nil {
		t.Fatal("second editor failed")
	}
	resume.Do(func() { close(st.resume) })
	select {
	case err := <-done:
		if err == nil || qerr.From(err).Category != qerr.Invalid || !strings.Contains(err.Error(), "changed while") {
			t.Fatal("stale editor was not rejected")
		}
	case <-ctx.Done():
		t.Fatal("first editor stuck")
	}
	rec, in := passwordRecord(t, h)
	if rec.Version != winner.Version {
		t.Fatal("losing editor overwrote winner version")
	}
	passwordWant(t, e, in.DSN, "winning-password")
	inv.mu.Lock()
	sent := append([]string(nil), inv.sent...)
	inv.mu.Unlock()
	if len(sent) != 1 || sent[0] != rec.ID {
		t.Fatal("conflicting editor sent invalidation or winner omitted it")
	}
}

// Keep new test services' ownership explicit, including their pools.
func passwordSharedService(t *testing.T, h *harness, st store.Store) *Service {
	t.Helper()
	p := pool.New(OpenConn, pool.Options{})
	t.Cleanup(p.Close)
	svc := New(st, h.svc.keys, p, catalog.New(time.Hour))
	t.Cleanup(svc.Close)
	return svc
}
func ptr[T any](v T) *T { return &v }
