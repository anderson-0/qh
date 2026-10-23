package auth

import (
	"context"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/store"
)

func newStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var tokenRe = regexp.MustCompile(`^qh_[a-z0-9]{8}_[0-9A-Za-z]{30,}$`)

func TestTokens(t *testing.T) {
	tok, prefix, hash, err := NewToken()
	if err != nil || !tokenRe.MatchString(tok) || len(hash) != 32 {
		t.Fatalf("token %q hash %d %v", tok, len(hash), err)
	}
	p, secret, ok := ParseToken(tok)
	if !ok || p != prefix || string(HashSecret(secret)) != string(hash) {
		t.Fatalf("parse: %q %q %v", p, secret, ok)
	}
	for _, bad := range []string{"", "qh_", "qh_short_x", "xx_abcdefgh_secret", "qh_ABCDEFGH_secret", "qh_abcdefgh_", "qh_abcdefgh_sec ret"} {
		if _, _, ok := ParseToken(bad); ok {
			t.Errorf("ParseToken(%q) accepted", bad)
		}
	}
	t2, _, _, _ := NewToken()
	if t2 == tok {
		t.Fatal("tokens repeat")
	}
}

func TestPrincipal(t *testing.T) {
	q := &Principal{Workspace: "team", Scopes: map[string]bool{ScopeQuery: true}}
	if !q.Has(ScopeQuery) || q.Has(ScopeManage) || q.IsAdmin() {
		t.Fatal("query key scopes")
	}
	admin := &Principal{Scopes: map[string]bool{ScopeAdmin: true}}
	if !admin.Has(ScopeQuery) || !admin.Has(ScopeManage) || !admin.IsAdmin() {
		t.Fatal("admin implies all scopes")
	}
	if ws, err := ResolveWorkspace(q, "other"); err != nil || ws != "team" {
		t.Fatalf("workspace keys ignore the header: %q %v", ws, err)
	}
	if ws, err := ResolveWorkspace(admin, " other "); err != nil || ws != "other" {
		t.Fatalf("admin header: %q %v", ws, err)
	}
	if _, err := ResolveWorkspace(admin, ""); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: admin keys must name a workspace") {
		t.Fatalf("admin without header: %v", err)
	}
	if ws, err := ResolveWorkspace(Local(), ""); err != nil || ws != store.DefaultWorkspace {
		t.Fatalf("local: %q %v", ws, err)
	}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	tok, info, err := CreateKey(ctx, st, KeyInput{Workspace: "default", Scopes: []string{ScopeQuery}, Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	a := NewAuthenticator(st)
	p, err := a.Authenticate(ctx, "Bearer "+tok)
	if err != nil || p.KeyID != info.ID || p.Workspace != "default" || !p.Has(ScopeQuery) || p.Prefix != info.Prefix {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	k, _ := st.GetKeyByPrefix(ctx, info.Prefix)
	if k.LastUsedAt == nil {
		t.Fatal("last_used_at not recorded")
	}
	prefix, _, _ := ParseToken(tok)
	for header, want := range map[string]string{
		"":                                "missing or malformed Authorization header",
		"Basic abc":                       "missing or malformed Authorization header",
		"Bearer nope":                     "invalid API key",
		"Bearer qh_" + prefix + "_wrong0": "invalid API key",
		"Bearer qh_zzzzzzzz_abc":          "invalid API key",
	} {
		_, err := a.Authenticate(ctx, header)
		if e := qerr.From(err); err == nil || e.Category != qerr.Auth || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", header, err)
		}
	}
}

func TestAuthenticateExpiredAndDeleted(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := NewAuthenticator(st)
	now := time.Now()
	a.now = func() time.Time { return now }
	tok, info, err := CreateKey(ctx, st, KeyInput{Workspace: "default", Scopes: []string{ScopeQuery}, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "Bearer "+tok); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := a.Authenticate(ctx, "Bearer "+tok); err == nil || !strings.Contains(err.Error(), "API key expired") {
		t.Fatalf("expired: %v", err)
	}
	tok2, info2, _ := CreateKey(ctx, st, KeyInput{Workspace: "default", Scopes: []string{ScopeQuery}})
	if _, err := a.Authenticate(ctx, "Bearer "+tok2); err != nil {
		t.Fatal(err)
	}
	if err := DeleteKey(ctx, st, info2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, "Bearer "+tok2); err == nil || qerr.From(err).Category != qerr.Auth {
		t.Fatalf("deleted key still works: %v", err)
	}
	_ = info
}

// The approved wording. It also travels in an HTTP header, so it must stay
// one line of printable ASCII.
func TestRevocationWarning(t *testing.T) {
	const want = "Only the selected key was revoked. Descendant keys may remain valid until individually revoked or expired."
	if RevocationWarning != want {
		t.Fatalf("RevocationWarning = %q", RevocationWarning)
	}
	for i := 0; i < len(RevocationWarning); i++ {
		if c := RevocationWarning[i]; c < 0x20 || c > 0x7e {
			t.Fatalf("byte %d is %#x", i, c)
		}
	}
}

// Deleting a key removes its row only. Its child, grandchild and an
// unrelated root keep authenticating with their own expiry and created_by.
func TestDeleteKeyRevokesOnlyTheSelectedKey(t *testing.T) {
	for _, selected := range []string{"parent", "child", "grandchild"} {
		t.Run(selected, func(t *testing.T) {
			ctx := context.Background()
			st := newStore(t)
			a := NewAuthenticator(st)
			toks, infos := map[string]string{}, map[string]*KeyInfo{}
			add := func(name, by string, notAfter *time.Time, expiresIn time.Duration) {
				t.Helper()
				tok, info, err := CreateKey(ctx, st, KeyInput{Workspace: "default", Scopes: []string{ScopeQuery, ScopeManage},
					Name: name, ExpiresIn: expiresIn, NotAfter: notAfter, CreatedBy: by})
				if err != nil {
					t.Fatal(err)
				}
				toks[name], infos[name] = tok, info
			}
			// The chain the HTTP API builds: each child is capped to its creator's expiry.
			add("parent", "", nil, 72*time.Hour)
			add("child", infos["parent"].ID, infos["parent"].ExpiresAt, 0)
			add("grandchild", infos["child"].ID, infos["child"].ExpiresAt, 0)
			add("root", "", nil, 0)
			for name, tok := range toks {
				if _, err := a.Authenticate(ctx, "Bearer "+tok); err != nil {
					t.Fatalf("%s before revocation: %v", name, err)
				}
			}
			before, err := st.ListAllKeys(ctx)
			if err != nil {
				t.Fatal(err)
			}
			id := infos[selected].ID
			if err := DeleteKey(ctx, st, id); err != nil {
				t.Fatal(err)
			}
			after, err := st.ListAllKeys(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var want []*store.APIKey
			for _, k := range before {
				if k.ID != id {
					want = append(want, k)
				}
			}
			if !reflect.DeepEqual(after, want) {
				t.Fatalf("deleting %s changed other rows: %d rows before, %d after", selected, len(before), len(after))
			}
			for name, tok := range toks {
				p, err := a.Authenticate(ctx, "Bearer "+tok)
				if name == selected {
					if err == nil || qerr.From(err).Category != qerr.Auth {
						t.Fatalf("revoked %s still authenticates: %v", name, err)
					}
					continue
				}
				if err != nil || p.KeyID != infos[name].ID || !sameExpiry(p.ExpiresAt, infos[name].ExpiresAt) {
					t.Fatalf("%s after revoking %s: %v", name, selected, err)
				}
			}
			if err := DeleteKey(ctx, st, id); err == nil || qerr.From(err).Category != qerr.UnknownSource {
				t.Fatalf("second delete: %v", err)
			}
		})
	}
}

func sameExpiry(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func TestCreateKeyValidation(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	cases := map[string]KeyInput{
		"error[invalid]: at least one scope is required":          {Workspace: "default"},
		`error[invalid]: unknown scope "root"`:                    {Workspace: "default", Scopes: []string{"root"}},
		"error[invalid]: admin keys cannot belong to a workspace": {Workspace: "default", Scopes: []string{ScopeAdmin}},
		"error[invalid]: workspace keys need a workspace":         {Scopes: []string{ScopeQuery}},
		`error[unknown_source]: no workspace named "nope"`:        {Workspace: "nope", Scopes: []string{ScopeQuery}},
	}
	for want, in := range cases {
		if _, _, err := CreateKey(ctx, st, in); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%+v: %v, want prefix %q", in, err, want)
		}
	}
	if _, info, err := CreateKey(ctx, st, KeyInput{Scopes: []string{ScopeAdmin}, Name: "ops"}); err != nil || info.Workspace != "" {
		t.Fatalf("admin key: %+v %v", info, err)
	}
	if list, _ := ListKeys(ctx, st, "", true); len(list) != 1 {
		t.Fatalf("list all: %v", list)
	}
}

func TestBootstrap(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	tok, created, err := Bootstrap(ctx, st, "")
	if err != nil || !created || !tokenRe.MatchString(tok) {
		t.Fatalf("bootstrap: %q %v %v", tok, created, err)
	}
	p, err := NewAuthenticator(st).Authenticate(ctx, "Bearer "+tok)
	if err != nil || !p.IsAdmin() || p.Workspace != "" {
		t.Fatalf("bootstrap key: %+v %v", p, err)
	}
	if tok2, created, err := Bootstrap(ctx, st, ""); err != nil || created || tok2 != "" {
		t.Fatalf("second bootstrap: %q %v %v", tok2, created, err)
	}

	st2 := newStore(t)
	fixed, _, _, _ := NewToken()
	if tok, created, err := Bootstrap(ctx, st2, fixed); err != nil || !created || tok != "" {
		t.Fatalf("fixed bootstrap: %q %v %v", tok, created, err)
	}
	if _, err := NewAuthenticator(st2).Authenticate(ctx, "Bearer "+fixed); err != nil {
		t.Fatalf("fixed key: %v", err)
	}
	if _, _, err := Bootstrap(ctx, newStore(t), "not-a-key"); err == nil || !strings.Contains(err.Error(), "QH_BOOTSTRAP_ADMIN_KEY") {
		t.Fatalf("bad fixed key: %v", err)
	}
	for _, weak := range []string{"qh_aaaaaaaa_a", "qh_aaaaaaaa_" + strings.Repeat("b", 31)} {
		wst := newStore(t)
		_, created, err := Bootstrap(ctx, wst, weak)
		if err == nil || qerr.From(err).Category != qerr.Invalid || created || !strings.Contains(err.Error(), "QH_BOOTSTRAP_ADMIN_KEY") {
			t.Fatalf("weak fixed key %q: created=%v err=%v", weak, created, err)
		}
		if n, _ := wst.CountKeys(ctx); n != 0 {
			t.Fatalf("weak fixed key %q left %d keys behind", weak, n)
		}
	}
	edge := "qh_aaaaaaaa_" + strings.Repeat("b", 32)
	if _, created, err := Bootstrap(ctx, newStore(t), edge); err != nil || !created {
		t.Fatalf("32-character fixed key: created=%v err=%v", created, err)
	}
}

// staleCount reports no keys even when some exist, as a Bootstrap that read
// the count just before another instance created the first key would see.
type staleCount struct{ store.Store }

func (staleCount) CountKeys(context.Context) (int, error) { return 0, nil }

// TestBootstrapTwiceWithFixedKeyDoesNotFail guards the CountKeys fast path only.
func TestBootstrapTwiceWithFixedKeyDoesNotFail(t *testing.T) {
	st := newStore(t)
	fixed := "qh_" + strings.Repeat("a", 8) + "_" + strings.Repeat("b", 40)
	_, created, err := Bootstrap(context.Background(), st, fixed)
	if err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	_, created, err = Bootstrap(context.Background(), st, fixed)
	if err != nil || created {
		t.Fatalf("second: %v %v", created, err)
	}
}

func TestBootstrapLostRace(t *testing.T) {
	ctx := context.Background()
	fixed := "qh_" + strings.Repeat("a", 8) + "_" + strings.Repeat("b", 40)
	for name, key := range map[string]string{"fixed": fixed, "generated": ""} {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			if _, created, err := Bootstrap(ctx, st, key); err != nil || !created {
				t.Fatalf("first: %v %v", created, err)
			}
			tok, created, err := Bootstrap(ctx, staleCount{st}, key)
			if err != nil || created || tok != "" {
				t.Fatalf("lost race: %q %v %v", tok, created, err)
			}
			if n, _ := st.CountKeys(ctx); n != 1 {
				t.Fatalf("keys = %d", n)
			}
		})
	}
}

func TestParseExpiry(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "90d": 90 * 24 * time.Hour, "720h": 720 * time.Hour, "30m": 30 * time.Minute}
	for in, want := range cases {
		if got, err := ParseExpiry(in); err != nil || got != want {
			t.Errorf("ParseExpiry(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"soon", "-1h", "0d", "1.5d"} {
		if _, err := ParseExpiry(bad); err == nil {
			t.Errorf("ParseExpiry(%q) accepted", bad)
		}
	}
}
