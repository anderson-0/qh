package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/auth"
)

func TestWorkspacesAndKeys(t *testing.T) {
	e := newEnv(t, true, nil)
	if r := e.do(t, "POST", "/api/v1/workspaces", e.admin, map[string]any{"name": "team"}); r.status != 201 || !strings.Contains(r.body, `"name":"team"`) {
		t.Fatalf("create workspace: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "POST", "/api/v1/workspaces", e.admin, map[string]any{"name": "team"}), 400, "invalid", `a workspace named "team" already exists`)
	var ws []struct{ Name string }
	e.do(t, "GET", "/api/v1/workspaces", e.admin, nil).json(t, &ws)
	if len(ws) != 2 || ws[1].Name != "team" {
		t.Fatalf("list workspaces: %+v", ws)
	}

	var created struct {
		ID        string   `json:"id"`
		Key       string   `json:"key"`
		Workspace string   `json:"workspace"`
		Scopes    []string `json:"scopes"`
	}
	r := e.do(t, "POST", "/api/v1/keys", e.admin, map[string]any{"workspace": "team", "scopes": []string{"query", "manage"}, "name": "team-admin", "expires_in": "90d"})
	r.json(t, &created)
	if r.status != 201 || !strings.HasPrefix(created.Key, "qh_") || created.Workspace != "team" {
		t.Fatalf("admin creates team key: %d %s", r.status, r.body)
	}
	team := created.Key

	expectProblem(t, e.do(t, "POST", "/api/v1/keys", team, map[string]any{"scopes": []string{"admin"}}), 403, "forbidden", "only admin keys can create admin keys")
	expectProblem(t, e.do(t, "POST", "/api/v1/keys", team, map[string]any{"workspace": "default", "scopes": []string{"query"}}), 403, "forbidden", "manage keys can only create keys for their own workspace")
	r = e.do(t, "POST", "/api/v1/keys", team, map[string]any{"scopes": []string{"query"}, "name": "agent"})
	r.json(t, &created)
	if r.status != 201 || created.Workspace != "team" {
		t.Fatalf("team creates a query key: %d %s", r.status, r.body)
	}
	agentID := created.ID

	var keys []struct {
		ID        string `json:"id"`
		Workspace string `json:"workspace"`
		Key       string `json:"key"`
	}
	e.do(t, "GET", "/api/v1/keys", team, nil).json(t, &keys)
	if len(keys) != 2 {
		t.Fatalf("team sees %d keys", len(keys))
	}
	for _, k := range keys {
		if k.Workspace != "team" || k.Key != "" {
			t.Fatalf("key listing leaks: %+v", k)
		}
	}
	e.do(t, "GET", "/api/v1/keys", e.admin, nil).json(t, &keys)
	if len(keys) != 3 {
		t.Fatalf("admin sees %d keys", len(keys))
	}
	e.key(t, "default", auth.ScopeQuery)
	e.do(t, "GET", "/api/v1/keys?workspace=default", e.admin, nil).json(t, &keys)
	if len(keys) != 1 || keys[0].Workspace != "default" {
		t.Fatalf("admin filter: %+v", keys)
	}
	defaultID := keys[0].ID
	expectProblem(t, e.do(t, "DELETE", "/api/v1/keys/"+defaultID, team, nil), 404, "unknown_source", "no API key with id")
	if r := e.do(t, "DELETE", "/api/v1/keys/"+agentID, team, nil); r.status != 204 {
		t.Fatalf("delete own key: %d %s", r.status, r.body)
	}

	expectProblem(t, e.do(t, "DELETE", "/api/v1/workspaces/default", e.admin, nil), 400, "invalid", "the default workspace cannot be deleted")
	if r := e.do(t, "DELETE", "/api/v1/workspaces/team", e.admin, nil); r.status != 204 {
		t.Fatalf("delete workspace: %d %s", r.status, r.body)
	}
	expectProblem(t, e.do(t, "GET", "/api/v1/connections", team, nil), 401, "auth", "invalid API key")
}

// A key created by a workspace's manage key never outlives that key, and
// records which key created it.
func TestKeysCannotOutliveTheirCreator(t *testing.T) {
	e := newEnv(t, true, nil)
	if r := e.do(t, "POST", "/api/v1/workspaces", e.admin, map[string]any{"name": "team"}); r.status != 201 {
		t.Fatalf("create workspace: %d %s", r.status, r.body)
	}
	type keyOut struct {
		ID        string     `json:"id"`
		Key       string     `json:"key"`
		ExpiresAt *time.Time `json:"expires_at"`
		CreatedBy string     `json:"created_by"`
	}
	create := func(token string, body map[string]any) keyOut {
		t.Helper()
		var k keyOut
		r := e.do(t, "POST", "/api/v1/keys", token, body)
		r.json(t, &k)
		if r.status != 201 {
			t.Fatalf("create key: %d %s", r.status, r.body)
		}
		return k
	}
	parent := create(e.admin, map[string]any{"workspace": "team", "scopes": []string{"query", "manage"}, "expires_in": "7d"})
	if parent.ExpiresAt == nil {
		t.Fatal("parent has no expiry")
	}
	for _, expires := range []string{"", "30d"} {
		child := create(parent.Key, map[string]any{"scopes": []string{"query", "manage"}, "expires_in": expires})
		if child.ExpiresAt == nil || !child.ExpiresAt.Equal(*parent.ExpiresAt) {
			t.Errorf("expires_in %q: child expires %v, parent %v", expires, child.ExpiresAt, parent.ExpiresAt)
		}
		if child.CreatedBy != parent.ID {
			t.Errorf("expires_in %q: created_by %q, want %q", expires, child.CreatedBy, parent.ID)
		}
	}
	short := create(parent.Key, map[string]any{"scopes": []string{"query"}, "expires_in": "1d"})
	if short.ExpiresAt == nil || !short.ExpiresAt.Before(*parent.ExpiresAt) {
		t.Errorf("a shorter expiry was changed: %v", short.ExpiresAt)
	}

	var list []keyOut
	e.do(t, "GET", "/api/v1/keys", e.admin, nil).json(t, &list)
	by := map[string]string{}
	for _, k := range list {
		by[k.ID] = k.CreatedBy
	}
	if by[short.ID] != parent.ID {
		t.Errorf("listing: created_by %q, want %q", by[short.ID], parent.ID)
	}
	var adminID string
	for id, creator := range by {
		if id != parent.ID && creator == "" {
			adminID = id // the bootstrap key has no creator
		}
	}
	if adminID == "" || by[parent.ID] != adminID {
		t.Errorf("parent created_by %q, bootstrap key %q", by[parent.ID], adminID)
	}
}
