package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/redact"
	"github.com/anderson-0/qh/internal/secretref"
)

// syntheticTLSKey stands in for a client TLS key. Nothing parses it.
const syntheticTLSKey = "-----BEGIN PRIVATE KEY-----\nc3ludGhldGljIHRscyBrZXkgZm9yIHRlc3Rz\n-----END PRIVATE KEY-----\n"

// redactEngine is a fake engine whose connection strings look like
// redact://user:password@host. Credentials returns the password as written
// and decoded, and rejects any string that holds "unparseable"; Inspect
// rejects one that holds "uninspectable". Open quotes every secret its spec
// carries: in its error when failOpen is set, otherwise in the errors of the
// echoConn it returns. It records the spec and the spec.Redact it receives.
type redactEngine struct {
	name     string
	failOpen bool
	mu       sync.Mutex
	opens    int
	spec     engine.ConnSpec
	conn     *echoConn
}

var redactEngineID atomic.Int64

// newRedactEngine registers a fresh redactEngine under a unique qhtest name,
// so no two tests share one and a rerun never registers a name twice.
func newRedactEngine(failOpen bool) *redactEngine {
	e := &redactEngine{name: fmt.Sprintf("qhtest-redact-%d", redactEngineID.Add(1)), failOpen: failOpen}
	engine.Register(e)
	return e
}

func (e *redactEngine) Name() string                           { return e.name }
func (e *redactEngine) Classify(string) (engine.Access, error) { return engine.Read, nil }
func (e *redactEngine) ReferencedObjects(string) []string      { return nil }
func (e *redactEngine) Inspect(dsn string) (engine.DSNInfo, error) {
	if !strings.HasPrefix(dsn, "redact://") || strings.Contains(dsn, "uninspectable") {
		return engine.DSNInfo{}, qerr.New(qerr.Invalid, "bad redact connection string")
	}
	return engine.DSNInfo{}, nil
}
func (e *redactEngine) Credentials(dsn string) ([]string, error) {
	rest, ok := strings.CutPrefix(dsn, "redact://")
	if !ok || strings.Contains(dsn, "unparseable") {
		return nil, errors.New("redact: cannot locate the password")
	}
	userinfo, _, hasAt := strings.Cut(rest, "@")
	_, raw, hasPassword := strings.Cut(userinfo, ":")
	if !hasAt || !hasPassword {
		return nil, nil
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return nil, errors.New("redact: cannot decode the password")
	}
	return []string{raw, decoded}, nil
}
func (e *redactEngine) Open(_ context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	leak := e.quote(spec)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opens++
	e.spec = spec
	if e.failOpen {
		err := qerr.Wrap(qerr.ConnectionFailed, "cannot open "+leak, errors.New(leak))
		if spec.Password != "" {
			err = err.WithHint("tried " + spec.Password)
		}
		return nil, err
	}
	e.conn = &echoConn{leak: leak}
	return e.conn, nil
}

// quote joins, with " / ", every nonempty secret spec carries, as a driver
// that echoes them would write them: the connection string, its decoded
// password, the override password, and the SSH and TLS secrets.
func (e *redactEngine) quote(spec engine.ConnSpec) string {
	parts := []string{spec.DSN}
	if creds, _ := e.Credentials(spec.DSN); len(creds) == 2 {
		parts = append(parts, creds[1])
	}
	parts = append(parts, spec.Password)
	if s := spec.SSH; s != nil {
		parts = append(parts, s.PrivateKey, s.Passphrase, s.Password)
	}
	if spec.TLS != nil {
		parts = append(parts, spec.TLS.Key)
	}
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " / ")
}

// last returns the spec of the latest Open and the number of opens.
func (e *redactEngine) last() (engine.ConnSpec, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.spec, e.opens
}

// echo returns the connection the latest successful Open returned.
func (e *redactEngine) echo() *echoConn {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conn
}

// markers is what n secrets quoted by redactEngine.quote become.
func markers(n int) string {
	return strings.TrimSuffix(strings.Repeat(redact.Marker+" / ", n), " / ")
}

