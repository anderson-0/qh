package sqliteuri_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/sqlite"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

// odd is a directory name that a URI could misread: a space, non-ASCII
// letters, a literal %, a literal escape (%41) and URI delimiters. Linux,
// macOS and Windows all allow it. The engine refuses ? and #, so they are not
// in it.
const odd = "Zoë 🚀 50% %41 ;&=+,$'!~"

// The engine opens a real file, read-write and read-only, at a path with odd
// characters. On Windows this is a drive path.
func TestEngineOpensOddPath(t *testing.T) {
	path := testutil.SQLiteFixtureIn(t, filepath.Join(t.TempDir(), odd))
	engineRoundTrip(t, path)
}

// The store creates its file at a path with odd characters and opens it again.
func TestStoreOpensOddPath(t *testing.T) {
	storeRoundTrip(t, filepath.Join(t.TempDir(), odd+" #1", "qh.db"))
}

// engineRoundTrip writes a row through a read-write connection to dsn, then
// reads it back through a read-only one. Neither mode creates a missing file,
// so success means SQLite opened the fixture itself.
func engineRoundTrip(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	opts := engine.QueryOpts{MaxRows: 10, Timeout: 10 * time.Second}
	rw := openEngine(t, dsn, engine.ModeRW)
	if _, err := rw.Query(ctx, "INSERT INTO users (id, email) VALUES (4, 'w@example.com')", opts); err != nil {
		t.Fatalf("insert through %s: %v", dsn, err)
	}
	ro := openEngine(t, dsn, engine.ModeRO)
	opts.ReadOnly = true
	r, err := ro.Query(ctx, "SELECT count(*) FROM users", opts)
	if err != nil || len(r.Rows) != 1 || r.Rows[0][0].Text != "4" {
		t.Fatalf("count through %s: %+v, %v", dsn, r, err)
	}
}

func openEngine(t *testing.T, dsn string, mode engine.Mode) engine.Conn {
	t.Helper()
	c, err := sqlite.Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: dsn, Mode: mode, PoolSize: 1})
	if err != nil {
		t.Fatalf("open %s (%s): %v", dsn, mode, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// storeRoundTrip creates the store at path, saves a connection, closes it,
// checks the file is at path and reads the connection back.
func storeRoundTrip(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("create store at %s: %v", path, err)
	}
	c := &store.Connection{
		ID: store.NewID(), WorkspaceID: store.DefaultWorkspace, Name: "shop", Engine: "sqlite",
		Secret: []byte{1}, SecretKeyID: 1, Database: "shop.db", Mode: "ro", MaxRows: 100, TimeoutMS: 30000, PoolSize: 1,
	}
	err = s.CreateConnection(ctx, c)
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("save to store at %s: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file is not at %s: %v", path, err)
	}
	s, err = store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen store at %s: %v", path, err)
	}
	defer s.Close()
	list, err := s.ListConnections(ctx, store.DefaultWorkspace)
	if err != nil || len(list) != 1 || list[0].Name != "shop" {
		t.Fatalf("connections after reopen at %s: %v, %v", path, list, err)
	}
}
