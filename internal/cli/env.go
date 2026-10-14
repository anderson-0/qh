package cli

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
)

// envConnInput returns the one connection QH_DSN describes, from QH_DSN,
// QH_ENGINE, QH_NAME, QH_DESCRIPTION and QH_MODE. It reports false when
// QH_DSN is unset. Errors never include the connection string.
func envConnInput() (core.ConnInput, bool, error) {
	dsn := normalizeScheme(strings.TrimSpace(os.Getenv("QH_DSN")))
	if dsn == "" {
		return core.ConnInput{}, false, nil
	}
	eng := strings.TrimSpace(os.Getenv("QH_ENGINE"))
	if eng == "" {
		var ok bool
		if eng, ok = inferEngine(dsn); !ok {
			return core.ConnInput{}, true, qerr.New(qerr.Invalid, "cannot tell the engine from QH_DSN; set QH_ENGINE").
				WithHint("QH_ENGINE is postgres, mysql, mariadb, mongodb or sqlite; without it QH_DSN must start with postgres://, postgresql://, mysql://, mariadb://, mongodb://, mongodb+srv://, sqlite:// or file:, or be a path ending in .db, .sqlite or .sqlite3")
		}
	}
	name := strings.TrimSpace(os.Getenv("QH_NAME"))
	if name == "" {
		name = "db"
	}
	mode := engine.Mode(strings.TrimSpace(os.Getenv("QH_MODE")))
	switch mode {
	case "":
		mode = engine.ModeRO
	case engine.ModeRO, engine.ModeRW:
	default:
		return core.ConnInput{}, true, qerr.New(qerr.Invalid, "QH_MODE must be ro or rw")
	}
	return core.ConnInput{Name: name, Engine: eng, DSN: dsn, Description: os.Getenv("QH_DESCRIPTION"), Mode: mode}, true, nil
}

// inferEngine names the engine a connection string is for, judging only by
// its scheme or a SQLite file extension. The go-sql-driver form of a MySQL
// string (user:pw@tcp(host)/db) has none, so it needs QH_ENGINE.
func inferEngine(dsn string) (string, bool) {
	d := strings.ToLower(strings.TrimSpace(dsn))
	for _, c := range []struct{ engine, prefix string }{
		{"postgres", "postgres://"}, {"postgres", "postgresql://"},
		{"mysql", "mysql://"}, {"mariadb", "mariadb://"},
		{"mongodb", "mongodb://"}, {"mongodb", "mongodb+srv://"},
		{"sqlite", "sqlite:"}, {"sqlite", "file:"},
	} {
		if strings.HasPrefix(d, c.prefix) {
			return c.engine, true
		}
	}
	// An @ or a scheme means credentials or another kind of string, such as
	// user:pw@tcp(host)/app.db, or host=h password=p dbname=app.db; it is not a
	// file.
	if strings.ContainsAny(d, "@=") || strings.Contains(d, "://") {
		return "", false
	}
	for _, ext := range []string{".db", ".sqlite", ".sqlite3"} {
		if strings.HasSuffix(d, ext) {
			return "sqlite", true
		}
	}
	return "", false
}

// schemes are those inferEngine and the engines recognise.
var schemes = map[string]bool{
	"postgres": true, "postgresql": true, "mysql": true, "mariadb": true,
	"mongodb": true, "mongodb+srv": true, "sqlite": true, "file": true,
}

// normalizeScheme lower-cases dsn's scheme when it is one qh knows. Schemes
// are case-insensitive (RFC 3986), but the drivers match them exactly: pgx
// reads POSTGRES://host as key/value text and would connect to the local
// socket as the OS user.
func normalizeScheme(dsn string) string {
	i := strings.IndexByte(dsn, ':')
	if i <= 0 || !schemes[strings.ToLower(dsn[:i])] {
		return dsn
	}
	return strings.ToLower(dsn[:i]) + dsn[i:]
}

// checkEnvConflicts refuses QH_DSN together with settings that point qh at
// another store, server or workspace.
func checkEnvConflicts(server string, workspace string, workspaceSet bool) error {
	if storeDSN() != "" {
		return qerr.New(qerr.Invalid, "QH_DSN cannot be used with --store or QH_STORE_DSN").
			WithHint("QH_DSN serves one connection from an ephemeral store; unset one of them")
	}
	if server != "" || os.Getenv("QH_SERVER") != "" {
		return qerr.New(qerr.Invalid, "QH_DSN cannot be used with --server").
			WithHint("QH_DSN serves one connection from this process; remove --server and QH_SERVER")
	}
	if workspaceSet && workspace != store.DefaultWorkspace {
		return qerr.New(qerr.Invalid, "QH_DSN serves the default workspace only").WithHint("Remove --workspace and QH_WORKSPACE")
	}
	return nil
}

// openEphemeralApp opens a service on a throwaway store with a random master
// key that lives only in memory, and creates in as its only connection (not
// tested, as with --skip-test). Nothing is written under the data directory;
// the store is a temporary file that Close removes.
func openEphemeralApp(ctx context.Context, in core.ConnInput, opts core.Options) (*app, error) {
	key := make([]byte, secret.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	keys, err := secret.NewKeyring(key)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "qh-ephemeral-")
	if err != nil {
		return nil, qerr.Wrap(qerr.Invalid, "cannot create a temporary store: "+err.Error(), err).
			WithHint("QH_DSN keeps its store in a temporary directory; mount a writable /tmp, e.g. docker run --read-only --tmpfs /tmp …")
	}
	st, err := store.OpenSQLite(filepath.Join(dir, "qh.db"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	pools := pool.New(core.OpenConn, pool.Options{})
	a := &app{svc: core.New(st, keys, pools, catalog.New(10*time.Minute), opts), pools: pools, st: st, keys: keys,
		cleanup: func() { os.RemoveAll(dir) }}
	if _, _, err := a.svc.CreateConnection(ctx, store.DefaultWorkspace, in, false); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}
