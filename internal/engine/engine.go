// Package engine defines the interface every database engine implements and
// the engine-neutral models for results and schemas.
package engine

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

// Mode is a connection's access mode.
type Mode string

const (
	ModeRO Mode = "ro"
	ModeRW Mode = "rw"
)

// Access is what a query needs from the database.
type Access = sqlx.Access

// The access classes.
const (
	Read  = sqlx.Read
	Write = sqlx.Write
	DDL   = sqlx.DDL
)

// ConnSpec is everything an engine needs to open a connection.
type ConnSpec struct {
	ID      string // stable connection id
	Version int64  // increases on every change to the connection
	Engine  string
	DSN     string // plaintext connection string; never log it
	Mode    Mode
	// Namespaces are the connection's allowed namespaces; empty allows all.
	// Engines whose queries can name another namespace (MongoDB's
	// getSiblingDB) refuse the rest.
	Namespaces []string
	PoolSize   int
	// Dial, when set, opens every network connection to the database. Server
	// mode passes the netguard policy here. Nil uses the engine's default.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// FileRoot, when set, confines file-based databases to this directory.
	FileRoot string
	// TLS, when set, replaces the connection string's TLS settings.
	TLS *TLS
	// Server is set in server mode: engines apply their server-mode limits
	// (Postgres: the connection-string allow-list, and no password from the
	// environment or files). Dial alone no longer implies it: a local SSH
	// tunnel sets Dial too.
	Server bool
	// Password, when set, replaces the connection string's password (a
	// resolved password_ref).
	Password string
	// SSH, DSNRef and PasswordRef are resolved by core.OpenConn (the pool
	// opener) into Dial, DSN and Password; engines never read them.
	SSH         *SSH
	DSNRef      string
	PasswordRef string
	// Resolve resolves a secret reference, applying the server allow-list.
	Resolve func(ctx context.Context, ref string) (string, error)
	// CheckTarget is the network policy's address check in server mode.
	CheckTarget func(netip.Addr) bool
}

// QueryOpts controls one query.
type QueryOpts struct {
	MaxRows  int // rows to return; the engine fetches MaxRows+1 to detect truncation
	ReadOnly bool
	Timeout  time.Duration
}

// Column describes a result column.
type Column struct {
	Name string
	Type string
}

// Value is one cell, already in canonical text form.
type Value struct {
	Null   bool
	Text   string // booleans true/false, timestamps RFC 3339, JSON compact, numbers as the database prints them
	Binary bool   // binary data; Size holds the byte length and Text is empty
	Size   int
}

// Result is the outcome of one statement.
type Result struct {
	Columns      []Column
	Rows         [][]Value
	Truncated    bool
	RowsAffected int64 // -1 when the statement returned rows
	Duration     time.Duration
}

// DSNInfo is what qh may show about a connection string.
type DSNInfo struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string // only for scrubbing error messages; never displayed
	Redacted string // the connection string with the password replaced by ***
}

// Engine is a database type.
type Engine interface {
	Name() string
	// Inspect parses dsn without connecting.
	Inspect(dsn string) (DSNInfo, error)
	Open(ctx context.Context, spec ConnSpec) (Conn, error)
	// Classify returns what q needs from the database, without connecting.
	// Its errors are *qerr.Error values (category invalid).
	Classify(q string) (Access, error)
	// ReferencedObjects names the tables or collections q reads, for
	// did-you-mean hints on unknown columns.
	ReferencedObjects(q string) []string
}

// SQLEngine is implemented by engines whose query language is SQL.
type SQLEngine interface {
	Engine
	Dialect() sqlx.Dialect
}

// PasswordDSNSetter is an optional engine capability for editing an inline
// database password. SetPassword is a pure string transformation: it must not
// connect, read environment variables or files, or resolve references. An empty
// password clears every inline database-password slot, preserving driver fallback
// policy. Errors must not include either input.
type PasswordDSNSetter interface {
	SetPassword(dsn, password string) (string, error)
}

// ServerDSNChecker is implemented by engines that restrict what a connection
// string may contain in server mode. CheckServerDSN must not connect or read
// files, and its error must not quote dsn.
type ServerDSNChecker interface {
	CheckServerDSN(dsn string) error
}

// Conn is an open connection pool to one database. Methods return
// *qerr.Error values for every error they can classify.
type Conn interface {
	Query(ctx context.Context, q string, o QueryOpts) (*Result, error)
	Explain(ctx context.Context, q string, o QueryOpts) (*Result, error)
	Introspect(ctx context.Context) (*Catalog, error)
	// Ping checks the connection and returns the server version.
	Ping(ctx context.Context) (string, error)
	Close() error
}

var (
	mu       sync.RWMutex
	registry = map[string]Engine{}
)

// Register makes an engine available by name. It panics on duplicates.
func Register(e Engine) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[e.Name()]; dup {
		panic("engine: duplicate registration of " + e.Name())
	}
	registry[e.Name()] = e
}

// Get returns the engine called name.
func Get(name string) (Engine, error) {
	mu.RLock()
	e, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, qerr.New(qerr.Invalid, fmt.Sprintf("unknown engine %q", name)).
			WithHint("Available: " + strings.Join(Names(), ", "))
	}
	return e, nil
}

// Names returns the registered engine names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