// echoConn is a fake connection. While fail is set, each method fails with a
// timeout that quotes leak in its message, hint, unknown column and table,
// and cause. Each method returns its fixed result either way.
type echoConn struct {
	leak   string
	fail   atomic.Bool
	closes atomic.Int64
	res    engine.Result
	cat    engine.Catalog
}

func (c *echoConn) err(method string) error {
	if !c.fail.Load() {
		return nil
	}
	e := qerr.Wrap(qerr.Timeout, method+" failed: "+c.leak, errors.New(c.leak)).WithHint("near " + c.leak)
	e.UnknownColumn, e.UnknownTable = c.leak, c.leak
	return e
}

func (c *echoConn) Query(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return &c.res, c.err("query")
}
func (c *echoConn) Explain(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return &c.res, c.err("explain")
}
func (c *echoConn) Introspect(context.Context) (*engine.Catalog, error) {
	return &c.cat, c.err("introspect")
}
func (c *echoConn) Ping(context.Context) (string, error) { return "echo 1", c.err("ping") }
func (c *echoConn) Close() error                         { c.closes.Add(1); return c.err("close") }

// innerConn returns the engine connection that c wraps when c is a
// *redactedConn, and c otherwise.
func innerConn(c engine.Conn) engine.Conn {
	if r, ok := c.(*redactedConn); ok {
		return r.conn
	}
	return c
}

// Every method of the connection OpenConn returns passes results through
// and removes the generation's secrets, inline and resolved, from every
// error field. The category is kept and the cause dropped. Errors that
// come after a successful open are covered as well as the open itself.
func TestRedactedConnRedactsEveryMethod(t *testing.T) {
	ctx := context.Background()
	t.Setenv("QH_TEST_METHODS_DSN", "redact://app:resolved%2Fdb-pw@db")
	t.Setenv("QH_TEST_METHODS_PW", "resolved-pw-value-!")
	refs := secretref.NewSet(false, nil).Resolve
	for _, c := range []struct {
		name string
		spec engine.ConnSpec
		n    int // secrets the engine quotes
	}{
		{"inline dsn and key, resolved password", engine.ConnSpec{DSN: "redact://app:in%21line-db-pw@db", PasswordRef: "env:QH_TEST_METHODS_PW",
			Resolve: refs, TLS: &engine.TLS{Mode: engine.TLSRequire, Key: syntheticTLSKey}}, 4},
		{"resolved dsn and password", engine.ConnSpec{DSNRef: "env:QH_TEST_METHODS_DSN", PasswordRef: "env:QH_TEST_METHODS_PW", Resolve: refs}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newRedactEngine(false)
			c.spec.Engine = e.name
			conn, err := OpenConn(ctx, c.spec)
			if err != nil {
				t.Fatalf("open failed: %s", qerr.From(err).Category)
			}
			inner := e.echo()
			if res, err := conn.Query(ctx, "q", engine.QueryOpts{}); res != &inner.res || err != nil {
				t.Fatal("query: result not passed through")
			}
			if res, err := conn.Explain(ctx, "q", engine.QueryOpts{}); res != &inner.res || err != nil {
				t.Fatal("explain: result not passed through")
			}
			if cat, err := conn.Introspect(ctx); cat != &inner.cat || err != nil {
				t.Fatal("introspect: result not passed through")
			}
			if v, err := conn.Ping(ctx); v != "echo 1" || err != nil {
				t.Fatal("ping: result not passed through")
			}

			inner.fail.Store(true)
			want := markers(c.n)
			for _, m := range []struct {
				name string
				call func() error
			}{
				{"query", func() error { _, err := conn.Query(ctx, "q", engine.QueryOpts{}); return err }},
				{"explain", func() error { _, err := conn.Explain(ctx, "q", engine.QueryOpts{}); return err }},
				{"introspect", func() error { _, err := conn.Introspect(ctx); return err }},
				{"ping", func() error { _, err := conn.Ping(ctx); return err }},
				{"close", conn.Close},
			} {
				err := m.call()
				if err == nil {
					t.Fatalf("%s: no error", m.name)
				}
				q := qerr.From(err)
				if q.Category != qerr.Timeout {
					t.Errorf("%s: category %s, want timeout", m.name, q.Category)
				}
				if errors.Unwrap(err) != nil {
					t.Errorf("%s: the cause is still reachable", m.name)
				}
				if q.Message != m.name+" failed: "+want || q.Hint != "near "+want || q.UnknownColumn != want || q.UnknownTable != want {
					t.Errorf("%s: an error field is not redacted as expected", m.name)
				}
			}
			if n := inner.closes.Load(); n != 1 {
				t.Fatalf("engine connection closed %d times", n)
			}
		})
	}
}

