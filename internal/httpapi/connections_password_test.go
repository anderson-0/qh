package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/auth"
)

// These cases catch losing password presence or trimming bytes between strict
// request decoding and the core patch. Core tests own sealed persistence.
func TestConnectionPasswordPatchREST(t *testing.T) {
	for _, k := range []string{"PGPASSWORD", "PGSERVICE", "PGSERVICEFILE", "PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "missing-passfile"))
	t.Setenv("PGSERVICEFILE", filepath.Join(t.TempDir(), "missing-servicefile"))

	const initial = "initial-password"
	const replacement = "replacement-password"
	const dsn = "postgres://app:" + initial + "@127.0.0.1:1/app?sslmode=disable&connect_timeout=1"
	e := newEnv(t, true, nil)
	t.Cleanup(e.svc.Close)
	key := e.key(t, "default", auth.ScopeManage, auth.ScopeQuery)
	private := func(t *testing.T, r reply) {
		t.Helper()
		for _, secret := range []string{initial, replacement, "replacement password", "target-password"} {
			for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
				if strings.Contains(r.body, form) {
					t.Fatal("response exposed a fixture credential")
				}
			}
		}
	}
	public := func(t *testing.T, r reply, status int) map[string]any {
		t.Helper()
		private(t, r)
		if r.status != status {
			t.Fatalf("response status %d, want %d", r.status, status)
		}
		var body map[string]any
		if json.Unmarshal([]byte(r.body), &body) != nil {
			t.Fatal("response is not a JSON object")
		}
		if _, present := body["password"]; present {
			t.Fatal("public connection contains a password property")
		}
		return body
	}
	create := func(t *testing.T, name string, fields map[string]any) {
		t.Helper()
		fields["name"], fields["engine"] = name, "postgres"
		public(t, e.do(t, "POST", "/api/v1/connections?skip_test=true", key, fields), 201)
	}
	create(t, "pg", map[string]any{"dsn": dsn})
	path := "/api/v1/connections/pg?skip_test=true"
	for _, tc := range []struct {
		name, body, want string
		present          bool
		invalid          bool
	}{
		{name: "omitted", body: `{}`},
		{name: "null", body: `{"password":null}`},
		{name: "empty clears", body: `{"password":""}`, present: true},
		{name: "replace", body: `{"password":"replacement-password"}`, present: true, want: replacement},
		{name: "exact whitespace", body: `{"password":"  replacement password\t "}`, present: true, want: "  replacement password\t "},
		{name: "boolean", body: `{"password":true}`, invalid: true},
		{name: "number", body: `{"password":123}`, invalid: true},
		{name: "array", body: `{"password":[]}`, invalid: true},
		{name: "object", body: `{"password":{}}`, invalid: true},
		{name: "unknown field", body: `{"password":"replacement-password","unexpected":true}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p connectionPatch
			err := decode(httptest.NewRequest("PATCH", path, strings.NewReader(tc.body)), &p)
			if tc.invalid {
				if err == nil {
					t.Error("strict patch decoder accepted invalid input")
				}
			} else if err != nil {
				t.Error("patch decoder rejected a valid password input")
			} else {
				cp, err := p.core()
				if err != nil || (cp.Password != nil) != tc.present {
					t.Error("password pointer presence changed before core")
				} else if tc.present && *cp.Password != tc.want {
					t.Error("password bytes changed before core")
				}
			}
			r := e.do(t, "PATCH", path, key, tc.body)
			private(t, r)
			if tc.invalid {
				expectProblem(t, r, 400, "invalid", "invalid JSON body")
			} else {
				public(t, r, 200)
			}
		})
	}

	t.Run("new DSN is the target", func(t *testing.T) {
		r := e.do(t, "PATCH", path, key, map[string]any{"dsn": "postgres://other:target-password@127.0.0.2:5433/next?sslmode=disable", "password": replacement})
		body := public(t, r, 200)
		if body["host"] != "127.0.0.2" || body["database"] != "next" || body["username"] != "other" {
			t.Fatal("password patch lost the replacement DSN target")
		}
	})
	t.Run("reference rules", func(t *testing.T) {
		create(t, "refs", map[string]any{"dsn_ref": "env:QH_UNUSED_DSN", "password_ref": "env:QH_UNUSED_PW"})
		refPath := "/api/v1/connections/refs?skip_test=true"
		for _, body := range []map[string]any{
			{"password": replacement},
			{"password": replacement, "dsn": dsn},
			{"password": replacement, "dsn_ref": "env:QH_UNUSED_NEXT"},
			{"password": replacement, "password_ref": "env:QH_UNUSED_NEXT"},
		} {
			r := e.do(t, "PATCH", refPath, key, body)
			private(t, r)
			expectProblem(t, r, 400, "invalid", "inline password edits require removing")
		}
		public(t, e.do(t, "PATCH", refPath, key, map[string]any{"dsn": dsn, "password_ref": " \t ", "password": replacement}), 200)
		for _, body := range []map[string]any{
			{"password": replacement, "dsn_ref": "env:QH_UNUSED_DSN"},
			{"password": replacement, "password_ref": "env:QH_UNUSED_PW"},
		} {
			r := e.do(t, "PATCH", refPath, key, body)
			private(t, r)
			expectProblem(t, r, 400, "invalid", "inline password edits require removing")
		}
		r := e.do(t, "PATCH", refPath, key, map[string]any{"dsn": dsn, "dsn_ref": "", "password": replacement})
		private(t, r)
		expectProblem(t, r, 400, "invalid", "give dsn or dsn_ref")
	})
	t.Run("sqlite refuses", func(t *testing.T) {
		public(t, e.do(t, "POST", "/api/v1/connections?skip_test=true", key, map[string]any{"name": "sqlite", "engine": "sqlite", "dsn": filepath.Join(t.TempDir(), "db")}), 201)
		for _, next := range []string{"", replacement} {
			r := e.do(t, "PATCH", "/api/v1/connections/sqlite?skip_test=true", key, map[string]any{"password": next})
			private(t, r)
			expectProblem(t, r, 400, "invalid", "this engine does not support inline password edits")
		}
	})
	t.Run("errors keep credentials private", func(t *testing.T) {
		r := e.do(t, "PATCH", path, key, map[string]any{"dsn": "postgres://app:target-password@127.0.0.1/%zz", "password": replacement})
		private(t, r)
		expectProblem(t, r, 400, "invalid", "invalid connection string for password edit")
		for _, tc := range []struct {
			token       string
			status      int
			typ, prefix string
		}{
			{"", 401, "auth", "missing or malformed Authorization header"},
			{"qh_aaaaaaaa_nope", 401, "auth", "invalid API key"},
			{e.key(t, "default", auth.ScopeQuery), 403, "forbidden", "this API key lacks the manage scope"},
		} {
			r := e.do(t, "PATCH", path, tc.token, map[string]any{"password": replacement})
			private(t, r)
			expectProblem(t, r, tc.status, tc.typ, tc.prefix)
		}
	})
	t.Run("workspace isolation", func(t *testing.T) {
		if _, err := e.svc.CreateWorkspace(context.Background(), "other"); err != nil {
			t.Fatal("cannot create workspace")
		}
		other := e.key(t, "other", auth.ScopeManage)
		for _, headers := range [][]string{nil, {"X-QH-Workspace", "default"}} {
			r := e.do(t, "PATCH", path, other, map[string]any{"password": replacement}, headers...)
			private(t, r)
			expectProblem(t, r, 404, "unknown_source", `no connection named "pg"`)
		}
	})
	t.Run("skip test controls connectivity", func(t *testing.T) {
		create(t, "unreachable", map[string]any{"dsn": dsn})
		body := map[string]any{"password": replacement}
		public(t, e.do(t, "PATCH", "/api/v1/connections/unreachable?skip_test=true", key, body), 200)
		r := e.do(t, "PATCH", "/api/v1/connections/unreachable", key, body)
		private(t, r)
		if r.status == 200 {
			t.Fatal("password-only edit did not test connectivity")
		}
		var problem problemBody
		if json.Unmarshal([]byte(r.body), &problem) != nil || problem.Type != "connection_failed" {
			t.Fatal("connectivity failure lost the existing error response")
		}
	})
	t.Run("body limit", func(t *testing.T) {
		r := e.do(t, "PATCH", path, key, `{"password":"`+strings.Repeat("x", 2<<20)+`"}`)
		private(t, r)
		expectProblem(t, r, 400, "invalid", "request body is larger than 1 MiB")
	})
	t.Run("get and list stay public", func(t *testing.T) {
		public(t, e.do(t, "GET", "/api/v1/connections/pg", key, nil), 200)
		r := e.do(t, "GET", "/api/v1/connections", key, nil)
		private(t, r)
		var list []map[string]any
		if r.status != 200 || json.Unmarshal([]byte(r.body), &list) != nil || len(list) < 1 {
			t.Fatal("list connections failed")
		}
		for _, connection := range list {
			if _, present := connection["password"]; present {
				t.Fatal("list connection contains a password property")
			}
		}
	})
}
