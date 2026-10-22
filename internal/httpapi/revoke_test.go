package httpapi

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/store"
)

// createKeyAs creates a key through the API with token. Failure messages
// never include the response body of a created key, which holds its token.
func createKeyAs(t *testing.T, e *env, token string, body map[string]any) keyJSON {
	t.Helper()
	var k keyJSON
	r := e.do(t, "POST", "/api/v1/keys", token, body)
	if r.status != 201 {
		t.Fatalf("create key %v: %d %s", body["name"], r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), &k); err != nil {
		t.Fatalf("create key %v: decode: %v", body["name"], err)
	}
	return k
}

// authenticates reports whether a workspace key is accepted.
func authenticates(t *testing.T, e *env, token string) bool {
	t.Helper()
	switch r := e.do(t, "GET", "/api/v1/connections", token, nil); r.status {
	case 200:
		return true
	case 401:
		return false
	default:
		t.Fatalf("GET connections: %d %s", r.status, r.body)
		return false
	}
}

// expectRevoked checks a successful revocation: 204, empty body, one
// QH-Warning header with the fixed text.
func expectRevoked(t *testing.T, r reply) {
	t.Helper()
	if got := r.header.Values("QH-Warning"); r.status != 204 || r.body != "" || len(got) != 1 || got[0] != auth.RevocationWarning {
		t.Fatalf("revoke: %d %q QH-Warning=%q", r.status, r.body, got)
	}
}

func expectNoWarning(t *testing.T, r reply) {
	t.Helper()
	if got := r.header.Values("QH-Warning"); len(got) != 0 {
		t.Fatalf("%d response carries QH-Warning %q", r.status, got)
	}
}

func sameExpiry(a, b *time.Time) bool {
	return a == nil && b == nil ||
		a != nil && b != nil && a.Equal(*b)
}

