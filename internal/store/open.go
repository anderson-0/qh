package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// ErrInvalidDSN reports a store connection string that is not a postgres URL.
var ErrInvalidDSN = errors.New("store: the store connection string must be a postgres:// URL")

// Open opens the store dsn names: the SQLite file in dataDir when dsn is
// empty, or a Postgres database.
func Open(ctx context.Context, dsn, dataDir string) (Store, error) {
	switch {
	case dsn == "":
		return OpenSQLite(filepath.Join(dataDir, "qh.db"))
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return OpenPostgres(ctx, dsn)
	}
	return nil, ErrInvalidDSN
}
