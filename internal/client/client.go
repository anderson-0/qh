// Package client calls a qh server's REST API. The CLI uses it in remote mode.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anderson-0/qh/internal/auth"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// Client is a REST client for one server, key and (optionally) workspace.
type Client struct {
	base      string
	key       string
	workspace string // sent as X-QH-Workspace when not ""
	http      *http.Client
}

// New returns a client for the server at base. It refuses plain http to
// hosts other than this machine, so the key never crosses a network in clear.
func New(base, key, workspace string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" {
		return nil, qerr.New(qerr.Invalid, "--server must be an http(s) URL such as https://qh.example.com")
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, qerr.New(qerr.Invalid, "refusing to send an API key over plain http to "+u.Host).
			WithHint("Use https, or a server on this machine")
	}
	if key == "" {
		return nil, qerr.New(qerr.Invalid, "remote mode needs an API key").WithHint("Pass --key or set QH_KEY")
	}
	return &Client{base: u.String(), key: key, workspace: workspace, http: &http.Client{
		Timeout: 6 * time.Minute,
		// Never follow redirects: Go would resend the Authorization header, even from https to http.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// send makes one request and returns the response body. Error responses
// become *qerr.Error values with the server's category, message and hint.
func (c *Client) send(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.workspace != "" {
		req.Header.Set("X-QH-Workspace", c.workspace)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, qerr.New(qerr.ConnectionFailed, "cannot reach qh server at "+c.base) // err may carry the request; drop it
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, qerr.New(qerr.ConnectionFailed, "lost the connection to qh server at "+c.base)
	}
	if res.StatusCode >= 300 {
		var p struct{ Type, Title, Detail string }
		if strings.HasPrefix(res.Header.Get("Content-Type"), "application/problem+json") && json.Unmarshal(b, &p) == nil && p.Type != "" {
			return nil, &qerr.Error{Category: qerr.Category(p.Type), Message: p.Title, Hint: p.Detail}
		}
		return nil, qerr.New(qerr.ConnectionFailed, fmt.Sprintf("qh server at %s answered HTTP %d", c.base, res.StatusCode)).
			WithHint("Use the server's final URL; qh does not follow redirects")
	}
	return b, nil
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	b, err := c.send(ctx, method, path, body, "application/json")
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return qerr.New(qerr.ConnectionFailed, "unexpected response from qh server at "+c.base)
	}
	return nil
}

func (c *Client) text(ctx context.Context, method, path string, body any) (string, error) {
	b, err := c.send(ctx, method, path, body, "text/plain")
	return string(b), err
}

func conn(name string) string { return "/api/v1/connections/" + url.PathEscape(name) }

type connectionJSON struct {
	Name              string          `json:"name"`
	Engine            string          `json:"engine"`
	Description       string          `json:"description"`
	Mode              string          `json:"mode"`
	Host              string          `json:"host"`
	Port              int             `json:"port"`
	Database          string          `json:"database"`
	Username          string          `json:"username"`
	DSN               string          `json:"dsn"`
	TLS               *engine.TLSInfo `json:"tls"`
	SSH               *engine.SSHInfo `json:"ssh"`
	DSNRef            string          `json:"dsn_ref"`
	PasswordRef       string          `json:"password_ref"`
	MaxRows           int             `json:"max_rows"`
	TimeoutMS         int             `json:"timeout_ms"`
	PoolSize          int             `json:"pool_size"`
	AllowedNamespaces []string        `json:"allowed_namespaces"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ServerVersion     string          `json:"server_version"`
	ConnectMS         *int64          `json:"connect_ms"`
}

func (j connectionJSON) info() (*core.ConnInfo, *core.TestResult) {
	ci := &core.ConnInfo{Name: j.Name, Engine: j.Engine, Description: j.Description, Mode: engine.Mode(j.Mode),
		Host: j.Host, Port: j.Port, Database: j.Database, User: j.Username, DSN: j.DSN, TLS: j.TLS,
		SSH: j.SSH, DSNRef: j.DSNRef, PasswordRef: j.PasswordRef,
		MaxRows: j.MaxRows, TimeoutMS: j.TimeoutMS, PoolSize: j.PoolSize, AllowedNamespaces: j.AllowedNamespaces,
		Version: j.Version, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt}
	if j.ConnectMS == nil {
		return ci, nil
	}
	return ci, &core.TestResult{ServerVersion: j.ServerVersion, Connect: time.Duration(*j.ConnectMS) * time.Millisecond}
}

type testJSON struct {
	ServerVersion string `json:"server_version"`
	ConnectMS     int64  `json:"connect_ms"`
}

func (c *Client) ListConnections(ctx context.Context) ([]*core.ConnInfo, error) {
	var list []connectionJSON
	if err := c.call(ctx, "GET", "/api/v1/connections", nil, &list); err != nil {
		return nil, err
	}
	out := make([]*core.ConnInfo, len(list))
	for i, j := range list {
		out[i], _ = j.info()
	}
	return out, nil
}

func (c *Client) GetConnection(ctx context.Context, name string) (*core.ConnInfo, error) {
	var j connectionJSON
	if err := c.call(ctx, "GET", conn(name), nil, &j); err != nil {
		return nil, err
	}
	ci, _ := j.info()
	return ci, nil
}

func skip(test bool) string {
	if test {
		return ""
	}
	return "?skip_test=true"
}

func (c *Client) CreateConnection(ctx context.Context, in core.ConnInput, test bool) (*core.ConnInfo, *core.TestResult, error) {
	body := map[string]any{"name": in.Name, "engine": in.Engine}
	set := func(k string, v any, ok bool) {
		if ok {
			body[k] = v
		}
	}
	set("dsn", in.DSN, in.DSN != "")
	set("host", in.Host, in.Host != "")
	set("port", in.Port, in.Port != 0)
	set("database", in.Database, in.Database != "")
	set("username", in.Username, in.Username != "")
	set("password", in.Password, in.Password != "")
	set("params", in.Params, len(in.Params) > 0)
	set("tls", in.TLS, in.TLS != nil)
	set("dsn_ref", in.DSNRef, in.DSNRef != "")
	set("password_ref", in.PasswordRef, in.PasswordRef != "")
	set("ssh", in.SSH, in.SSH != nil)
	set("description", in.Description, in.Description != "")
	set("mode", string(in.Mode), in.Mode != "")
	set("max_rows", in.MaxRows, in.MaxRows != 0)
	set("timeout_ms", in.TimeoutMS, in.TimeoutMS != 0)
	set("pool_size", in.PoolSize, in.PoolSize != 0)
	set("allowed_namespaces", in.AllowedNamespaces, len(in.AllowedNamespaces) > 0)
	var j connectionJSON
	if err := c.call(ctx, "POST", "/api/v1/connections"+skip(test), body, &j); err != nil {
		return nil, nil, err
	}
	ci, tr := j.info()
	return ci, tr, nil
}

func (c *Client) UpdateConnection(ctx context.Context, name string, p core.ConnPatch, test bool) (*core.ConnInfo, *core.TestResult, error) {
	body := map[string]any{}
	if p.Password != nil {
		body["password"] = *p.Password
	}
	if p.DSN != nil {
		body["dsn"] = *p.DSN
	}
	if p.Description != nil {
		body["description"] = *p.Description
	}
	if p.Mode != nil {
		body["mode"] = string(*p.Mode)
	}
	if p.MaxRows != nil {
		body["max_rows"] = *p.MaxRows
	}
	if p.TimeoutMS != nil {
		body["timeout_ms"] = *p.TimeoutMS
	}
	if p.PoolSize != nil {
		body["pool_size"] = *p.PoolSize
	}
	if p.AllowedNamespaces != nil {
		body["allowed_namespaces"] = *p.AllowedNamespaces
	}
	switch {
	case p.ClearTLS && p.TLS != nil:
		return nil, nil, qerr.New(qerr.Invalid, "give tls or clear it, not both")
	case p.ClearTLS:
		body["tls"] = nil
	case p.TLS != nil:
		body["tls"] = p.TLS
	}
	if p.DSNRef != nil {
		body["dsn_ref"] = *p.DSNRef
	}
	if p.PasswordRef != nil {
		body["password_ref"] = *p.PasswordRef
	}
	switch {
	case p.ClearSSH && p.SSH != nil:
		return nil, nil, qerr.New(qerr.Invalid, "give ssh or clear it, not both")
	case p.ClearSSH:
		body["ssh"] = nil
	case p.SSH != nil:
		body["ssh"] = p.SSH
	}
	var j connectionJSON
	if err := c.call(ctx, "PATCH", conn(name)+skip(test), body, &j); err != nil {
		return nil, nil, err
	}
	ci, tr := j.info()
	return ci, tr, nil
}

func (c *Client) DeleteConnection(ctx context.Context, name string) error {
	return c.call(ctx, "DELETE", conn(name), nil, nil)
}

func (c *Client) TestConnection(ctx context.Context, name string) (*core.TestResult, error) {
	var j testJSON
	if err := c.call(ctx, "POST", conn(name)+"/test", nil, &j); err != nil {
		return nil, err
	}
	return &core.TestResult{ServerVersion: j.ServerVersion, Connect: time.Duration(j.ConnectMS) * time.Millisecond}, nil
}

func (c *Client) Query(ctx context.Context, in core.QueryInput) (string, error) {
	body := map[string]any{"query": in.Query}
	if in.MaxRows != 0 {
		body["max_rows"] = in.MaxRows
	}
	if in.Explain {
		body["explain"] = true
	}
	return c.text(ctx, "POST", conn(in.Source)+"/query", body)
}

func (c *Client) Schema(ctx context.Context, in core.SchemaInput) (string, error) {
	q := url.Values{}
	if in.Pattern != "" {
		q.Set("pattern", in.Pattern)
	}
	if in.Object != "" {
		q.Set("object", in.Object)
	}
	path := conn(in.Source) + "/schema"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.text(ctx, "GET", path, nil)
}

func (c *Client) QueryLog(ctx context.Context, f core.QueryLogFilter) ([]*core.QueryLogEntry, error) {
	q := url.Values{}
	if f.Connection != "" {
		q.Set("connection", f.Connection)
	}
	if !f.Since.IsZero() {
		q.Set("since", f.Since.UTC().Format(time.RFC3339))
	}
	if f.Limit != 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	path := "/api/v1/query-log"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var list []struct {
		ID            int64     `json:"id"`
		Connection    string    `json:"connection"`
		KeyID         string    `json:"key_id"`
		Tool          string    `json:"tool"`
		Query         string    `json:"query"`
		Rows          int64     `json:"rows"`
		DurationMS    int64     `json:"duration_ms"`
		ErrorCategory string    `json:"error_category"`
		At            time.Time `json:"at"`
	}
	if err := c.call(ctx, "GET", path, nil, &list); err != nil {
		return nil, err
	}
	out := make([]*core.QueryLogEntry, len(list))
	for i, e := range list {
		out[i] = &core.QueryLogEntry{ID: e.ID, Connection: e.Connection, KeyID: e.KeyID, Tool: e.Tool, Query: e.Query,
			Rows: e.Rows, Duration: time.Duration(e.DurationMS) * time.Millisecond, ErrorCategory: e.ErrorCategory, At: e.At}
	}
	return out, nil
}

type workspaceJSON struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (c *Client) CreateWorkspace(ctx context.Context, name string) (*core.WorkspaceInfo, error) {
	var j workspaceJSON
	if err := c.call(ctx, "POST", "/api/v1/workspaces", map[string]string{"name": name}, &j); err != nil {
		return nil, err
	}
	return &core.WorkspaceInfo{Name: j.Name, CreatedAt: j.CreatedAt}, nil
}

func (c *Client) ListWorkspaces(ctx context.Context) ([]*core.WorkspaceInfo, error) {
	var list []workspaceJSON
	if err := c.call(ctx, "GET", "/api/v1/workspaces", nil, &list); err != nil {
		return nil, err
	}
	out := make([]*core.WorkspaceInfo, len(list))
	for i, j := range list {
		out[i] = &core.WorkspaceInfo{Name: j.Name, CreatedAt: j.CreatedAt}
	}
	return out, nil
}

func (c *Client) DeleteWorkspace(ctx context.Context, name string) error {
	return c.call(ctx, "DELETE", "/api/v1/workspaces/"+url.PathEscape(name), nil, nil)
}

type keyJSON struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Name       string     `json:"name"`
	Workspace  string     `json:"workspace"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedBy  string     `json:"created_by"`
	Key        string     `json:"key"`
}

func (j keyJSON) info() *auth.KeyInfo {
	return &auth.KeyInfo{ID: j.ID, Prefix: j.Prefix, Name: j.Name, Workspace: j.Workspace, Scopes: j.Scopes,
		CreatedAt: j.CreatedAt, ExpiresAt: j.ExpiresAt, LastUsedAt: j.LastUsedAt, CreatedBy: j.CreatedBy}
}

func (c *Client) CreateKey(ctx context.Context, in auth.KeyInput) (string, *auth.KeyInfo, error) {
	body := map[string]any{"scopes": in.Scopes}
	if c.workspace != "" && in.Workspace != "" { // the CLI fills in "default" when none was named; send only an explicit one
		body["workspace"] = in.Workspace
	}
	if in.Name != "" {
		body["name"] = in.Name
	}
	if in.ExpiresIn > 0 {
		body["expires_in"] = in.ExpiresIn.String()
	}
	var j keyJSON
	if err := c.call(ctx, "POST", "/api/v1/keys", body, &j); err != nil {
		return "", nil, err
	}
	return j.Key, j.info(), nil
}

func (c *Client) ListKeys(ctx context.Context) ([]*auth.KeyInfo, error) {
	var list []keyJSON
	if err := c.call(ctx, "GET", "/api/v1/keys", nil, &list); err != nil {
		return nil, err
	}
	out := make([]*auth.KeyInfo, len(list))
	for i, j := range list {
		out[i] = j.info()
	}
	return out, nil
}

func (c *Client) DeleteKey(ctx context.Context, id string) error {
	return c.call(ctx, "DELETE", "/api/v1/keys/"+url.PathEscape(id), nil, nil)
}
