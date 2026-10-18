package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/mongodb"
	_ "github.com/anderson-0/qh/internal/engine/mysql"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/testutil"
)

// rotateEngine is a fake engine whose connection strings are
// rotate://<token>. Credentials returns the token, so the token is a secret
// only through extraction. Its connections list a table named after the
// token and users(id, name, s3cret). They fail the query "column <name>"
// with <name> as the unknown column, and any other query with the token
// minus its last byte as the unknown table, so a table hint names the
// token. A query starting "block " first waits until release is closed.
type rotateEngine struct {
	name    string
	entered chan struct{} // receives once per blocked query
	release chan struct{}

	mu         sync.Mutex
	inspectErr error // Inspect's error while set
	credsFail  bool  // Credentials fails while set
}

var rotateEngineID atomic.Int64

// newRotateEngine registers a fresh rotateEngine under a unique qhtest name,
// so no two tests share one and a rerun never registers a name twice.
func newRotateEngine() *rotateEngine {
	e := &rotateEngine{name: fmt.Sprintf("qhtest-rotate-%d", rotateEngineID.Add(1)),
		entered: make(chan struct{}, 1), release: make(chan struct{})}
	engine.Register(e)
	return e
}

func (e *rotateEngine) Name() string                           { return e.name }
func (e *rotateEngine) Classify(string) (engine.Access, error) { return engine.Read, nil }
func (e *rotateEngine) ReferencedObjects(string) []string      { return nil }

// fail sets the errors of Inspect and Credentials.
func (e *rotateEngine) fail(inspect error, creds bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inspectErr, e.credsFail = inspect, creds
}

func (e *rotateEngine) Inspect(dsn string) (engine.DSNInfo, error) {
	e.mu.Lock()
	err := e.inspectErr
	e.mu.Unlock()
	if err != nil {
		return engine.DSNInfo{}, err
	}
	if !strings.HasPrefix(dsn, "rotate://") {
		return engine.DSNInfo{}, qerr.New(qerr.Invalid, "bad rotate connection string")
	}
	return engine.DSNInfo{Redacted: "rotate://***"}, nil
}

func (e *rotateEngine) Credentials(dsn string) ([]string, error) {
	e.mu.Lock()
	fail := e.credsFail
	e.mu.Unlock()
	token, ok := strings.CutPrefix(dsn, "rotate://")
	if !ok || fail {
		return nil, errors.New("rotate: cannot locate the token")
	}
	return []string{token}, nil
}

func (e *rotateEngine) Open(_ context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	token, _ := strings.CutPrefix(spec.DSN, "rotate://")
	return &rotateConn{e: e, token: token}, nil
}

type rotateConn struct {
	e     *rotateEngine
	token string
}

