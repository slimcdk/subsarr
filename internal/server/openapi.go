package server

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openAPISpec []byte

// handleOpenAPI serves the specification the service was built from. It lives
// next to the handlers and is embedded in the binary so that the documentation an
// operator reads is the documentation of the build they are running.
func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}
