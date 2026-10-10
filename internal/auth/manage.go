package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

// KeyInput describes a key to create.
type KeyInput struct {
	Workspace string // empty for an admin key
	Scopes    []string
	Name      string
	ExpiresIn time.Duration // 0 for no expiry
}

// KeyInfo is what may be shown about a key. It never includes the secret.
type KeyInfo struct {
	ID         string
	Prefix     string
	Name       string
	Workspace  string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

func toInfo(k *store.APIKey) *KeyInfo {
	return &KeyInfo{ID: k.ID, Prefix: k.Prefix, Name: k.Name, Workspace: k.WorkspaceID, Scopes: k.Scopes,
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt}
}

func validate(in KeyInput) error {
	if len(in.Scopes) == 0 {
		return qerr.New(qerr.Invalid, "at least one scope is required").WithHint("Use query, manage or admin")
	}
	admin := false
	for _, s := range in.Scopes {
		if !validScopes[s] {
			return qerr.New(qerr.Invalid, fmt.Sprintf("unknown scope %q", s)).WithHint("Use query, manage or admin")
		}
		admin = admin || s == ScopeAdmin
	}
	if admin && in.Workspace != "" {
		return qerr.New(qerr.Invalid, "admin keys cannot belong to a workspace")
	}
	if !admin && in.Workspace == "" {
		return qerr.New(qerr.Invalid, "workspace keys need a workspace")
	}
	if in.ExpiresIn < 0 {
		return qerr.New(qerr.Invalid, "expiry must be positive")
	}
	return nil
}

// CreateKey creates a key and returns it in full. The full key is never
// stored or shown again.
func CreateKey(ctx context.Context, st store.Store, in KeyInput) (string, *KeyInfo, error) {
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if err := validate(in); err != nil {
		return "", nil, err
	}
	for {
		tok, prefix, hash, err := NewToken()
		if err != nil {
			return "", nil, err
		}
		k := &store.APIKey{ID: store.NewID(), WorkspaceID: in.Workspace, Prefix: prefix, Hash: hash, Scopes: in.Scopes, Name: in.Name}
		if in.ExpiresIn > 0 {
			exp := time.Now().UTC().Add(in.ExpiresIn)
			k.ExpiresAt = &exp
		}
		err = st.CreateKey(ctx, k)
		switch {
		case errors.Is(err, store.ErrExists):
			continue // prefix collision; draw again
		case errors.Is(err, store.ErrNotFound):
			return "", nil, qerr.New(qerr.UnknownSource, fmt.Sprintf("no workspace named %q", in.Workspace))
		case err != nil:
			return "", nil, err
		}
		return tok, toInfo(k), nil
	}
}

// ListKeys lists the keys of one workspace ("" lists admin keys), or every
// key when all is true.
func ListKeys(ctx context.Context, st store.Store, workspace string, all bool) ([]*KeyInfo, error) {
	var keys []*store.APIKey
	var err error
	if all {
		keys, err = st.ListAllKeys(ctx)
	} else {
		keys, err = st.ListKeys(ctx, workspace)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*KeyInfo, len(keys))
	for i, k := range keys {
		out[i] = toInfo(k)
	}
	return out, nil
}

// DeleteKey revokes a key.
func DeleteKey(ctx context.Context, st store.Store, id string) error {
	err := st.DeleteKey(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return qerr.New(qerr.UnknownSource, fmt.Sprintf("no API key with id %q", id))
	}
	return err
}

// Bootstrap creates the first admin key when the store has none. When fixed
// is set (QH_BOOTSTRAP_ADMIN_KEY) that key is stored and token is empty;
// otherwise a new key is generated and returned so it can be shown once.
func Bootstrap(ctx context.Context, st store.Store, fixed string) (token string, created bool, err error) {
	n, err := st.CountKeys(ctx)
	if err != nil || n > 0 {
		return "", false, err
	}
	k := &store.APIKey{ID: store.NewID(), Scopes: []string{ScopeAdmin}, Name: "bootstrap"}
	if fixed != "" {
		prefix, secret, ok := ParseToken(strings.TrimSpace(fixed))
		if !ok {
			return "", false, qerr.New(qerr.Invalid, "QH_BOOTSTRAP_ADMIN_KEY is not a valid qh API key").
				WithHint("Generate one with qh key create, or unset it to have one generated")
		}
		k.Prefix, k.Hash = prefix, HashSecret(secret)
	} else {
		if token, k.Prefix, k.Hash, err = NewToken(); err != nil {
			return "", false, err
		}
	}
	if err := st.CreateKey(ctx, k); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// ParseExpiry reads an expiry such as "90d" or "720h"; "" means none.
func ParseExpiry(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	bad := qerr.New(qerr.Invalid, fmt.Sprintf("invalid expiry %q", s)).WithHint("Use whole days such as 90d, or a duration such as 720h")
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, bad
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, bad
	}
	return d, nil
}
