package cli

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

func TestMasterkeyRotate(t *testing.T) {
	t.Setenv("QH_MASTER_KEY", "")
	home, db := t.TempDir(), testutil.SQLiteFixture(t)
	if _, errOut, code := run(t, home, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db); code != 0 {
		t.Fatalf("add: %s", errOut)
	}
	old := filepath.Join(t.TempDir(), "old.key")
	if err := os.Rename(filepath.Join(home, "master.key"), old); err != nil {
		t.Fatal(err)
	}
	// Without --generate, rotate refuses to invent a master key.
	home3 := t.TempDir()
	run(t, home3, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db)
	os.Remove(filepath.Join(home3, "master.key"))
	if _, errOut, code := run(t, home3, "", "masterkey", "rotate", "--old-key-file", old); code != 1 ||
		!strings.HasPrefix(errOut, "error[invalid]: no current master key") {
		t.Fatalf("no key: %d %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(home3, "master.key")); !os.IsNotExist(err) {
		t.Fatalf("master.key was created: %v", err)
	}
	// The next command generates a new master.key, which cannot open shop yet.
	out, errOut, code := run(t, home, "", "masterkey", "rotate", "--generate", "--old-key-file", old)
	if code != 0 || !strings.HasPrefix(out, "re-sealed 1 of 1 connections with master key ") ||
		!strings.Contains(out, "\ngenerated a new master key in ") {
		t.Fatalf("rotate: %d %q %q", code, out, errOut)
	}
	if out, _, _ := run(t, home, "", "query", "shop", "SELECT count(*) AS n FROM orders"); !strings.HasPrefix(out, "n\n250\n") {
		t.Fatalf("query after rotation: %q", out)
	}
	if out, _, _ := run(t, home, "", "masterkey", "rotate", "--old-key-file", old); !strings.HasPrefix(out, "re-sealed 0 of 1 connections") {
		t.Fatalf("second rotation: %q", out)
	}
	if _, errOut, code := run(t, home, "", "masterkey", "rotate", "--old-key-file", filepath.Join(home, "master.key")); code != 1 ||
		!strings.HasPrefix(errOut, "error[invalid]: the old key is the current master key") {
		t.Fatalf("same key: %d %q", code, errOut)
	}
	if _, errOut, code := run(t, home, "", "masterkey", "rotate"); code != 1 || !strings.HasPrefix(errOut, "error[invalid]: --old-key-file is required") {
		t.Fatalf("missing flag: %d %q", code, errOut)
	}

	// A wrong old key changes nothing.
	home2 := t.TempDir()
	run(t, home2, "", "conn", "add", "shop", "--engine", "sqlite", "--dsn", db)
	os.Remove(filepath.Join(home2, "master.key"))
	wrong := filepath.Join(t.TempDir(), "wrong.key")
	k := make([]byte, 32)
	rand.Read(k)
	os.WriteFile(wrong, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600)
	if _, errOut, code := run(t, home2, "", "masterkey", "rotate", "--generate", "--old-key-file", wrong); code != 1 ||
		!strings.HasPrefix(errOut, `error[invalid]: connection "shop" in workspace "default" cannot be opened`) {
		t.Fatalf("wrong key: %d %q", code, errOut)
	}
}
