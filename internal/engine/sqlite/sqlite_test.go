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

func TestPath(t *testing.T) {
	cases := map[string]string{
		"/tmp/a b.db":        "/tmp/a b.db",
		"sqlite:///tmp/a.db": "/tmp/a.db",
		"file:/tmp/a.db":     "/tmp/a.db",
		"file:///tmp/a.db":   "/tmp/a.db",
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
	info, err := Engine{}.Inspect("sqlite:///tmp/a.db")
	if err != nil || info.Database != "/tmp/a.db" || info.Redacted != "/tmp/a.db" || info.Password != "" {
		t.Fatalf("Inspect = %+v, %v", info, err)
	}
}
