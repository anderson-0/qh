package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const (
	DefaultMaxRows   = 100
	MaxMaxRows       = 10_000
	DefaultTimeoutMS = 30_000
	MaxTimeoutMS     = 300_000
	DefaultPoolSize  = 5
	MaxPoolSize      = 50
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ConnInput describes a connection to create. Connection details are either
// DSN, or the separate fields Host … Params, which the engine turns into a
// connection string.
type ConnInput struct {
	Name              string
	Engine            string
	DSN               string
	Host              string
	Port              int
	Database          string
	Username          string
	Password          string
	Params            map[string]string
	TLS               *engine.TLS
	Description       string
	Mode              engine.Mode
	MaxRows           int
	TimeoutMS         int
	PoolSize          int
	AllowedNamespaces []string
}

// ConnPatch changes the non-nil fields of a connection.
type ConnPatch struct {
	DSN               *string
	Description       *string
	Mode              *engine.Mode
	MaxRows           *int
	TimeoutMS         *int
	PoolSize          *int
	AllowedNamespaces *[]string
	TLS               *engine.TLS // replaces the TLS settings
	ClearTLS          bool        // removes the TLS settings
}

// ConnInfo is what may be shown about a connection. DSN is redacted.
type ConnInfo struct {
	Name              string
	Engine            string
	Description       string
	Mode              engine.Mode
	Host              string
	Port              int
	Database          string
	User              string
	DSN               string
	TLS               *engine.TLSInfo
	MaxRows           int
	TimeoutMS         int
	PoolSize          int
	AllowedNamespaces []string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TestResult reports a successful connection test.
type TestResult struct {
	ServerVersion string
	Connect       time.Duration
}

func bounded(field string, v, def, maxV int) (int, error) {
	if v == 0 {
		return def, nil
	}
	if v < 0 || v > maxV {
		return 0, qerr.New(qerr.Invalid, fmt.Sprintf("%s must be between 1 and %d", field, maxV))
	}
	return v, nil
}

func normalize(in ConnInput) (ConnInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !nameRe.MatchString(in.Name) {
		return in, qerr.New(qerr.Invalid, fmt.Sprintf("invalid connection name %q", in.Name)).
			WithHint("Use lowercase letters, digits, _ and -, starting with a letter or digit, at most 64 characters")
	}
	in.Engine = strings.ToLower(strings.TrimSpace(in.Engine))
	eng, err := engine.Get(in.Engine)
	if err != nil {
		return in, err
	}
	if in.DSN, err = connString(eng, in); err != nil {
		return in, err
	}
	in.Host, in.Port, in.Database, in.Username, in.Password, in.Params = "", 0, "", "", "", nil
	if in.TLS != nil {
		if in.Engine == "sqlite" {
			return in, qerr.New(qerr.Invalid, "tls applies only to network databases")
		}
		if err := in.TLS.Validate(); err != nil {
			return in, err
		}
	}
	if in.Mode == "" {
		in.Mode = engine.ModeRO
	}
	if in.Mode != engine.ModeRO && in.Mode != engine.ModeRW {
		return in, qerr.New(qerr.Invalid, "mode must be ro or rw")
	}
	if in.MaxRows, err = bounded("max_rows", in.MaxRows, DefaultMaxRows, MaxMaxRows); err != nil {
		return in, err
	}
	if in.TimeoutMS, err = bounded("timeout_ms", in.TimeoutMS, DefaultTimeoutMS, MaxTimeoutMS); err != nil {
		return in, err
	}
	if in.PoolSize, err = bounded("pool_size", in.PoolSize, DefaultPoolSize, MaxPoolSize); err != nil {
		return in, err
	}
	var ns []string
	for _, n := range in.AllowedNamespaces {
		if n = strings.TrimSpace(n); n != "" {
			ns = append(ns, n)
		}
	}
	in.AllowedNamespaces = ns
	in.Description = strings.Join(strings.Fields(in.Description), " ") // one line, so Sources stays one line per connection
	return in, nil
}

// connString returns in.DSN, or builds a connection string from the
// separate fields. Giving both is an error.
func connString(eng engine.Engine, in ConnInput) (string, error) {
	dsn := strings.TrimSpace(in.DSN)
	fields := in.Host != "" || in.Port != 0 || in.Database != "" || in.Username != "" || in.Password != "" || len(in.Params) > 0
	if !fields {
		if dsn == "" {
			return "", qerr.New(qerr.Invalid, "connection string is required").
				WithHint("Give dsn, or host (database for sqlite) with port, database, username, password and params")
		}
		return dsn, nil
	}
	if dsn != "" {
		return "", qerr.New(qerr.Invalid, "give either dsn or host, port, database, username, password and params, not both")
	}
	b, ok := eng.(engine.DSNBuilder)
	if !ok {
		return "", qerr.New(qerr.Invalid, fmt.Sprintf("engine %q needs a connection string (dsn)", eng.Name()))
	}
	return b.BuildDSN(engine.DSNFields{Host: strings.TrimSpace(in.Host), Port: in.Port, Database: in.Database,
		User: in.Username, Password: in.Password, Params: in.Params})
}

// load opens a stored connection's secret.
func (s *Service) load(rec *store.Connection) (ConnInput, error) {
	pt, err := s.keys.Open(rec.Secret, []byte(rec.ID))
	if err != nil {
		return ConnInput{}, fmt.Errorf("connection %q: %w", rec.Name, err)
	}
	dsn, tls, err := decodeSecret(pt)
	if err != nil {
		return ConnInput{}, fmt.Errorf("connection %q: cannot read its sealed settings: %w", rec.Name, err)
	}
	return ConnInput{
		Name: rec.Name, Engine: rec.Engine, DSN: dsn, TLS: tls, Description: rec.Description,
		Mode: engine.Mode(rec.Mode), MaxRows: rec.MaxRows, TimeoutMS: rec.TimeoutMS, PoolSize: rec.PoolSize,
		AllowedNamespaces: rec.AllowedNamespaces,
	}, nil
}

// fill seals in.DSN and in.TLS into rec and copies the other fields.
func (s *Service) fill(rec *store.Connection, in ConnInput, info engine.DSNInfo) error {
	pt, err := encodeSecret(in.DSN, in.TLS)
	if err != nil {
		return err
	}
	sealed, err := s.keys.Seal(pt, []byte(rec.ID))
	if err != nil {
		return err
	}
	rec.Engine, rec.Secret, rec.SecretKeyID = in.Engine, sealed, s.keys.PrimaryID()
	rec.Host, rec.Port, rec.Database, rec.Username = info.Host, info.Port, info.Database, info.User
	rec.Description, rec.Mode = in.Description, string(in.Mode)
	rec.MaxRows, rec.TimeoutMS, rec.PoolSize = in.MaxRows, in.TimeoutMS, in.PoolSize
	rec.AllowedNamespaces = in.AllowedNamespaces
	rec.TLS = ""
	if in.TLS != nil {
		b, err := json.Marshal(in.TLS.Info())
		if err != nil {
			return err
		}
		rec.TLS = string(b)
	}
	return nil
}

// tlsInfo parses a stored TLS summary.
func tlsInfo(s string) *engine.TLSInfo {
	if s == "" {
		return nil
	}
	var t engine.TLSInfo
	if json.Unmarshal([]byte(s), &t) != nil {
		return nil
	}
	return &t
}

func (s *Service) testConn(ctx context.Context, ws string, eng engine.Engine, in ConnInput, info engine.DSNInfo) (*TestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := eng.Open(ctx, s.connSpec(ws, "test", 0, in.Engine, in.DSN, in.Mode, 1, in.TLS))
	if err != nil {
		return nil, scrub(err, secretsOf(in.DSN, info)...)
	}
	defer conn.Close()
	v, err := conn.Ping(ctx)
	if err != nil {
		return nil, scrub(err, secretsOf(in.DSN, info)...)
	}
	return &TestResult{ServerVersion: v, Connect: time.Since(start)}, nil
}

func toInfo(rec *store.Connection, info engine.DSNInfo) *ConnInfo {
	return &ConnInfo{
		Name: rec.Name, Engine: rec.Engine, Description: rec.Description, Mode: engine.Mode(rec.Mode),
		Host: rec.Host, Port: rec.Port, Database: rec.Database, User: rec.Username, DSN: info.Redacted, TLS: tlsInfo(rec.TLS),
		MaxRows: rec.MaxRows, TimeoutMS: rec.TimeoutMS, PoolSize: rec.PoolSize,
		AllowedNamespaces: rec.AllowedNamespaces, Version: rec.Version,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
}

// CreateConnection validates, optionally tests, seals and stores a connection.
func (s *Service) CreateConnection(ctx context.Context, ws string, in ConnInput, test bool) (*ConnInfo, *TestResult, error) {
	in, err := normalize(in)
	if err != nil {
		return nil, nil, err
	}
	if err := s.allowEngine(in.Engine); err != nil {
		return nil, nil, err
	}
	eng, _ := engine.Get(in.Engine)
	if err := s.checkDSN(eng, in.DSN); err != nil {
		return nil, nil, err
	}
	info, err := eng.Inspect(in.DSN)
	if err != nil {
		return nil, nil, scrub(err, in.DSN)
	}
	var tr *TestResult
	if test {
		if tr, err = s.testConn(ctx, ws, eng, in, info); err != nil {
			return nil, nil, err
		}
	}
	rec := &store.Connection{ID: store.NewID(), WorkspaceID: ws, Name: in.Name}
	if err := s.fill(rec, in, info); err != nil {
		return nil, nil, err
	}
	if err := s.store.CreateConnection(ctx, rec); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, nil, qerr.New(qerr.Invalid, fmt.Sprintf("a connection named %q already exists", in.Name)).
				WithHint("Edit it, or choose another name")
		}
		return nil, nil, err
	}
	return toInfo(rec, info), tr, nil
}

// UpdateConnection applies p. It tests the connection again when the
// connection string or mode changes and test is true.
func (s *Service) UpdateConnection(ctx context.Context, ws, name string, p ConnPatch, test bool) (*ConnInfo, *TestResult, error) {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return nil, nil, err
	}
	in, err := s.load(rec)
	if err != nil {
		return nil, nil, err
	}
	if p.DSN != nil {
		in.DSN = *p.DSN
	}
	if p.Description != nil {
		in.Description = *p.Description
	}
	if p.Mode != nil {
		in.Mode = *p.Mode
	}
	if p.MaxRows != nil {
		in.MaxRows = *p.MaxRows
	}
	if p.TimeoutMS != nil {
		in.TimeoutMS = *p.TimeoutMS
	}
	if p.PoolSize != nil {
		in.PoolSize = *p.PoolSize
	}
	if p.AllowedNamespaces != nil {
		in.AllowedNamespaces = *p.AllowedNamespaces
	}
	if p.ClearTLS && p.TLS != nil {
		return nil, nil, qerr.New(qerr.Invalid, "give tls or clear it, not both")
	}
	if p.ClearTLS {
		in.TLS = nil
	}
	if p.TLS != nil {
		in.TLS = p.TLS
	}
	if in, err = normalize(in); err != nil {
		return nil, nil, err
	}
	if err := s.allowEngine(in.Engine); err != nil {
		return nil, nil, err
	}
	eng, _ := engine.Get(in.Engine)
	if err := s.checkDSN(eng, in.DSN); err != nil {
		return nil, nil, err
	}
	info, err := eng.Inspect(in.DSN)
	if err != nil {
		return nil, nil, scrub(err, in.DSN)
	}
	var tr *TestResult
	if test && (p.DSN != nil || p.Mode != nil || p.TLS != nil || p.ClearTLS) {
		if tr, err = s.testConn(ctx, ws, eng, in, info); err != nil {
			return nil, nil, err
		}
	}
	if err := s.fill(rec, in, info); err != nil {
		return nil, nil, err
	}
	if err := s.store.UpdateConnection(ctx, rec); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, nil, qerr.New(qerr.Invalid, "the connection changed while it was being updated").WithHint("Retry")
		}
		return nil, nil, err
	}
	s.cache.Invalidate(rec.ID)
	return toInfo(rec, info), tr, nil
}

