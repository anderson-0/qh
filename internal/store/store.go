// Package store persists workspaces and connections.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// DefaultWorkspace is the workspace local mode uses.
const DefaultWorkspace = "default"

var (
	ErrNotFound = errors.New("store: not found")
	ErrExists   = errors.New("store: already exists")
	ErrConflict = errors.New("store: changed concurrently")
)

// Connection is a stored database connection. Secret holds the sealed
// connection string; the plaintext is never stored.
type Connection struct {
	ID                string
	WorkspaceID       string
	Name              string
	Engine            string
	Secret            []byte
	SecretKeyID       uint32
	Host              string
	Port              int
	Database          string
	Username          string
	Description       string
	Mode              string // "ro" or "rw"
	MaxRows           int
	TimeoutMS         int
	PoolSize          int
	AllowedNamespaces []string
	TLS               string // JSON summary of the TLS settings (engine.TLSInfo); "" when there are none
	SSH               string // JSON summary of the SSH tunnel (engine.SSHInfo); "" when there is none
	Refs              string // JSON {"dsn_ref","password_ref"}; "" when the connection uses none
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// QueryLogEntry is one logged query call.
type QueryLogEntry struct {
	ID             int64
	WorkspaceID    string
	ConnectionID   string
	ConnectionName string // kept so entries of deleted connections still name them
	KeyID          string // "" for local calls
	Tool           string // "query" or "explain"
	Query          string
	RowCount       int64
	DurationMS     int64
	ErrorCategory  string // "" when the call succeeded
	At             time.Time
}

// QueryLogFilter selects query log entries of one workspace.
type QueryLogFilter struct {
	WorkspaceID    string
	ConnectionName string    // "" for every connection
	Since          time.Time // zero for no lower bound
	Limit          int       // <= 0 for no limit
}

// Workspace isolates connections and keys. Its ID is its name.
type Workspace struct {
	ID        string
	CreatedAt time.Time
}

// APIKey is a stored API key. Only the SHA-256 of the key's secret is kept.
type APIKey struct {
	ID          string
	WorkspaceID string // empty for admin keys, which belong to no workspace
	Prefix      string
	Hash        []byte
	Scopes      []string
	Name        string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	CreatedBy   string // id of the key that created this one; empty for bootstrap and CLI keys
}

// Store is the persistence interface.
type Store interface {
	CreateConnection(ctx context.Context, c *Connection) error
	UpdateConnection(ctx context.Context, c *Connection) error
	DeleteConnection(ctx context.Context, workspaceID, name string) error
	GetConnection(ctx context.Context, workspaceID, name string) (*Connection, error)
	ListConnections(ctx context.Context, workspaceID string) ([]*Connection, error)
	CreateWorkspace(ctx context.Context, id string) (*Workspace, error)
	ListWorkspaces(ctx context.Context) ([]*Workspace, error)
	DeleteWorkspace(ctx context.Context, id string) error
	CreateKey(ctx context.Context, k *APIKey) error
	// CreateKeyIfNone creates k only when the store holds no API keys, atomically
	// across processes, and reports whether it did.
	CreateKeyIfNone(ctx context.Context, k *APIKey) (bool, error)
	// ImportConnection and ImportKey insert a row exactly as given, timestamps
	// and version included (qh store copy).
	ImportConnection(ctx context.Context, c *Connection) error
	ImportKey(ctx context.Context, k *APIKey) error
	GetKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error)
	ListKeys(ctx context.Context, workspaceID string) ([]*APIKey, error)
	ListAllKeys(ctx context.Context) ([]*APIKey, error)
	DeleteKey(ctx context.Context, id string) error
	TouchKey(ctx context.Context, id string, at time.Time) error
	CountKeys(ctx context.Context) (int, error)
	AppendQueryLog(ctx context.Context, e *QueryLogEntry) error
	ListQueryLog(ctx context.Context, f QueryLogFilter) ([]*QueryLogEntry, error)
	PruneQueryLog(ctx context.Context, before time.Time) (int64, error)
	ResealConnections(ctx context.Context, fn func(c *Connection) (secret []byte, keyID uint32, err error)) (int, error)
	Ping(ctx context.Context) error
	Close() error
}

// NewID returns a random 128-bit id as 32 hex characters.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b[:])
}