func (c *rotateConn) Query(ctx context.Context, q string, _ engine.QueryOpts) (*engine.Result, error) {
	if rest, ok := strings.CutPrefix(q, "block "); ok {
		c.e.entered <- struct{}{}
		select {
		case <-c.e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		q = rest
	}
	if column, ok := strings.CutPrefix(q, "column "); ok {
		e := qerr.New(qerr.QueryError, fmt.Sprintf("column %q does not exist", column))
		e.UnknownColumn = column
		return nil, e
	}
	table := c.token[:len(c.token)-1]
	e := qerr.New(qerr.QueryError, fmt.Sprintf("relation %q does not exist", table))
	e.UnknownTable = table
	return nil, e
}

func (c *rotateConn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	return c.Query(ctx, q, o)
}

func (c *rotateConn) Introspect(context.Context) (*engine.Catalog, error) {
	return &engine.Catalog{Namespaces: []engine.Namespace{{Name: "main", Objects: []*engine.Object{
		{Namespace: "main", Name: c.token, Kind: engine.KindTable, RowEstimate: -1},
		{Namespace: "main", Name: "users", Kind: engine.KindTable, RowEstimate: -1,
			Fields: []engine.Field{{Name: "id"}, {Name: "name"}, {Name: "s3cret"}}},
	}}}}, nil
}

func (c *rotateConn) Ping(context.Context) (string, error) { return "rotate 1", nil }
func (c *rotateConn) Close() error                         { return nil }

// A hint is redacted by the connection generation whose catalog it quotes,
// while an older generation's query is still in flight: a dsn_ref rotates
// between two queries on the same connection version.
func TestHintRedactedByItsGeneration(t *testing.T) {
	const gen1, gen2 = "gen1-tok!a", "gen2-tok!b"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHarness(t)
	e := newRotateEngine()
	t.Setenv("QH_TEST_ROTATE", "rotate://"+gen1)
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "rot", Engine: e.name, DSNRef: "env:QH_TEST_ROTATE"}, false); err != nil {
		t.Fatal(err)
	}
	rec, err := h.st.GetConnection(ctx, ws, "rot")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Query A opens generation 1 and blocks in Query.
	errA := make(chan error, 1)
	go func() {
		_, err := h.svc.Query(ctx, ws, QueryInput{Source: "rot", Query: "block table"})
		errA <- err
	}()
	select {
	case <-e.entered:
	case <-ctx.Done():
		t.Fatal("query A did not start")
	}

	// 2. The reference rotates. Drop retires A's pool without closing it.
	t.Setenv("QH_TEST_ROTATE", "rotate://"+gen2)
	h.svc.pools.Drop(rec.ID)
	h.svc.cache.Invalidate(rec.ID)

	// 3. Query B opens generation 2 and fills the catalog cache from it.
	_, err = h.svc.Query(ctx, ws, QueryInput{Source: "rot", Query: "table"})
	if err == nil || err.Error() != `error[query_error]: relation "gen2-tok!" does not exist. Did you mean ***?` {
		t.Fatal("query B: not the expected redacted hint")
	}
	testutil.RequireNoSecret(t, "query B", err.Error(), gen1, gen2)

	// 4. Both generations share the catalog cache key (id@version#gen.epoch),
	// because the record version does not change. Without this second
	// invalidation, A would build its hint from generation 2's catalog and
	// redact it with generation 1's secrets. That is an accepted limitation
	// (D6): it needs a table named after a secret, which is schema data.
	// This test checks the hint text each generation builds itself, so do
	// not remove this step.
	h.svc.cache.Invalidate(rec.ID)
	close(e.release)
	select {
	case err = <-errA:
	case <-ctx.Done():
		t.Fatal("query A did not finish")
	}
	if err == nil || err.Error() != `error[query_error]: relation "gen1-tok!" does not exist. Did you mean ***?` {
		t.Fatal("query A: not the expected redacted hint")
	}
	testutil.RequireNoSecret(t, "query A", err.Error(), gen1, gen2)
}

// A stored connection string that its engine can no longer inspect, or
// whose secrets it can no longer locate, fails closed with fixed text, and
// conn show and list label it.
func TestStoredUnparseableConnStringFailsClosed(t *testing.T) {
	const token = "stored-tok!"
	for _, c := range []struct {
		name    string
		inspect error
		creds   bool
	}{
		{"inspect fails", qerr.Wrap(qerr.Invalid, "cannot parse rotate://"+token, errors.New(token)), false},
		{"extraction fails", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			h := newHarness(t)
			e := newRotateEngine()
			if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "bad", Engine: e.name, DSN: "rotate://" + token}, false); err != nil {
				t.Fatal(err)
			}
			e.fail(c.inspect, c.creds)
			want := "error[invalid]: invalid " + e.name + " connection string"
			_, err := h.svc.Query(ctx, ws, QueryInput{Source: "bad", Query: "table"})
			if err == nil || err.Error() != want {
				t.Error("query: not the fixed error")
			}
			_, err = h.svc.TestConnection(ctx, ws, "bad")
			if err == nil || err.Error() != want {
				t.Error("test connection: not the fixed error")
			}
			if info, err := h.svc.GetConnection(ctx, ws, "bad"); err != nil || info.DSN != "<connection string cannot be parsed>" {
				t.Error("show: not the label")
			}
			if list, err := h.svc.ListConnections(ctx, ws); err != nil || len(list) != 1 || list[0].DSN != "<connection string cannot be parsed>" {
				t.Error("list: not the label")
			}
		})
	}
}