// DeleteConnection removes a connection and closes its pool.
func (s *Service) DeleteConnection(ctx context.Context, ws, name string) error {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return err
	}
	if err := s.store.DeleteConnection(ctx, ws, name); err != nil {
		return err
	}
	s.pools.Drop(rec.ID)
	s.cache.Invalidate(rec.ID)
	return nil
}

func (s *Service) info(rec *store.Connection) *ConnInfo {
	var di engine.DSNInfo
	if in, err := s.load(rec); err == nil {
		if eng, err := engine.Get(rec.Engine); err == nil {
			if s.checkDSN(eng, in.DSN) != nil {
				di.Redacted = "<connection string refused by this server>"
			} else {
				di, _ = eng.Inspect(in.DSN)
			}
		}
	}
	if di.Redacted == "" {
		di.Redacted = "<cannot decrypt with the current master key>"
	}
	return toInfo(rec, di)
}

// GetConnection returns one connection.
func (s *Service) GetConnection(ctx context.Context, ws, name string) (*ConnInfo, error) {
	rec, err := s.store.GetConnection(ctx, ws, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, s.unknownSource(ctx, ws, name)
	}
	if err != nil {
		return nil, err
	}
	return s.info(rec), nil
}

// ListConnections returns the workspace's connections ordered by name.
func (s *Service) ListConnections(ctx context.Context, ws string) ([]*ConnInfo, error) {
	recs, err := s.store.ListConnections(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]*ConnInfo, len(recs))
	for i, rec := range recs {
		out[i] = s.info(rec)
	}
	return out, nil
}

