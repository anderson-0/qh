package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/core"
)

// Capture the real client's outgoing request: missing and empty passwords must
// differ, exact bytes survive, and testing remains controlled by the caller.
func TestClientPasswordPatch(t *testing.T) {
	for _, tc := range []struct {
		name, next  string
		wantPresent bool
	}{
		{name: "omitted"},
		{name: "empty clears", wantPresent: true},
		{name: "replace", next: "replacement-password", wantPresent: true},
		{name: "exact whitespace", next: "  replacement password\t ", wantPresent: true},
	} {
		for _, test := range []bool{false, true} {
			name := tc.name + "/skip"
			if test {
				name = tc.name + "/test"
			}
			t.Run(name, func(t *testing.T) {
				type request struct {
					body                                map[string]any
					method, path, query, key, workspace string
				}
				captured := make(chan request, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						w.WriteHeader(400)
						return
					}
					captured <- request{body, r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("X-QH-Workspace")}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"name":"pg","engine":"postgres","mode":"ro","dsn":"postgres://app:***@127.0.0.1/app","version":2}`))
				}))
				defer srv.Close()
				c, err := New(srv.URL, "qh_fixture", "team")
				if err != nil {
					t.Fatal("cannot create client")
				}
				p := core.ConnPatch{}
				if tc.wantPresent {
					p.Password = &tc.next
				}
				info, tr, err := c.UpdateConnection(context.Background(), "pg", p, test)
				if err != nil || info == nil || info.Version != 2 || tr != nil {
					t.Fatal("update response contract changed")
				}
				got := <-captured
				body, wantPresent, next := got.body, tc.wantPresent, tc.next
				value, present := body["password"]
				if present != wantPresent {
					t.Fatal("password presence changed in transport")
				}
				if present && value != next {
					t.Fatal("password bytes changed in transport")
				}
				wantQuery := "skip_test=true"
				if test {
					wantQuery = ""
				}
				if got.method != "PATCH" || got.path != "/api/v1/connections/pg" || got.query != wantQuery || got.key != "Bearer qh_fixture" || got.workspace != "team" {
					t.Fatal("password patch changed routing, testing or authorization headers")
				}
			})
		}
	}
	t.Run("real server", func(t *testing.T) {
		for _, k := range []string{"PGPASSWORD", "PGSERVICE", "PGHOST", "PGHOSTADDR", "PGPORT", "PGDATABASE", "PGUSER", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY"} {
			t.Setenv(k, "")
		}
		t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "missing-passfile"))
		t.Setenv("PGSERVICEFILE", filepath.Join(t.TempDir(), "missing-servicefile"))
		url, admin := server(t)
		c, err := New(url, admin, "default")
		if err != nil {
			t.Fatal("cannot create client")
		}
		ctx := context.Background()
		info, _, err := c.CreateConnection(ctx, core.ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://app:initial-password@127.0.0.1:1/app?sslmode=disable"}, false)
		if err != nil || info == nil {
			t.Fatal("cannot create fixture connection")
		}
		next := "replacement-password"
		info, tr, err := c.UpdateConnection(ctx, "pg", core.ConnPatch{Password: &next}, false)
		if err != nil || info == nil || info.Version != 2 || tr != nil {
			t.Fatal("password patch failed against real API")
		}
		for _, secret := range []string{"initial-password", next} {
			if strings.Contains(info.DSN, secret) {
				t.Fatal("client received an unredacted password")
			}
		}
	})
	t.Run("older strict server rejects password", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var oldPatch struct {
				Description *string `json:"description"`
			}
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&oldPatch); err != nil {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"invalid","title":"invalid JSON body: unknown field","status":400}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"pg","version":2}`))
		}))
		defer srv.Close()
		c, err := New(srv.URL, "qh_fixture", "default")
		if err != nil {
			t.Fatal("cannot create client")
		}
		next := "replacement-password"
		info, tr, err := c.UpdateConnection(context.Background(), "pg", core.ConnPatch{Password: &next}, false)
		if info != nil || tr != nil || err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: invalid JSON body") {
			t.Fatal("older server reported unsupported password edit as success")
		}
		if strings.Contains(err.Error(), next) {
			t.Fatal("older server error exposed a credential")
		}
	})
}
