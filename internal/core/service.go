// Package core applies qh's policy (read-only enforcement, row caps,
// suggestions, secret scrubbing) on top of the engines. The MCP server and
// the CLI are thin adapters over Service.
package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/sqlx"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/suggest"
)

// Service implements the agent tools and connection management.
type Service struct {
	store store.Store
	keys  *secret.Keyring
	pools *pool.Manager
	cache *catalog.Cache
	opts  Options
}

// Options configures a Service for server mode. The zero value is local mode.
type Options struct {
	// Dial, when set, opens every database network connection (netguard).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// SQLiteRoot, when set, confines each workspace's SQLite files to
	// SQLiteRoot/<workspace>/.
	SQLiteRoot string
	// SQLiteDisabled refuses SQLite connections entirely.
	SQLiteDisabled bool
	// NoQueryLog turns the query log off.
	NoQueryLog bool
}

// New returns a Service. opts (at most one) configures server mode.
func New(st store.Store, keys *secret.Keyring, pools *pool.Manager, cache *catalog.Cache, opts ...Options) *Service {
	s := &Service{store: st, keys: keys, pools: pools, cache: cache}
	if len(opts) > 0 {
		s.opts = opts[0]
	}
	return s
}

// allowEngine refuses engines this server does not permit.
func (s *Service) allowEngine(name string) error {
	if s.opts.SQLiteDisabled && name == "sqlite" {
		return qerr.New(qerr.Invalid, "SQLite connections are disabled on this server").
			WithHint("Start qh serve with --sqlite-root DIR to allow SQLite files under DIR")
	}
	return nil
}

// checkDSN applies the engine's server-mode limits on connection strings
// (for Postgres: no settings that read server files or PG* defaults). Local
// mode accepts everything the engine does.
func (s *Service) checkDSN(eng engine.Engine, dsn string) error {
	if s.opts.Dial == nil {
		return nil
	}
	if c, ok := eng.(engine.ServerDSNChecker); ok {
		return c.CheckServerDSN(dsn)
	}
	return nil
}

// connSpec builds the spec engines open for workspace ws, carrying the
// server's options. Workspace names cannot contain path separators.
func (s *Service) connSpec(ws, id string, version int64, eng, dsn string, mode engine.Mode, poolSize int, tls *engine.TLS) engine.ConnSpec {
	spec := engine.ConnSpec{ID: id, Version: version, Engine: eng, DSN: dsn, Mode: mode, PoolSize: poolSize, Dial: s.opts.Dial, TLS: tls}
	if s.opts.SQLiteRoot != "" {
		spec.FileRoot = filepath.Join(s.opts.SQLiteRoot, ws)
	}
	return spec
}

// OpenConn opens spec with its registered engine. It is the production pool.Opener.
func OpenConn(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	e, err := engine.Get(spec.Engine)
	if err != nil {
		return nil, err
	}
	return e.Open(ctx, spec)
}

// target is a stored connection with its secret opened.
type target struct {
	rec  *store.Connection
	eng  engine.Engine
	spec engine.ConnSpec
	info engine.DSNInfo
}

func (t *target) scrub(err error) error { return scrub(err, secretsOf(t.spec.DSN, t.info)...) }

func (s *Service) resolve(ctx context.Context, ws, name string) (*target, error) {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return nil, err
	}
	if err := s.allowEngine(rec.Engine); err != nil {
		return nil, err
	}
	in, err := s.load(rec)
	if err != nil {
		return nil, err
	}
	eng, err := engine.Get(rec.Engine)
	if err != nil {
		return nil, err
	}
	if err := s.checkDSN(eng, in.DSN); err != nil {
		return nil, err
	}
	info, _ := eng.Inspect(in.DSN)
	return &target{
		rec: rec, eng: eng, info: info,
		spec: s.connSpec(ws, rec.ID, rec.Version, rec.Engine, in.DSN, in.Mode, rec.PoolSize, in.TLS),
	}, nil
}

func (s *Service) unknownSource(ctx context.Context, ws, name string) error {
	e := qerr.New(qerr.UnknownSource, fmt.Sprintf("no connection named %q", name))
	list, err := s.store.ListConnections(ctx, ws)
	if err != nil || len(list) == 0 {
		return e.WithHint("No connections exist yet. Add one with `qh conn add`")
	}
	names := make([]string, len(list))
	for i, c := range list {
		names[i] = c.Name
	}
	return e.WithHint("Available: " + strings.Join(names, ", "))
}