func headerNames(r reply) string {
	var names []string
	for k := range r.header {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// Revoking a key deletes that key only. Keys it created, and keys they
// created, keep authenticating with their provenance and expiry unchanged,
// and a manage key may still revoke a key it did not create.
func TestRevokeSelectedKeyOnly(t *testing.T) {
	e := newEnv(t, true, nil)
	if r := e.do(t, "POST", "/api/v1/workspaces", e.admin, map[string]any{"name": "team"}); r.status != 201 {
		t.Fatalf("create workspace: %d %s", r.status, r.body)
	}
	both := []string{auth.ScopeQuery, auth.ScopeManage}
	parent := createKeyAs(t, e, e.admin, map[string]any{"workspace": "team", "scopes": both, "expires_in": "7d", "name": "parent"})
	child := createKeyAs(t, e, parent.Key, map[string]any{"scopes": both, "name": "child"})
	grand := createKeyAs(t, e, child.Key, map[string]any{"scopes": []string{auth.ScopeQuery}, "name": "grandchild"})
	root := createKeyAs(t, e, e.admin, map[string]any{"workspace": "team", "scopes": both, "name": "root"})
	if child.CreatedBy != parent.ID || grand.CreatedBy != child.ID {
		t.Fatalf("created_by chain: child %q, grandchild %q", child.CreatedBy, grand.CreatedBy)
	}

	list := func() map[string]keyJSON {
		t.Helper()
		var keys []keyJSON
		r := e.do(t, "GET", "/api/v1/keys?workspace=team", e.admin, nil)
		if r.status != 200 {
			t.Fatalf("list keys: %d %s", r.status, r.body)
		}
		r.json(t, &keys)
		out := map[string]keyJSON{}
		for _, k := range keys {
			out[k.ID] = k
		}
		return out
	}
	before := list()
	for _, k := range []keyJSON{parent, child, grand, root} {
		if !authenticates(t, e, k.Key) {
			t.Fatalf("%s refused before any revocation", k.Name)
		}
	}

	// 1. The admin key revokes the parent; its descendants keep working.
	expectRevoked(t, e.do(t, "DELETE", "/api/v1/keys/"+parent.ID, e.admin, nil))
	if authenticates(t, e, parent.Key) {
		t.Fatal("revoked parent still authenticates")
	}
	for _, k := range []keyJSON{child, grand, root} {
		if !authenticates(t, e, k.Key) {
			t.Fatalf("%s refused after its ancestor was revoked", k.Name)
		}
	}
	after := list()
	if _, ok := after[parent.ID]; ok || len(after) != len(before)-1 {
		t.Fatalf("after revoking parent: %d keys (was %d), parent listed %v", len(after), len(before), ok)
	}
	for id, k := range after {
		was, ok := before[id]
		if !ok || k.CreatedBy != was.CreatedBy || !sameExpiry(k.ExpiresAt, was.ExpiresAt) {
			t.Fatalf("%s changed: created_by %q -> %q, expires %v -> %v", k.Name, was.CreatedBy, k.CreatedBy, was.ExpiresAt, k.ExpiresAt)
		}
	}
	if after[child.ID].CreatedBy != parent.ID {
		t.Fatalf("child created_by %q, want the revoked parent's id", after[child.ID].CreatedBy)
	}

	// 2. A manage key that created none of them revokes the middle key.
	expectRevoked(t, e.do(t, "DELETE", "/api/v1/keys/"+child.ID, root.Key, nil))
	if authenticates(t, e, child.Key) {
		t.Fatal("revoked child still authenticates")
	}
	for _, k := range []keyJSON{grand, root} {
		if !authenticates(t, e, k.Key) {
			t.Fatalf("%s refused after the child was revoked", k.Name)
		}
	}

	// 3. Deleting a workspace removes its keys without the notice.
	r := e.do(t, "DELETE", "/api/v1/workspaces/team", e.admin, nil)
	if r.status != 204 {
		t.Fatalf("delete workspace: %d %s", r.status, r.body)
	}
	expectNoWarning(t, r)
}

// Local mode needs no token and sends the same notice.
func TestRevokeLocalMode(t *testing.T) {
	e := newEnv(t, false, nil)
	_, k, err := auth.CreateKey(context.Background(), e.st, auth.KeyInput{Workspace: store.DefaultWorkspace, Scopes: []string{auth.ScopeQuery}})
	if err != nil {
		t.Fatal(err)
	}
	expectRevoked(t, e.do(t, "DELETE", "/api/v1/keys/"+k.ID, "", nil))
}

// No failed revocation carries the notice, and another workspace's key looks
// exactly like a missing one whether or not it created other keys.
func TestRevokeFailuresCarryNoWarning(t *testing.T) {
	e := newEnv(t, true, nil)
	for _, name := range []string{"team", "other"} {
		if r := e.do(t, "POST", "/api/v1/workspaces", e.admin, map[string]any{"name": name}); r.status != 201 {
			t.Fatalf("create workspace %s: %d %s", name, r.status, r.body)
		}
	}
	both := []string{auth.ScopeQuery, auth.ScopeManage}
	team := createKeyAs(t, e, e.admin, map[string]any{"workspace": "team", "scopes": both, "name": "team"})
	reader := createKeyAs(t, e, team.Key, map[string]any{"scopes": []string{auth.ScopeQuery}, "name": "reader"})
	lone := createKeyAs(t, e, e.admin, map[string]any{"workspace": "other", "scopes": []string{auth.ScopeQuery}, "name": "lone"})
	oparent := createKeyAs(t, e, e.admin, map[string]any{"workspace": "other", "scopes": both, "name": "oparent"})
	ochild := createKeyAs(t, e, oparent.Key, map[string]any{"scopes": []string{auth.ScopeQuery}, "name": "ochild"})

	notFound := func(r reply) {
		t.Helper()
		expectProblem(t, r, 404, "unknown_source", "no API key with id")
		expectNoWarning(t, r)
	}

	// 1. A missing id is the reference.
	missing := store.NewID()
	ref := e.do(t, "DELETE", "/api/v1/keys/"+missing, team.Key, nil)
	notFound(ref)

	// 2. Foreign keys, with and without descendants, look missing.
	for _, k := range []keyJSON{lone, oparent, ochild} {
		r := e.do(t, "DELETE", "/api/v1/keys/"+k.ID, team.Key, nil)
		notFound(r)
		if got, want := strings.ReplaceAll(r.body, k.ID, "ID"), strings.ReplaceAll(ref.body, missing, "ID"); got != want {
			t.Fatalf("foreign %s: body %q, missing id gives %q", k.Name, got, want)
		}
		if got, want := headerNames(r), headerNames(ref); got != want {
			t.Fatalf("foreign %s: headers %s, missing id gives %s", k.Name, got, want)
		}
	}
	for _, k := range []keyJSON{lone, oparent, ochild} {
		if !authenticates(t, e, k.Key) {
			t.Fatalf("foreign %s refused after a failed revocation", k.Name)
		}
	}

	// 3. Missing scope, missing token, and an admin key on a missing id.
	r := e.do(t, "DELETE", "/api/v1/keys/"+team.ID, reader.Key, nil)
	expectProblem(t, r, 403, "forbidden", "this API key lacks the manage scope")
	expectNoWarning(t, r)
	r = e.do(t, "DELETE", "/api/v1/keys/"+team.ID, "", nil)
	expectProblem(t, r, 401, "auth", "")
	expectNoWarning(t, r)
	notFound(e.do(t, "DELETE", "/api/v1/keys/"+missing, e.admin, nil))

	// 4. Revoking the same key twice: the second attempt is not found.
	expectRevoked(t, e.do(t, "DELETE", "/api/v1/keys/"+reader.ID, team.Key, nil))
	notFound(e.do(t, "DELETE", "/api/v1/keys/"+reader.ID, team.Key, nil))
}
