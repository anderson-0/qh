package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiDoc []byte

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openapiDoc)
}
