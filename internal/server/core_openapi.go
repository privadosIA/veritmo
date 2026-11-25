package server

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"gopkg.in/yaml.v3"

	"veritmo/internal/server/api"
)

// openapiYAML is the hand-written OpenAPI description of every route
// registered in routes(). openapi_test.go keeps it in step with them.
//
//go:embed openapi.yaml
var openapiYAML []byte

var (
	openapiOnce sync.Once
	openapiJSON []byte
	openapiErr  error
)

// openapiDocument converts the embedded YAML to JSON once.
func openapiDocument() ([]byte, error) {
	openapiOnce.Do(func() {
		var doc map[string]any
		if err := yaml.Unmarshal(openapiYAML, &doc); err != nil {
			openapiErr = err
			return
		}
		openapiJSON, openapiErr = json.Marshal(doc)
	})
	return openapiJSON, openapiErr
}

// GET /api/openapi.json. It lives under /api/, so the auth middleware
// protects it like every other endpoint (spec: api-documentation).
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	doc, err := openapiDocument()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, api.CodeInternal, "the API description could not be built: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}
