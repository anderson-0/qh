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
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Store is the persistence interface. Plan 3 adds workspaces, keys and the query log.
type Store interface {
	CreateConnection(ctx context.Context, c *Connection) error
	UpdateConnection(ctx context.Context, c *Connection) error
	DeleteConnection(ctx context.Context, workspaceID, name string) error
	GetConnection(ctx context.Context, workspaceID, name string) (*Connection, error)
	ListConnections(ctx context.Context, workspaceID string) ([]*Connection, error)
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
