// Package secret seals connection strings with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// KeySize is the master key length in bytes.
const KeySize = 32

var (
	ErrUnknownKey = errors.New("secret: sealed with an unknown master key")
	ErrCorrupt    = errors.New("secret: cannot decrypt (wrong key, wrong record, or corrupted data)")
)

// KeyID identifies a master key: the first 4 bytes of its SHA-256.
func KeyID(key []byte) uint32 {
	h := sha256.Sum256(key)
	return binary.BigEndian.Uint32(h[:4])
}

// Keyring seals with its primary key and opens with any of its keys.
type Keyring struct {
	primary uint32
	keys    map[uint32]cipher.AEAD
}

// NewKeyring builds a keyring whose primary key is the first argument.
func NewKeyring(primary []byte, old ...[]byte) (*Keyring, error) {
	k := &Keyring{keys: map[uint32]cipher.AEAD{}}
	for i, key := range append([][]byte{primary}, old...) {
		if len(key) != KeySize {
			return nil, fmt.Errorf("secret: master key %d is %d bytes, want %d", i, len(key), KeySize)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		id := KeyID(key)
		k.keys[id] = aead
		if i == 0 {
			k.primary = id
		}
	}
	return k, nil
}

// PrimaryID returns the id of the key used by Seal.
func (k *Keyring) PrimaryID() uint32 { return k.primary }

// Seal encrypts plaintext. aad must be supplied again to Open.
func (k *Keyring) Seal(plaintext, aad []byte) ([]byte, error) {
	aead := k.keys[k.primary]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 4, 4+len(nonce)+len(plaintext)+aead.Overhead())
	binary.BigEndian.PutUint32(out, k.primary)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts a blob produced by Seal.
func (k *Keyring) Open(blob, aad []byte) ([]byte, error) {
	if len(blob) < 4 {
		return nil, ErrCorrupt
	}
	aead, ok := k.keys[binary.BigEndian.Uint32(blob)]
	if !ok {
		return nil, ErrUnknownKey
	}
	ns := aead.NonceSize()
	if len(blob) < 4+ns {
		return nil, ErrCorrupt
	}
	pt, err := aead.Open(nil, blob[4:4+ns], blob[4+ns:], aad)
	if err != nil {
		return nil, ErrCorrupt
	}
	return pt, nil
}

// ParseKey decodes a base64 master key.
func ParseKey(b64 string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("secret: master key is not valid base64: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret: master key is %d bytes, want %d", len(key), KeySize)
	}
	return key, nil
}

// LoadOrCreateKeyFile reads a base64 master key from path, creating it with
// mode 0600 when it does not exist.
func LoadOrCreateKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return ParseKey(string(b))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return LoadOrCreateKeyFile(path) // another process created it first
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}