// When Inspect fails, an engine's own fixed text (category invalid, no
// cause) is kept, still redacted. Anything else becomes fixed text.
func TestRedactInspectFailures(t *testing.T) {
	const msg = "rotate strings need a host"
	h := newHarness(t)
	e := newRotateEngine()
	fixed := "error[invalid]: invalid " + e.name + " connection string"
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"fixed text", qerr.New(qerr.Invalid, msg).WithHint("Add one after d3-tok!"), "error[invalid]: " + msg + ". Add one after ***"},
		{"with a cause", qerr.Wrap(qerr.Invalid, msg, errors.New("d3-tok!")), fixed},
		{"another category", qerr.New(qerr.ConnectionFailed, msg), fixed},
		{"not classified", errors.New(msg), fixed},
	} {
		e.fail(c.err, false)
		_, _, err := h.svc.CreateConnection(context.Background(), ws, ConnInput{Name: "d3", Engine: e.name, DSN: "rotate://d3-tok!"}, false)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: not the expected text", c.name)
		}
	}
}

// A connection string whose secrets the engine cannot locate keeps the
// engine's own fixed text, which says how to fix it, and shows no part of
// the password.
func TestRedactKeepsEngineHintWhenExtractionFails(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ engine, dsn, canary, want string }{
		{"mysql", "u:canary-pw-7!@localhost", "canary-pw-7!", "error[invalid]: invalid mysql connection string. " +
			"Use mysql://user:password@host:3306/database or user:password@tcp(host:3306)/database; " +
			"special characters in the password must be percent-encoded (@ as %40, / as %2F)"},
		{"mongodb", "mongodb://u:canary:pw-7!@h:27017:9/db", "canary:pw-7!", "error[invalid]: invalid mongodb connection string. " +
			"The form is scheme://user:password@host:27017/database, where scheme is mongodb or mongodb+srv; " +
			"escape @ : / ? in the user name and password as %40 %3A %2F %3F"},
	} {
		_, err := h.svc.TestInput(context.Background(), ws, ConnInput{Engine: c.engine, DSN: c.dsn})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: not the engine's fixed text", c.engine)
		}
		if err != nil {
			testutil.RequireNoSecret(t, c.engine, err.Error(), c.canary)
		}
	}
}

// An edit's errors are redacted with the patch's own secrets only. Errors
// that echo the caller's input, such as an invalid reference, must not
// confirm a guess of a stored secret by hiding it.
func TestRedactUpdateWithPatchSecretsOnly(t *testing.T) {
	const hint = ": unknown scheme. Use env:NAME, file:/absolute/path or aws-sm://<secret-id>[#key]"
	ctx := context.Background()
	h := newHarness(t)
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u@db/app",
		SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "stored-ssh-pw!"}}, false); err != nil {
		t.Fatal(err)
	}
	_, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{PasswordRef: ptr("bad:stored-ssh-pw!")}, false)
	if err == nil || err.Error() != `error[invalid]: invalid secret reference "bad:stored-ssh-pw!"`+hint {
		t.Error("guess of a stored secret: not echoed as given")
	}
	patch := ConnPatch{PasswordRef: ptr("bad:patch-ssh-pw!"), SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "patch-ssh-pw!"}}
	_, _, err = h.svc.UpdateConnection(ctx, ws, "pg", patch, false)
	if err == nil || err.Error() != `error[invalid]: invalid secret reference "bad:***"`+hint {
		t.Error("patched secret: not redacted as expected")
	}

	// A server's connection-string check echoes a refused option key from
	// the patched string. A key that guesses the stored SSH password is
	// echoed as given too.
	srv := newServiceWith(t, openStore(t), Options{Dial: dialNowhere})
	if _, _, err := srv.CreateConnection(ctx, ws, ConnInput{Name: "mg", Engine: "mongodb", DSN: "mongodb://u@db.example/app",
		SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "stored-ssh-pw!"}}, false); err != nil {
		t.Fatal(err)
	}
	_, _, err = srv.UpdateConnection(ctx, ws, "mg", ConnPatch{DSN: ptr("mongodb://u@db.example/app?stored-ssh-pw!=1")}, false)
	if err == nil || err.Error() != "error[invalid]: this server does not accept the option stored-ssh-pw! in a mongodb connection string. "+
		"TLS certificates, keys and CAs go in the connection's tls settings, and the password in the connection string's user info or password_ref" {
		t.Error("server check, guess of a stored secret: not echoed as given")
	}
}

