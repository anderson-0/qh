package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestConformance(t *testing.T) {
	enginetest.Run(t, enginetest.Harness{
		Engine: Engine{},
		Setup: func(t *testing.T) (engine.ConnSpec, engine.ConnSpec) {
			path := testutil.SQLiteFixture(t)
			return engine.ConnSpec{ID: "ro", Version: 1, Engine: "sqlite", DSN: path, Mode: engine.ModeRO, PoolSize: 2},
				engine.ConnSpec{ID: "rw", Version: 1, Engine: "sqlite", DSN: "sqlite://" + path, Mode: engine.ModeRW, PoolSize: 2}
		},
		SlowQuery: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c",
		ReadOnlyEscapes: []string{
			"ATTACH DATABASE 'qh-evil-attach.db' AS evil",
			"VACUUM INTO 'qh-evil-vacuum.db'",
			"PRAGMA query_only = 0",
			"EXPLAIN PRAGMA query_only = 0",
		},
		GoldenPrefix: "sqlite",
	})
	for _, f := range []string{"qh-evil-attach.db", "qh-evil-vacuum.db"} {
		if _, err := os.Stat(f); err == nil {
			os.Remove(f)
			t.Errorf("%s was created", f)
		}
	}
}

func TestAttachRejectedOnReadWrite(t *testing.T) {
	path := testutil.SQLiteFixture(t)
	c, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "rw", DSN: path, Mode: engine.ModeRW, PoolSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := filepath.Join(t.TempDir(), "attached.db")
	_, err = c.Query(context.Background(), "ATTACH DATABASE '"+target+"' AS x", engine.QueryOpts{MaxRows: 10})
	if err == nil || !strings.Contains(err.Error(), "error[invalid]") {
		t.Fatalf("attach on rw: %v", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("attach created a file")
	}
}

func TestOpenMissingFileDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: path, Mode: engine.ModeRW, PoolSize: 1})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("Open created the file")
	}
}

// A missing file is named in the error, unless its path has an @ or =, which
// marks a connection string for another engine: it may hold a password, and
// the error reaches the agent.
func TestOpenMissingFileDoesNotEchoSecrets(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.db")
	for _, dsn := range []string{
		"host=h user=u password=hunter2pw dbname=d", "u:hunter2pw/x@h/x.db", "//u:hunter2pw@h/x.db",
		"mongodb+srv:/u:hunter2pw@h/db", "/tmp/hunter2pw=x.db",
	} {
		for _, root := range []string{"", "/"} {
			_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, FileRoot: root})
			if e := qerr.From(err); err == nil || e.Category == "" || strings.Contains(err.Error(), "hunter2pw") {
				t.Errorf("Open(%q, root %q) = %v", dsn, root, err)
			}
		}
	}
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: missing, Mode: engine.ModeRO, PoolSize: 1})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("a plain missing path should be named: %v", err)
	}
}

func TestPath(t *testing.T) {
	// abs is the absolute form of a rooted path: itself on Unix, on the
	// current drive on Windows.
	abs := func(p string) string {
		a, err := filepath.Abs(filepath.FromSlash(p))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	cases := map[string]string{
		"/tmp/a b.db":        abs("/tmp/a b.db"),
		"sqlite:///tmp/a.db": abs("/tmp/a.db"),
		"file:/tmp/a.db":     abs("/tmp/a.db"),
		"file:///tmp/a.db":   abs("/tmp/a.db"),
	}
	for in, want := range cases {
		if got, err := Path(in); err != nil || got != want {
			t.Errorf("Path(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "/tmp/a.db?_pragma=x", "/tmp/a.db#frag"} {
		if _, err := Path(bad); err == nil {
			t.Errorf("Path(%q) accepted", bad)
		}
	}
	// A string for another database is refused with a message that does not
	// repeat it, so a password in it never reaches an agent.
	for _, other := range []string{
		"u:hunter2pw@tcp(h)/x.db", "u:hunter2pw@unix(/run/m.sock)/x.db", "postgres://u:hunter2pw@h/x.db",
		"mysql://u:hunter2pw@h/x", "MONGODB://u:hunter2pw@h/x", "https://h/x.db",
		// Behind a sqlite: or file: prefix it is still another engine's string.
		"sqlite://u:hunter2pw@tcp(h)/x.db", "file:u:hunter2pw@tcp(h)/x.db", "SQLITE:u:hunter2pw@unix(/s)/x.db",
		"sqlite:u:hunter2pw@h/x.db",
	} {
		_, err := Path(other)
		if err == nil || strings.Contains(err.Error(), "hunter2pw") || qerr.From(err).Category != qerr.Invalid {
			t.Errorf("Path(%q) = %v", other, err)
		}
	}
	// Paths that merely contain an @ stay valid.
	for in, want := range map[string]string{"/tmp/me@x/a.db": abs("/tmp/me@x/a.db"), "/tmp/a@2x.db": abs("/tmp/a@2x.db"), "SQLITE:///tmp/a.db": abs("/tmp/a.db"), "FILE:/tmp/a.db": abs("/tmp/a.db")} {
		if got, err := Path(in); err != nil || got != want {
			t.Errorf("Path(%q) = %q, %v", in, got, err)
		}
	}
	info, err := Engine{}.Inspect("sqlite:///tmp/a.db")
	if err != nil || info.Database != abs("/tmp/a.db") || info.Redacted != abs("/tmp/a.db") || info.Password != "" {
		t.Fatalf("Inspect = %+v, %v", info, err)
	}
}
