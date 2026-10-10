package secret

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpen(t *testing.T) {
	k, err := NewKeyring(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := k.Seal([]byte("postgres://u:pw@h/db"), []byte("conn-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("pw@h")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := k.Open(blob, []byte("conn-1"))
	if err != nil || string(got) != "postgres://u:pw@h/db" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := k.Open(blob, []byte("conn-2")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong aad err = %v", err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := k.Open(blob, []byte("conn-1")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered err = %v", err)
	}
	if _, err := k.Open([]byte{1, 2}, nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short blob err = %v", err)
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKeyBytes := newKey(t), newKey(t)
	old, _ := NewKeyring(oldKey)
	blob, _ := old.Seal([]byte("secret"), []byte("id"))
	both, err := NewKeyring(newKeyBytes, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := both.Open(blob, []byte("id")); err != nil || string(got) != "secret" {
		t.Fatalf("old blob with rotated keyring = %q, %v", got, err)
	}
	resealed, _ := both.Seal([]byte("secret"), []byte("id"))
	onlyNew, _ := NewKeyring(newKeyBytes)
	if _, err := onlyNew.Open(resealed, []byte("id")); err != nil {
		t.Fatalf("resealed blob: %v", err)
	}
	if _, err := onlyNew.Open(blob, []byte("id")); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("old blob without old key err = %v", err)
	}
	if both.PrimaryID() != KeyID(newKeyBytes) {
		t.Fatal("primary should be the first key")
	}
}

func TestBadKeyLength(t *testing.T) {
	if _, err := NewKeyring(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key accepted")
	}
	if _, err := ParseKey("dG9vLXNob3J0"); err == nil {
		t.Fatal("short base64 key accepted")
	}
}

func TestKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	k1, err := LoadOrCreateKeyFile(path)
	if err != nil || len(k1) != KeySize {
		t.Fatalf("create = %d bytes, %v", len(k1), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
	k2, err := LoadOrCreateKeyFile(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatal("reload returned a different key")
	}
}

func TestEmptyKeyFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateKeyFile(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("empty key file err = %v, want an error naming %s", err, path)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatal("existing key file was replaced")
	}
}

func TestKeyFileNoTempLeftovers(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateKeyFile(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "master.key" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory entries = %v, want only master.key", names)
	}
}
