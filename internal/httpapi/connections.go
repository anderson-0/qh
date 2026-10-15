package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/qerr"
)

type connectionJSON struct {
	Name              string          `json:"name"`
	Engine            string          `json:"engine"`
	Description       string          `json:"description,omitempty"`
	Mode              string          `json:"mode"`
	Host              string          `json:"host,omitempty"`
	Port              int             `json:"port,omitempty"`
	Database          string          `json:"database,omitempty"`
	Username          string          `json:"username,omitempty"`
	DSN               string          `json:"dsn"`
	TLS               *engine.TLSInfo `json:"tls,omitempty"`
	SSH               *engine.SSHInfo `json:"ssh,omitempty"`
	DSNRef            string          `json:"dsn_ref,omitempty"`
	PasswordRef       string          `json:"password_ref,omitempty"`
	MaxRows           int             `json:"max_rows"`
	TimeoutMS         int             `json:"timeout_ms"`
	PoolSize          int             `json:"pool_size"`
	AllowedNamespaces []string        `json:"allowed_namespaces,omitempty"`
	Version           int64           `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ServerVersion     string          `json:"server_version,omitempty"`
	ConnectMS         *int64          `json:"connect_ms,omitempty"`
}

func toConnection(c *core.ConnInfo, tr *core.TestResult) connectionJSON {
	out := connectionJSON{Name: c.Name, Engine: c.Engine, Description: c.Description, Mode: string(c.Mode),
		Host: c.Host, Port: c.Port, Database: c.Database, Username: c.User, DSN: c.DSN, TLS: c.TLS,
		SSH: c.SSH, DSNRef: c.DSNRef, PasswordRef: c.PasswordRef,
		MaxRows: c.MaxRows, TimeoutMS: c.TimeoutMS, PoolSize: c.PoolSize, AllowedNamespaces: c.AllowedNamespaces,
		Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if tr != nil {
		ms := tr.Connect.Milliseconds()
		out.ServerVersion, out.ConnectMS = tr.ServerVersion, &ms
	}
	return out
}

type connectionInput struct {
	Name              string            `json:"name"`
	Engine            string            `json:"engine"`
	DSN               string            `json:"dsn,omitempty"`
	Host              string            `json:"host,omitempty"`
	Port              int               `json:"port,omitempty"`
	Database          string            `json:"database,omitempty"`
	Username          string            `json:"username,omitempty"`
	Password          string            `json:"password,omitempty"`
	Params            map[string]string `json:"params,omitempty"`
	TLS               *engine.TLS       `json:"tls,omitempty"`
	DSNRef            string            `json:"dsn_ref,omitempty"`
	PasswordRef       string            `json:"password_ref,omitempty"`
	SSH               *engine.SSH       `json:"ssh,omitempty"`
	Description       string            `json:"description,omitempty"`
	Mode              string            `json:"mode,omitempty"`
	MaxRows           int               `json:"max_rows,omitempty"`
	TimeoutMS         int               `json:"timeout_ms,omitempty"`
	PoolSize          int               `json:"pool_size,omitempty"`
	AllowedNamespaces []string          `json:"allowed_namespaces,omitempty"`
}

func (in connectionInput) core() core.ConnInput {
	return core.ConnInput{Name: in.Name, Engine: in.Engine, DSN: in.DSN, Host: in.Host, Port: in.Port, Database: in.Database,
		Username: in.Username, Password: in.Password, Params: in.Params, TLS: in.TLS, Description: in.Description,
		Mode: engine.Mode(in.Mode), MaxRows: in.MaxRows, TimeoutMS: in.TimeoutMS, PoolSize: in.PoolSize,
		AllowedNamespaces: in.AllowedNamespaces, DSNRef: in.DSNRef, PasswordRef: in.PasswordRef, SSH: in.SSH}
}

type connectionPatch struct {
	Password          *string         `json:"password"`
	DSN               *string         `json:"dsn"`
	Description       *string         `json:"description"`
	Mode              *string         `json:"mode"`
	MaxRows           *int            `json:"max_rows"`
	TimeoutMS         *int            `json:"timeout_ms"`
	PoolSize          *int            `json:"pool_size"`
	AllowedNamespaces *[]string       `json:"allowed_namespaces"`
	TLS               json.RawMessage `json:"tls"` // absent: unchanged; null: removed; object: replaced
	DSNRef            *string         `json:"dsn_ref"`
	PasswordRef       *string         `json:"password_ref"`
	SSH               json.RawMessage `json:"ssh"` // absent: unchanged; null: removed; object: replaced
}

func (p connectionPatch) core() (core.ConnPatch, error) {
	out := core.ConnPatch{DSN: p.DSN, Password: p.Password, Description: p.Description, MaxRows: p.MaxRows, TimeoutMS: p.TimeoutMS,
		PoolSize: p.PoolSize, AllowedNamespaces: p.AllowedNamespaces, DSNRef: p.DSNRef, PasswordRef: p.PasswordRef}
	if p.Mode != nil {
		m := engine.Mode(*p.Mode)
		out.Mode = &m
	}
	switch raw := bytes.TrimSpace(p.TLS); {
	case len(raw) == 0:
	case string(raw) == "null":
		out.ClearTLS = true
	default:
		var t engine.TLS
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			return out, qerr.New(qerr.Invalid, "invalid JSON body: tls: "+err.Error())
		}
		out.TLS = &t
	}
	switch raw := bytes.TrimSpace(p.SSH); {
	case len(raw) == 0:
	case string(raw) == "null":
		out.ClearSSH = true
	default:
		var s engine.SSH
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return out, qerr.New(qerr.Invalid, "invalid JSON body: ssh: "+err.Error())
		}
		out.SSH = &s
	}
	return out, nil
}

type testJSON struct {
	ServerVersion string `json:"server_version"`
	ConnectMS     int64  `json:"connect_ms"`
}

func skipTest(r *http.Request) bool { return r.URL.Query().Get("skip_test") == "true" }

func (a *api) listConnections(w http.ResponseWriter, r *http.Request) {
	list, err := a.cfg.Service.ListConnections(r.Context(), workspace(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]connectionJSON, 0, len(list))
	for _, c := range list {
		out = append(out, toConnection(c, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) getConnection(w http.ResponseWriter, r *http.Request) {
	c, err := a.cfg.Service.GetConnection(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toConnection(c, nil))
}

func (a *api) createConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	c, tr, err := a.cfg.Service.CreateConnection(r.Context(), workspace(r), in.core(), !skipTest(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toConnection(c, tr))
}

func (a *api) updateConnection(w http.ResponseWriter, r *http.Request) {
	var p connectionPatch
	if err := decode(r, &p); err != nil {
		a.fail(w, r, err)
		return
	}
	cp, err := p.core()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c, tr, err := a.cfg.Service.UpdateConnection(r.Context(), workspace(r), r.PathValue("name"), cp, !skipTest(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toConnection(c, tr))
}

func (a *api) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if err := a.cfg.Service.DeleteConnection(r.Context(), workspace(r), r.PathValue("name")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) testInput(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	tr, err := a.cfg.Service.TestInput(r.Context(), workspace(r), in.core())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, testJSON{ServerVersion: tr.ServerVersion, ConnectMS: tr.Connect.Milliseconds()})
}

func (a *api) testConnection(w http.ResponseWriter, r *http.Request) {
	tr, err := a.cfg.Service.TestConnection(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, testJSON{ServerVersion: tr.ServerVersion, ConnectMS: tr.Connect.Milliseconds()})
}

func (a *api) refreshSchema(w http.ResponseWriter, r *http.Request) {
	n, d, err := a.cfg.Service.RefreshSchema(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"objects": int64(n), "duration_ms": d.Milliseconds()})
}

type fieldJSON struct {
	Name     string   `json:"name"`
	Type     string   `json:"type,omitempty"`
	Nullable bool     `json:"nullable"`
	Default  string   `json:"default,omitempty"`
	Comment  string   `json:"comment,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

type foreignKeyJSON struct {
	Namespace    string   `json:"namespace"`
	Object       string   `json:"object"`
	Columns      []string `json:"columns"`
	RefNamespace string   `json:"ref_namespace"`
	RefObject    string   `json:"ref_object"`
	RefColumns   []string `json:"ref_columns"`
}

type indexJSON struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

type objectJSON struct {
	Namespace    string           `json:"namespace"`
	Name         string           `json:"name"`
	Kind         string           `json:"kind"`
	Comment      string           `json:"comment,omitempty"`
	RowEstimate  int64            `json:"row_estimate"`
	Fields       []fieldJSON      `json:"fields"`
	PrimaryKey   []string         `json:"primary_key,omitempty"`
	ForeignKeys  []foreignKeyJSON `json:"foreign_keys,omitempty"`
	ReferencedBy []foreignKeyJSON `json:"referenced_by,omitempty"`
	Indexes      []indexJSON      `json:"indexes,omitempty"`
}

func toForeignKeys(fks []engine.ForeignKey) []foreignKeyJSON {
	var out []foreignKeyJSON
	for _, fk := range fks {
		out = append(out, foreignKeyJSON{Namespace: fk.Namespace, Object: fk.Object, Columns: fk.Columns,
			RefNamespace: fk.RefNamespace, RefObject: fk.RefObject, RefColumns: fk.RefColumns})
	}
	return out
}

func toObject(o *engine.Object) objectJSON {
	out := objectJSON{Namespace: o.Namespace, Name: o.Name, Kind: string(o.Kind), Comment: o.Comment, RowEstimate: o.RowEstimate,
		Fields: make([]fieldJSON, 0, len(o.Fields)), PrimaryKey: o.PrimaryKey,
		ForeignKeys: toForeignKeys(o.ForeignKeys), ReferencedBy: toForeignKeys(o.ReferencedBy)}
	for _, f := range o.Fields {
		out.Fields = append(out.Fields, fieldJSON{Name: f.Name, Type: f.Type, Nullable: f.Nullable, Default: f.Default, Comment: f.Comment, Enum: f.Enum})
	}
	for _, ix := range o.Indexes {
		out.Indexes = append(out.Indexes, indexJSON{Name: ix.Name, Columns: ix.Columns, Unique: ix.Unique})
	}
	return out
}

func (a *api) getSchema(w http.ResponseWriter, r *http.Request) {
	pattern, object := r.URL.Query().Get("pattern"), r.URL.Query().Get("object")
	if !wantsJSON(r) {
		out, err := a.cfg.Service.Schema(r.Context(), workspace(r), core.SchemaInput{Source: r.PathValue("name"), Pattern: pattern, Object: object})
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeText(w, out)
		return
	}
	cat, err := a.cfg.Service.Catalog(r.Context(), workspace(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	objs := format.Matching(cat, pattern, object)
	out := make([]objectJSON, 0, len(objs))
	for _, o := range objs {
		out = append(out, toObject(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"objects": out})
}

type queryRequest struct {
	Query   string `json:"query"`
	MaxRows int    `json:"max_rows,omitempty"`
	Explain bool   `json:"explain,omitempty"`
}

type columnJSON struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type queryJSON struct {
	Columns      []columnJSON `json:"columns"`
	Rows         [][]any      `json:"rows"`
	Truncated    bool         `json:"truncated"`
	RowsAffected *int64       `json:"rows_affected,omitempty"`
	DurationMS   int64        `json:"duration_ms"`
}

func toQuery(r *engine.Result) queryJSON {
	q := queryJSON{Columns: make([]columnJSON, 0, len(r.Columns)), Rows: make([][]any, 0, len(r.Rows)),
		Truncated: r.Truncated, DurationMS: r.Duration.Milliseconds()}
	for _, c := range r.Columns {
		q.Columns = append(q.Columns, columnJSON{Name: c.Name, Type: c.Type})
	}
	for _, row := range r.Rows {
		out := make([]any, len(row))
		for i, v := range row {
			switch {
			case v.Null:
				out[i] = nil
			case v.Binary:
				out[i] = map[string]int{"bytes": v.Size}
			default:
				out[i] = v.Text
			}
		}
		q.Rows = append(q.Rows, out)
	}
	if r.RowsAffected >= 0 {
		n := r.RowsAffected
		q.RowsAffected = &n
	}
	return q
}

func (a *api) query(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	in := core.QueryInput{Source: r.PathValue("name"), Query: req.Query, MaxRows: req.MaxRows, Explain: req.Explain}
	if !wantsJSON(r) {
		out, err := a.cfg.Service.Query(r.Context(), workspace(r), in)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeText(w, out)
		return
	}
	res, err := a.cfg.Service.QueryResult(r.Context(), workspace(r), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toQuery(res))
}