// TestConnection connects with a stored connection's settings.
func (s *Service) TestConnection(ctx context.Context, ws, name string) (*TestResult, error) {
	t, err := s.resolve(ctx, ws, name)
	if err != nil {
		return nil, err
	}
	return s.testConn(ctx, ws, t.eng, ConnInput{Engine: t.rec.Engine, DSN: t.spec.DSN, Mode: t.spec.Mode, TLS: t.spec.TLS}, t.info)
}

// RefreshSchema rebuilds a connection's cached schema.
func (s *Service) RefreshSchema(ctx context.Context, ws, name string) (int, time.Duration, error) {
	t, err := s.resolve(ctx, ws, name)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	s.cache.Invalidate(t.rec.ID)
	conn, release, err := s.pools.Get(ctx, t.spec)
	if err != nil {
		return 0, 0, t.scrub(err)
	}
	defer release()
	cat, err := s.catalog(ctx, conn, t)
	if err != nil {
		return 0, 0, t.scrub(err)
	}
	return cat.Count(), time.Since(start), nil
}

// TestInput connects with connection details that are not saved. The name
// may be empty.
func (s *Service) TestInput(ctx context.Context, ws string, in ConnInput) (*TestResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "test"
	}
	in, err := normalize(in)
	if err != nil {
		return nil, err
	}
	if err := s.allowEngine(in.Engine); err != nil {
		return nil, err
	}
	eng, _ := engine.Get(in.Engine)
	if err := s.checkDSN(eng, in.DSN); err != nil {
		return nil, err
	}
	info, err := eng.Inspect(in.DSN)
	if err != nil {
		return nil, scrub(err, in.DSN)
	}
	return s.testConn(ctx, ws, eng, in, info)
}
