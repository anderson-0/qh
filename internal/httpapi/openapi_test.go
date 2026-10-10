package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
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