// structuredEngine is a fake engine that builds connection strings from
// fields, as structured://user:password@host/ with the password
// percent-encoded; for the host "refused" it fails quoting the password. It
// edits and extracts their password. Open always fails with
// connection_failed, quoting the decoded password in the message and the
// hint.
type structuredEngine struct{}

var registerStructured sync.Once

func (structuredEngine) Name() string                           { return "qhtest-structured" }
func (structuredEngine) Classify(string) (engine.Access, error) { return engine.Read, nil }
func (structuredEngine) ReferencedObjects(string) []string      { return nil }

func (structuredEngine) Inspect(dsn string) (engine.DSNInfo, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "structured" {
		return engine.DSNInfo{}, qerr.New(qerr.Invalid, "bad structured connection string")
	}
	return engine.DSNInfo{Host: u.Host, User: u.User.Username(), Redacted: "structured://" + u.User.Username() + ":***@" + u.Host}, nil
}

func (structuredEngine) BuildDSN(f engine.DSNFields) (string, error) {
	if f.Host == "refused" {
		return "", qerr.New(qerr.Invalid, "structured: host refused with "+f.Password)
	}
	return engine.BuildURL("structured", f)
}

func (structuredEngine) SetPassword(dsn, password string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return "", errors.New("structured: no user to set a password for")
	}
	u.User = url.UserPassword(u.User.Username(), password)
	if password == "" {
		u.User = url.User(u.User.Username())
	}
	return u.String(), nil
}

func (structuredEngine) Credentials(dsn string) ([]string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, errors.New("structured: cannot locate the password")
	}
	if pw, ok := u.User.Password(); ok {
		return []string{pw}, nil
	}
	return nil, nil
}

func (structuredEngine) Open(_ context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	u, err := url.Parse(spec.DSN)
	if err != nil {
		return nil, qerr.New(qerr.ConnectionFailed, "bad structured connection string")
	}
	pw, _ := u.User.Password()
	return nil, qerr.New(qerr.ConnectionFailed, "authentication failed for "+u.User.Username()+" with "+pw).
		WithHint("the server saw " + pw)
}

