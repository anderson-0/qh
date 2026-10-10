// Package engine defines the interface every database engine implements and
// the engine-neutral models for results and schemas.
package engine

import (
	"context"
	"fmt"
	"net"
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

// ConnSpec is everything an engine needs to open a connection.
type ConnSpec struct {
	ID       string // stable connection id
	Version  int64  // increases on every change to the connection
	Engine   string
	DSN      string // plaintext connection string; never log it
	Mode     Mode
	PoolSize int
	// Dial, when set, opens every network connection to the database. Server
	// mode passes the netguard policy here. Nil uses the engine's default.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// FileRoot, when set, confines file-based databases to this directory.
	FileRoot string
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
	Dialect() sqlx.Dialect
	// Inspect parses dsn without connecting.
	Inspect(dsn string) (DSNInfo, error)
	Open(ctx context.Context, spec ConnSpec) (Conn, error)
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
