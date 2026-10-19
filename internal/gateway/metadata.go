package gateway

import (
	"encoding/json"
	"net/http"
)

// gatewayProtocolVersion matches the built-in `claude gateway` server.
const gatewayProtocolVersion = 1

// handleMetadata serves RFC 8414 authorization server metadata. Claude Code
// ignores endpoints that are not same-origin with the gateway URL, so every
// endpoint is built from the public URL.
func (g *Gateway) handleMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                g.cfg.PublicURL,
		"device_authorization_endpoint":         g.endpoint(deviceAuthPath),
		"token_endpoint":                        g.endpoint(tokenPath),
		"grant_types_supported":                 []string{grantDeviceCode, grantRefreshToken},
		"response_types_supported":              []string{},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"gateway_protocol_version":              gatewayProtocolVersion,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
