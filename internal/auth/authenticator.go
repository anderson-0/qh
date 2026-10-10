package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// Authenticator checks API keys against the store on every request, so a
// deleted or expired key stops working immediately.
type Authenticator struct {
	st      store.Store
	now     func() time.Time
	mu      sync.Mutex
	touched map[string]time.Time
}

// NewAuthenticator returns an Authenticator backed by st.
func NewAuthenticator(st store.Store) *Authenticator {
	return &Authenticator{st: st, now: time.Now, touched: map[string]time.Time{}}
}

func authErr(msg string) *qerr.Error {
	return qerr.New(qerr.Auth, msg).WithHint("Send Authorization: Bearer qh_…")
}

// Authenticate validates an Authorization header value.
func (a *Authenticator) Authenticate(ctx context.Context, header string) (*Principal, error) {
	tok, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return nil, authErr("missing or malformed Authorization header")
	}
	prefix, secret, ok := ParseToken(strings.TrimSpace(tok))
	if !ok {
		return nil, authErr("invalid API key")
	}
	k, err := a.st.GetKeyByPrefix(ctx, prefix)
	if errors.Is(err, store.ErrNotFound) {
		return nil, authErr("invalid API key")
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(k.Hash, HashSecret(secret)) != 1 {
		return nil, authErr("invalid API key")
	}
	now := a.now()
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return nil, authErr("API key expired")
	}
	a.touch(ctx, k.ID, now)
	p := &Principal{KeyID: k.ID, Prefix: k.Prefix, Workspace: k.WorkspaceID, Scopes: map[string]bool{}, ExpiresAt: k.ExpiresAt}
	for _, s := range k.Scopes {
		p.Scopes[s] = true
	}
	return p, nil
}

// touch records last use at most once a minute per key. A failure to record
// it never fails the request.
func (a *Authenticator) touch(ctx context.Context, id string, now time.Time) {
	a.mu.Lock()
	last, seen := a.touched[id]
	if seen && now.Sub(last) < time.Minute {
		a.mu.Unlock()
		return
	}
	a.touched[id] = now
	a.mu.Unlock()
	_ = a.st.TouchKey(ctx, id, now)
}
