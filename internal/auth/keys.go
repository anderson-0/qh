// Package auth implements qh's API keys, scopes and request authentication.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"math/big"
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

const (
	ScopeQuery  = "query"
	ScopeManage = "manage"
	ScopeAdmin  = "admin"
)

var validScopes = map[string]bool{ScopeQuery: true, ScopeManage: true, ScopeAdmin: true}

// Principal is an authenticated caller.
type Principal struct {
	KeyID     string
	Prefix    string
	Workspace string // empty for admin keys
	Scopes    map[string]bool
}

// Has reports whether p may use scope. Admin implies every scope.
func (p *Principal) Has(scope string) bool { return p.Scopes[ScopeAdmin] || p.Scopes[scope] }

// IsAdmin reports whether p holds the admin scope.
func (p *Principal) IsAdmin() bool { return p.Scopes[ScopeAdmin] }

// Local is the principal every request acts as in local mode.
func Local() *Principal {
	return &Principal{KeyID: "local", Workspace: store.DefaultWorkspace, Scopes: map[string]bool{ScopeAdmin: true}}
}

// ResolveWorkspace returns the workspace a request acts on. Workspace keys
// always use their own workspace; admin keys name one with the
// X-QH-Workspace header, falling back to their own (local mode).
func ResolveWorkspace(p *Principal, header string) (string, error) {
	header = strings.TrimSpace(header)
	if !p.IsAdmin() {
		return p.Workspace, nil
	}
	if header != "" {
		return header, nil
	}
	if p.Workspace != "" {
		return p.Workspace, nil
	}
	return "", qerr.New(qerr.Invalid, "admin keys must name a workspace").WithHint("Send the X-QH-Workspace header")
}

const prefixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewToken returns a new key qh_<prefix>_<secret>, its prefix, and the
// SHA-256 of its secret (the only part that is stored).
func NewToken() (token, prefix string, hash []byte, err error) {
	raw := make([]byte, 8+32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", nil, err
	}
	var p strings.Builder
	for _, b := range raw[:8] {
		p.WriteByte(prefixAlphabet[int(b)%len(prefixAlphabet)])
	}
	secret := new(big.Int).SetBytes(raw[8:]).Text(62)
	return "qh_" + p.String() + "_" + secret, p.String(), HashSecret(secret), nil
}

// ParseToken splits a key into its prefix and secret.
func ParseToken(token string) (prefix, secret string, ok bool) {
	rest, ok := strings.CutPrefix(token, "qh_")
	if !ok {
		return "", "", false
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || len(prefix) != 8 || secret == "" {
		return "", "", false
	}
	for _, c := range prefix {
		if !strings.ContainsRune(prefixAlphabet, c) {
			return "", "", false
		}
	}
	for _, c := range secret {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "", "", false
		}
	}
	return prefix, secret, true
}

// HashSecret is the stored form of a key's secret.
func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}
