package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestFileRoot(t *testing.T) {
	inside := testutil.SQLiteFixture(t)
	root := filepath.Dir(inside)
	outside := testutil.SQLiteFixture(t)
	link := filepath.Join(root, "escape.db")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	open := func(dsn string) error {
		c, err := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, FileRoot: root})
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := open(inside); err != nil {
		t.Fatalf("inside root: %v", err)
	}
	// t.TempDir directories are siblings, so this walks from inside the root to the other fixture.
	traversal := filepath.Join(root, "..", "..", filepath.Base(filepath.Dir(filepath.Dir(outside))), "with space", "shop.db")
	for _, dsn := range []string{outside, link, traversal, "/etc/hosts"} {
		err := open(dsn)
		if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: sqlite path is outside the allowed directory") {
			t.Errorf("%s: %v", dsn, err)
		}
	}
}