// A structured password, and a password edit, of one or two bytes are
// redacted from connection test errors, and the structured password from
// the errors of building the connection string too.
func TestRedactStructuredPasswords(t *testing.T) {
	registerStructured.Do(func() { engine.Register(structuredEngine{}) })
	const want = "error[connection_failed]: authentication failed for u with ***. the server saw ***"
	ctx := context.Background()
	h := newHarness(t)
	fields := ConnInput{Name: "st", Engine: "qhtest-structured", Host: "db", Username: "u", Password: "pw"}
	if _, _, err := h.svc.CreateConnection(ctx, ws, fields, true); err == nil || err.Error() != want {
		t.Error("create: not the expected redacted text")
	}
	fields.Name = ""
	if _, err := h.svc.TestInput(ctx, ws, fields); err == nil || err.Error() != want {
		t.Error("test input: not the expected redacted text")
	}
	fields.Host = "refused"
	const refused = "error[invalid]: structured: host refused with ***"
	if _, err := h.svc.TestInput(ctx, ws, fields); err == nil || err.Error() != refused {
		t.Error("test input, build failure: not the expected redacted text")
	}
	fields.Name = "st"
	if _, _, err := h.svc.CreateConnection(ctx, ws, fields, false); err == nil || err.Error() != refused {
		t.Error("create, build failure: not the expected redacted text")
	}
	fields.Host, fields.Password = "db", "old-pw!"
	if _, _, err := h.svc.CreateConnection(ctx, ws, fields, false); err != nil {
		t.Fatalf("create without a test failed: %s", qerr.From(err).Category)
	}
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "st", ConnPatch{Password: ptr("z")}, true); err == nil || err.Error() != want {
		t.Error("password edit: not the expected redacted text")
	}
}

// An unknown column is redacted before suggestions run, so the hint is built
// from the redacted name. A name that holds a secret gets weaker
// suggestions or none: here the raw name would have suggested users.s3cret,
// a near copy of the secret. A name without one keeps its hint.
func TestRedactUnknownColumnBeforeHint(t *testing.T) {
	const token = "s3cr!t"
	ctx := context.Background()
	h := newHarness(t)
	e := newRotateEngine()
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "col", Engine: e.name, DSN: "rotate://" + token}, false); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Query(ctx, ws, QueryInput{Source: "col", Query: "column " + token})
	if err == nil || err.Error() != `error[query_error]: column "***" does not exist` {
		t.Error("secret column: not the expected text")
	}
	if err != nil && qerr.From(err).UnknownColumn != "***" {
		t.Error("secret column: the unknown column is not redacted")
	}
	_, err = h.svc.Query(ctx, ws, QueryInput{Source: "col", Query: "column nmae"})
	if err == nil || err.Error() != `error[query_error]: column "nmae" does not exist. Did you mean users.name?` {
		t.Error("column without a secret: not the expected hint")
	}
}

// The password a driver would take from its environment (PGPASSWORD) is not
// redacted: it is the operator's, and redacting it at every length would
// show which bytes it holds.
func TestRedactIgnoresDriverDefaultPassword(t *testing.T) {
	passwordPGEnvironment(t)
	t.Setenv("PGPASSWORD", "e")
	h := newHarness(t)
	_, err := h.svc.TestInput(context.Background(), ws, ConnInput{Engine: "postgres",
		DSN: "postgres://u@127.0.0.1:1/app?sslmode=disable&connect_timeout=2"})
	if err == nil {
		t.Fatal("connected to port 1")
	}
	if e := qerr.From(err); e.Category != qerr.ConnectionFailed {
		t.Fatalf("not a connection failure: %s", e.Category)
	}
	// The decisive check: the driver's message ("cannot reach the
	// database: …") holds many "e" bytes, so redacting the default would
	// leave markers.
	if strings.Contains(err.Error(), "***") {
		t.Fatal("the driver default was redacted")
	}
}

// Every network engine can locate the secrets in its connection strings,
// and only SQLite's connection strings are displayable. A new engine
// without an extractor fails this test.
func TestEveryNetworkEngineExtractsCredentials(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range engine.Names() {
		e, err := engine.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		seen[name] = true
		if _, ok := e.(engine.DisplayableDSN); ok != (name == "sqlite") {
			t.Errorf("%s: implements engine.DisplayableDSN: %v", name, ok)
		}
		if name == "sqlite" || strings.HasPrefix(name, "qhtest-") {
			continue
		}
		if _, ok := e.(engine.CredentialExtractor); !ok {
			t.Errorf("%s does not implement engine.CredentialExtractor", name)
		}
	}
	for _, name := range []string{"sqlite", "postgres", "mysql", "mariadb", "mongodb"} {
		if !seen[name] {
			t.Errorf("%s is not registered in this test binary", name)
		}
	}
}