// Close closes the wrapped connection exactly once and redacts its error.
func TestRedactedConnCloseOnce(t *testing.T) {
	inner := &echoConn{leak: "close-secret-!"}
	inner.fail.Store(true)
	err := (&redactedConn{conn: inner, set: redact.New("close-secret-!")}).Close()
	if n := inner.closes.Load(); n != 1 {
		t.Fatalf("closed %d times", n)
	}
	if err == nil || err.Error() != "error[timeout]: close failed: ***. near ***" || errors.Unwrap(err) != nil {
		t.Fatal("close error not redacted as expected")
	}
	ok := &echoConn{}
	if err := (&redactedConn{conn: ok, set: redact.New("close-secret-!")}).Close(); err != nil || ok.closes.Load() != 1 {
		t.Fatal("a clean close did not close once with a nil error")
	}
}

// One redactedConn serves concurrent queries; run with -race.
func TestRedactedConnConcurrentQueries(t *testing.T) {
	ctx := context.Background()
	e := newRedactEngine(false)
	conn, err := OpenConn(ctx, engine.ConnSpec{Engine: e.name, DSN: "redact://app:race%21db-pw@db", Password: "race-override-!"})
	if err != nil {
		t.Fatalf("open failed: %s", qerr.From(err).Category)
	}
	t.Cleanup(func() { conn.Close() })
	e.echo().fail.Store(true)
	want := "error[timeout]: query failed: " + markers(3) + ". near " + markers(3)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 50 {
				if _, err := conn.Query(ctx, "q", engine.QueryOpts{}); err == nil || err.Error() != want {
					t.Error("concurrent query: error not redacted as expected")
					return
				}
			}
		})
	}
	wg.Wait()
}

// displayableEngine is an engine whose connection strings are displayable
// paths. Only its type matters.
type displayableEngine struct{ engine.Engine }

func (displayableEngine) DisplayableDSN() {}

func TestInlineSecrets(t *testing.T) {
	ssh := &engine.SSH{PrivateKey: "ssh-key", Passphrase: "ssh-pp", Password: "ssh-pw"}
	tls := &engine.TLS{Mode: engine.TLSRequire, Key: "tls-key"}
	sqlite, err := engine.Get("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	all := []string{"dsn", "pw", "ssh-key", "ssh-pp", "ssh-pw", "tls-key"}
	for _, c := range []struct {
		name string
		e    engine.Engine
		ssh  *engine.SSH
		tls  *engine.TLS
		want []string
	}{
		{name: "unvalidated engine name", e: nil, ssh: ssh, tls: tls, want: all},
		{name: "network engine", e: newRedactEngine(false), ssh: ssh, tls: tls, want: all},
		{name: "displayable engine", e: displayableEngine{}, ssh: ssh, tls: tls, want: all[1:]},
		{name: "sqlite", e: sqlite, want: []string{"pw"}},
		{name: "no ssh or tls", e: newRedactEngine(false), want: []string{"dsn", "pw"}},
	} {
		if got := inlineSecrets(c.e, "dsn", "pw", c.ssh, c.tls); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: wrong secrets", c.name)
		}
	}
}
