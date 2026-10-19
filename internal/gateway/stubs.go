package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// handleManagedSettings answers Claude Code's policy fetch. Proem serves no
// policy; 404 tells the client there is none.
func (g *Gateway) handleManagedSettings(w http.ResponseWriter, r *http.Request) {
	writeAnthropicError(w, http.StatusNotFound, "not_found_error", "no managed settings")
}

// handleTelemetry accepts OTLP exports from a signed-in client and discards
// them, so a client with telemetry enabled does not log errors.
func (g *Gateway) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if _, err := g.sessions.Verify(token); err != nil {
		g.fail(r, "invalid_session")
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid session: run /login")
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxTelemetryBytes))
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		return
	}
	// An empty body is a valid, empty protobuf Export*ServiceResponse.
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func bearer(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errType, "message": message},
	})
}
