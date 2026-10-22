package sqliteuri_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anderson-0/qh/internal/testutil"
)

// The engine opens a fixture through a UNC path: the administrative share
// \\localhost\C$ names the same directory as the drive path. The test skips
// when the share is not reachable.
func TestEngineOpensUNCPath(t *testing.T) {
	dir := filepath.Join(longTempDir(t), odd)
	path := testutil.SQLiteFixtureIn(t, dir)
	vol := filepath.VolumeName(path)
	if len(vol) != 2 || vol[1] != ':' {
		t.Skipf("%s is not on a drive", path)
	}
	unc := `\\localhost\` + vol[:1] + `$` + path[len(vol):]
	if _, err := os.Stat(unc); err != nil {
		t.Skipf("administrative share not reachable: %v", err)
	}
	engineRoundTrip(t, unc)
}

// The store opens a path with the \\?\ prefix, which Windows does not
// normalize. The engine refuses paths with ?, so only the store can get one.
func TestStoreOpensVerbatimPath(t *testing.T) {
	path := filepath.Join(longTempDir(t), odd+" #1", "qh.db")
	storeRoundTrip(t, `\\?\`+path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file is not at %s: %v", path, err)
	}
}

// longTempDir returns t.TempDir() with long names: a runner's temporary
// directory can contain 8.3 short names such as RUNNER~1.
func longTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	long, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return long
}
