package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/auth"
)

func TestOpenAPIMatchesRoutes(t *testing.T) {
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiDoc, &doc); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi version %q", doc.OpenAPI)
	}
	documented := map[string]bool{}
	for path, ops := range doc.Paths {
		for method := range ops {
			if method != "parameters" {
				documented[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	a := &api{mcp: http.NotFoundHandler()}
	served := map[string]bool{}
	for _, rt := range a.routes() {
		m := rt.method
		if m == "" {
			m = "POST" // /mcp is documented as POST
		}
		served[m+" "+rt.pattern] = true
	}
	var missing, extra []string
	for k := range served {
		if !documented[k] {
			missing = append(missing, k)
		}
	}
	for k := range documented {
		if !served[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("undocumented routes %v; documented but not served %v", missing, extra)
	}
}

func TestOpenAPIIsServedPublicly(t *testing.T) {
	e := newEnv(t, true, nil)
	r := e.do(t, "GET", "/api/v1/openapi.json", "", nil)
	if r.status != 200 || !strings.Contains(r.body, `"openapi"`) || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", r.status, r.header.Get("Content-Type"))
	}
}

// Validate the request/response boundary in the served schema, so generated
// clients can send null or empty strings without exposing response passwords.
func TestOpenAPIPasswordPatch(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				AdditionalProperties *bool                      `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openapiDoc, &doc); err != nil {
		t.Fatal(err)
	}
	patch := doc.Components.Schemas["ConnectionPatch"]
	if patch.AdditionalProperties == nil || *patch.AdditionalProperties {
		t.Fatal("patch schema no longer rejects unknown fields")
	}
	raw, ok := patch.Properties["password"]
	if !ok {
		t.Fatal("ConnectionPatch does not document password")
	}
	var password struct {
		Type []string `json:"type"`
	}
	if json.Unmarshal(raw, &password) != nil || len(password.Type) != 2 {
		t.Fatal("patch password must allow string and null")
	}
	sort.Strings(password.Type)
	if password.Type[0] != "null" || password.Type[1] != "string" {
		t.Fatal("patch password must allow string and null")
	}
	if _, present := doc.Components.Schemas["Connection"].Properties["password"]; present {
		t.Fatal("response schema exposes password")
	}
}

// The revoke response documents QH-Warning with exactly the text the handler
// sends, and no other response of that operation has headers.
func TestOpenAPIRevokeWarning(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiDoc, &doc); err != nil {
		t.Fatal(err)
	}
	var op struct {
		Responses map[string]struct {
			Headers map[string]struct {
				Schema struct {
					Type  string `json:"type"`
					Const string `json:"const"`
				} `json:"schema"`
			} `json:"headers"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(doc.Paths["/api/v1/keys/{id}"]["delete"], &op); err != nil {
		t.Fatal(err)
	}
	h, ok := op.Responses["204"].Headers["QH-Warning"]
	if !ok || h.Schema.Type != "string" || h.Schema.Const != auth.RevocationWarning {
		t.Fatalf("204 QH-Warning: %+v present=%v", h, ok)
	}
	for code, r := range op.Responses {
		if code != "204" && len(r.Headers) > 0 {
			t.Fatalf("response %s documents headers", code)
		}
	}
}
