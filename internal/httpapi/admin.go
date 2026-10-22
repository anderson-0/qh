package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/qerr"
)

type workspaceJSON struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *api) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := a.cfg.Service.ListWorkspaces(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]workspaceJSON, 0, len(list))
	for _, ws := range list {
		out = append(out, workspaceJSON{Name: ws.Name, CreatedAt: ws.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	ws, err := a.cfg.Service.CreateWorkspace(r.Context(), in.Name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, workspaceJSON{Name: ws.Name, CreatedAt: ws.CreatedAt})
}

func (a *api) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if err := a.cfg.Service.DeleteWorkspace(r.Context(), r.PathValue("name")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type keyJSON struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Name       string     `json:"name,omitempty"`
	Workspace  string     `json:"workspace,omitempty"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedBy  string     `json:"created_by,omitempty"` // id of the key that created this one
	Key        string     `json:"key,omitempty"`        // only in the response that creates the key
}

func toKey(k *auth.KeyInfo) keyJSON {
	return keyJSON{ID: k.ID, Prefix: k.Prefix, Name: k.Name, Workspace: k.Workspace, Scopes: k.Scopes,
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, CreatedBy: k.CreatedBy}
}

// listKeys: admin keys see every key (or one workspace with ?workspace=);
// manage keys see their own workspace's keys.
func (a *api) listKeys(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var list []*auth.KeyInfo
	var err error
	if p.IsAdmin() {
		filter := r.URL.Query()
		list, err = auth.ListKeys(r.Context(), a.cfg.Store, filter.Get("workspace"), !filter.Has("workspace"))
	} else {
		list, err = auth.ListKeys(r.Context(), a.cfg.Store, p.Workspace, false)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]keyJSON, 0, len(list))
	for _, k := range list {
		out = append(out, toKey(k))
	}
	writeJSON(w, http.StatusOK, out)
}

// createKey: admin keys create any key; manage keys create query/manage keys
// for their own workspace only, and those keys expire no later than the
// manage key itself (a longer or missing expiry is capped to it). In server
// mode the new key records the id of the key that created it.
func (a *api) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Workspace string   `json:"workspace,omitempty"`
		Scopes    []string `json:"scopes"`
		Name      string   `json:"name,omitempty"`
		ExpiresIn string   `json:"expires_in,omitempty"`
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	exp, err := auth.ParseExpiry(in.ExpiresIn)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ki := auth.KeyInput{Scopes: in.Scopes, Name: in.Name, ExpiresIn: exp}
	p := principal(r)
	if a.cfg.Auth != nil {
		ki.CreatedBy = p.KeyID
	}
	if !p.IsAdmin() {
		if in.Workspace != "" && in.Workspace != p.Workspace {
			a.fail(w, r, qerr.New(qerr.Forbidden, "manage keys can only create keys for their own workspace"))
			return
		}
		for _, s := range in.Scopes {
			if s == auth.ScopeAdmin {
				a.fail(w, r, qerr.New(qerr.Forbidden, "only admin keys can create admin keys"))
				return
			}
		}
		in.Workspace = p.Workspace
		ki.NotAfter = p.ExpiresAt
	}
	ki.Workspace = in.Workspace
	tok, info, err := auth.CreateKey(r.Context(), a.cfg.Store, ki)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := toKey(info)
	out.Key = tok
	writeJSON(w, http.StatusCreated, out)
}

// warningHeader carries a fixed notice on a successful response.
const warningHeader = "QH-Warning"

// deleteKey revokes the selected key only; keys it created stay valid. Manage
// keys may revoke only their own workspace's keys, and other workspaces' keys
// look like they do not exist. Success is 204 with auth.RevocationWarning in
// the QH-Warning header.
func (a *api) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if p := principal(r); !p.IsAdmin() {
		list, err := auth.ListKeys(r.Context(), a.cfg.Store, p.Workspace, false)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		found := false
		for _, k := range list {
			found = found || k.ID == id
		}
		if !found {
			a.fail(w, r, qerr.New(qerr.UnknownSource, fmt.Sprintf("no API key with id %q", id)))
			return
		}
	}
	if err := auth.DeleteKey(r.Context(), a.cfg.Store, id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set(warningHeader, auth.RevocationWarning)
	w.WriteHeader(http.StatusNoContent)
}