// Sources lists the workspace's connections, one per line.
func (s *Service) Sources(ctx context.Context, ws string) (string, error) {
	list, err := s.store.ListConnections(ctx, ws)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "-- no connections. Add one with `qh conn add`\n", nil
	}
	var b strings.Builder
	for _, c := range list {
		fmt.Fprintf(&b, "%s %s %s", c.Name, c.Engine, c.Mode)
		if c.Description != "" {
			b.WriteString(" — " + c.Description)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// SchemaInput is the schema tool's input.
type SchemaInput struct {
	Source  string
	Pattern string
	Object  string
}

// Schema renders the connection's schema overview or object details.
func (s *Service) Schema(ctx context.Context, ws string, in SchemaInput) (string, error) {
	t, err := s.resolve(ctx, ws, in.Source)
	if err != nil {
		return "", err
	}
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return "", t.scrub(err)
	}
	defer release()
	cat, err := s.catalog(ctx, conn, t)
	if err != nil {
		return "", t.scrub(err)
	}
	out, n := format.Schema(cat, format.SchemaOpts{Pattern: in.Pattern, Object: in.Object})
	if n == 0 && (strings.TrimSpace(in.Pattern) != "" || strings.TrimSpace(in.Object) != "") {
		what := strings.TrimSpace(in.Object)
		if what == "" {
			what = strings.TrimSpace(in.Pattern)
		}
		return fmt.Sprintf("-- no objects match %q. %s\n", what, suggest.TableHint(cat, strings.Trim(what, "*"))), nil
	}
	return out, nil
}

// QueryInput is the query tool's input.
type QueryInput struct {
	Source  string
	Query   string
	MaxRows int
	Explain bool
}

// QueryResult runs one statement and returns its result.
func (s *Service) QueryResult(ctx context.Context, ws string, in QueryInput) (*engine.Result, error) {
	t, err := s.resolve(ctx, ws, in.Source)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := s.runQuery(ctx, t, in)
	s.logQuery(ctx, ws, t, in, res, err, time.Since(start))
	return res, err
}

// runQuery classifies, guards and executes one statement on a resolved connection.
func (s *Service) runQuery(ctx context.Context, t *target, in QueryInput) (*engine.Result, error) {
	access, err := sqlx.Classify(in.Query, t.eng.Dialect())
	if err != nil {
		return nil, err
	}
	readOnly := t.spec.Mode != engine.ModeRW
	if !in.Explain && readOnly && access != sqlx.Read {
		return nil, qerr.New(qerr.ReadOnly, fmt.Sprintf("connection %q is read-only; only statements that read data are allowed", t.rec.Name)).
			WithHint("Use a read-write connection to change data")
	}
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return nil, t.scrub(err)
	}
	defer release()
	opts := engine.QueryOpts{
		MaxRows:  effectiveMaxRows(t.rec.MaxRows, in.MaxRows),
		ReadOnly: readOnly,
		Timeout:  time.Duration(t.rec.TimeoutMS) * time.Millisecond,
	}
	var res *engine.Result
	if in.Explain {
		res, err = conn.Explain(ctx, in.Query, opts)
	} else {
		res, err = conn.Query(ctx, in.Query, opts)
	}
	if err != nil {
		return nil, t.scrub(s.withSuggestions(ctx, conn, t, in.Query, err))
	}
	// Any statement that is not a plain read may change the schema (DDL, but
	// also a DO block or a function call), so drop the cached catalog.
	if access != sqlx.Read && !in.Explain {
		s.cache.Invalidate(t.rec.ID)
	}
	return res, nil
}

// Query runs one statement and renders the result as TSV.
func (s *Service) Query(ctx context.Context, ws string, in QueryInput) (string, error) {
	res, err := s.QueryResult(ctx, ws, in)
	if err != nil {
		return "", err
	}
	return format.Result(res), nil
}

// Catalog returns a connection's schema, limited to its allowed namespaces.
func (s *Service) Catalog(ctx context.Context, ws, name string) (*engine.Catalog, error) {
	t, err := s.resolve(ctx, ws, name)
	if err != nil {
		return nil, err
	}
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return nil, t.scrub(err)
	}
	defer release()
	cat, err := s.catalog(ctx, conn, t)
	if err != nil {
		return nil, t.scrub(err)
	}
	return cat, nil
}

func effectiveMaxRows(connMax, requested int) int {
	if requested > 0 && requested < connMax {
		return requested
	}
	return connMax
}

func (s *Service) catalog(ctx context.Context, conn engine.Conn, t *target) (*engine.Catalog, error) {
	cat, err := s.cache.Get(ctx, t.rec.ID, t.rec.Version, func(ctx context.Context) (*engine.Catalog, error) {
		return conn.Introspect(ctx)
	})
	if err != nil {
		return nil, err
	}
	return cat.Only(t.rec.AllowedNamespaces), nil
}

// withSuggestions adds did-you-mean hints to unknown-column and unknown-table errors.
func (s *Service) withSuggestions(ctx context.Context, conn engine.Conn, t *target, q string, err error) error {
	e := qerr.From(err)
	if e.UnknownColumn == "" && e.UnknownTable == "" {
		return e
	}
	cat, cerr := s.catalog(ctx, conn, t)
	if cerr != nil {
		return e
	}
	out := *e
	if out.UnknownColumn != "" {
		out.Hint = suggest.ColumnHint(cat, out.UnknownColumn, sqlx.ReferencedTables(q, t.eng.Dialect()))
	} else {
		out.Hint = suggest.TableHint(cat, out.UnknownTable)
	}
	return &out
}

// secretsOf returns the strings to scrub from errors: the password, and the
// whole connection string when it contains one.
func secretsOf(dsn string, info engine.DSNInfo) []string {
	if info.Password == "" {
		return nil
	}
	return []string{dsn, info.Password}
}

// scrub returns err as a *qerr.Error with every secret replaced by ***. The
// underlying error is dropped so it cannot leak through Unwrap.
func scrub(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	e := *qerr.From(err)
	e.Err = nil
	for _, sec := range secrets {
		if len(sec) < 4 {
			continue
		}
		e.Message = strings.ReplaceAll(e.Message, sec, "***")
		e.Hint = strings.ReplaceAll(e.Hint, sec, "***")
	}
	return &e
}
