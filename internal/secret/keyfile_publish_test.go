//go:build linux || darwin || windows

package secret

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func forceKeyFileFallback(temp, path string) error {
	return publishKeyFile(temp, path, func(string, string) error { return unsupportedKeyFileLinkError() }, renameKeyFileNoReplace)
}

// Removing the unsupported-link fallback must prevent creation of the key.
func TestKeyFileUnsupportedLinkFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	key, err := loadOrCreateKeyFile(path, forceKeyFileFallback)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ParseKey(string(data))
	if err != nil || !bytes.Equal(key, loaded) {
		t.Fatal("published key does not match returned key")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("key mode = %v", info.Mode().Perm())
		}
	}
	assertKeyFileDirectory(t, dir, "master.key")
}

// Replacing an existing key, or ignoring a malformed winner, loses persisted data.
func TestKeyFilePublicationWinner(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("fallback=%v/malformed=%v", fallback, malformed), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "master.key")
				winner := []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n")
				if malformed {
					winner = []byte("broken")
				}
				key, err := loadOrCreateKeyFile(path, func(temp, path string) error {
					if err := os.WriteFile(path, winner, 0600); err != nil {
						return err
					}
					if fallback {
						return forceKeyFileFallback(temp, path)
					}
					return publishKeyFile(temp, path, os.Link, renameKeyFileNoReplace)
				})
				if malformed {
					if err == nil || !strings.Contains(err.Error(), "master key file "+path) {
						t.Fatalf("malformed winner was not parsed: %v", err)
					}
				} else if err != nil || !bytes.Equal(key, make([]byte, 32)) {
					t.Fatalf("did not load winner: %v", err)
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(data, winner) {
					t.Fatal("winner was replaced")
				}
				assertKeyFileDirectory(t, dir, "master.key")
			})
		}
	}
}

// Swallowing either publish error must not report success or leave a generated key.
func TestKeyFilePublicationFailureCleanup(t *testing.T) {
	denied := errors.New("unrelated link failure")
	unavailable := errors.New("exclusive rename unavailable")
	for _, tc := range []struct {
		name                     string
		linkErr, renameErr, want error
	}{
		{"link", denied, nil, denied},
		{"fallback", unsupportedKeyFileLinkError(), unavailable, unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := loadOrCreateKeyFile(filepath.Join(dir, "master.key"), func(temp, path string) error {
				return publishKeyFile(temp, path, func(string, string) error { return tc.linkErr }, func(string, string) error { return tc.renameErr })
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			assertKeyFileDirectory(t, dir)
		})
	}
}

// Publishing early exposes a partial key or creates a non-private file.
func TestKeyFileCompletedBeforePublish(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	_, err := loadOrCreateKeyFile(path, func(temp, path string) error {
		if filepath.Dir(temp) != dir {
			t.Fatal("temporary key is on another filesystem")
		}
		data, err := os.ReadFile(temp)
		if err != nil {
			return err
		}
		if len(data) != 45 || data[44] != '\n' {
			t.Fatal("temporary key is incomplete")
		}
		if _, err := ParseKey(string(data)); err != nil {
			return err
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("key exposed before publication")
		}
		return forceKeyFileFallback(temp, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKeyFileDirectory(t, dir, "master.key")
}

// A racing creator must return the winner; readers must never see a partial key.
func TestKeyFileFallbackConcurrentReadersAndCreators(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	const writers = 24
	keys := make(chan []byte, writers)
	errs := make(chan error, writers+1)
	ready := make(chan struct{})
	observed := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var once sync.Once
		defer once.Do(func() { close(observed) })
		for {
			select {
			case <-ready:
				return
			default:
			}
			data, err := readKeyFile(path)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				errs <- err
				return
			}
			if _, err := ParseKey(string(data)); err != nil {
				errs <- err
				return
			}
			once.Do(func() { close(observed) })
		}
	}()
	var creators sync.WaitGroup
	for range writers {
		creators.Add(1)
		go func() {
			defer creators.Done()
			key, err := loadOrCreateKeyFile(path, forceKeyFileFallback)
			if err != nil {
				errs <- err
				return
			}
			keys <- key
		}()
	}
	creators.Wait()
	if _, err := os.Stat(path); err == nil {
		<-observed
	}
	close(ready)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	close(keys)
	var winner []byte
	for key := range keys {
		if winner == nil {
			winner = key
		}
		if !bytes.Equal(winner, key) {
			t.Fatal("creators returned different keys")
		}
	}
	if len(winner) != 32 {
		t.Fatal("no creator returned a key")
	}
	assertKeyFileDirectory(t, dir, "master.key")
}

func assertKeyFileDirectory(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("directory has %d entries, want %d", len(entries), len(want))
	}
	for i, entry := range entries {
		if entry.Name() != want[i] {
			t.Fatalf("unexpected entry %q", entry.Name())
		}
	}
}

// Falling back on a permission, space, I/O, or cross-device error hides the cause.
func testKeyFileLinkErrors(t *testing.T, unsupported, fatal []error) {
	t.Helper()
	for _, wrapped := range []bool{false, true} {
		for _, group := range []struct {
			errors   []error
			fallback bool
		}{{unsupported, true}, {fatal, false}} {
			for _, cause := range group.errors {
				t.Run(fmt.Sprintf("%v/wrapped=%v", cause, wrapped), func(t *testing.T) {
					dir := t.TempDir()
					temp := filepath.Join(dir, "temp")
					path := filepath.Join(dir, "master.key")
					if err := os.WriteFile(temp, []byte("complete"), 0600); err != nil {
						t.Fatal(err)
					}
					linkErr := cause
					if wrapped {
						linkErr = &os.LinkError{Op: "link", Old: temp, New: path, Err: cause}
					}
					err := publishKeyFile(temp, path, func(string, string) error { return linkErr }, renameKeyFileNoReplace)
					if group.fallback {
						if err != nil {
							t.Fatal(err)
						}
						data, err := os.ReadFile(path)
						if err != nil || string(data) != "complete" {
							t.Fatal("fallback did not publish complete file")
						}
					} else {
						if err != linkErr {
							t.Fatalf("error=%v, want original link error", err)
						}
						if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
							t.Fatal("unrelated error published a key")
						}
					}
				})
			}
		}
	}
}

// A successful hard link must remain the first publication method.
func TestKeyFileHardLinkFastPath(t *testing.T) {
	dir := t.TempDir()
	temp, path := filepath.Join(dir, "temp"), filepath.Join(dir, "master.key")
	if err := os.WriteFile(temp, []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	err := publishKeyFile(temp, path, os.Link, func(string, string) error { return errors.New("unexpected fallback") })
	if err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(temp)
	if err != nil {
		t.Fatal(err)
	}
	newInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(oldInfo, newInfo) {
		t.Fatal("fast path did not hard-link completed file")
	}
}

// Cleanup must remove only this attempt's temp, even when publication fails.
func TestKeyFileFailurePreservesUnrelatedTemp(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, ".master.key-other")
	if err := os.WriteFile(sentinel, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("publication failed")
	_, err := loadOrCreateKeyFile(filepath.Join(dir, "master.key"), func(string, string) error { return cause })
	if !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
	assertKeyFileDirectory(t, dir, ".master.key-other")
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unrelated" {
		t.Fatal("cleanup touched unrelated file")
	}
}
