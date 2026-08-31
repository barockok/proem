package proxy

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/barockok/pro-ant/internal/pool"
)

const (
	oauthBeta  = "oauth-2025-04-20"
	betaHeader = "anthropic-beta"
)

// AuthHeaders returns headers to inject for member.
func AuthHeaders(m pool.Member) (http.Header, error) {
	h := http.Header{}
	var token string
	if m.Cred.Env != "" {
		token = os.Getenv(m.Cred.Env)
	} else if m.Cred.File != "" {
		b, err := os.ReadFile(m.Cred.File)
		if err != nil {
			return nil, err
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return h, nil
	}
	switch m.Type {
	case pool.TypeAnthropicOAuth:
		h.Set("Authorization", "Bearer "+token)
		h.Set(betaHeader, oauthBeta)
	case pool.TypeAnthropicAPI:
		h.Set("x-api-key", token)
	case pool.TypeOpenRouter, pool.TypeDeepSeek:
		h.Set("Authorization", "Bearer "+token)
	default:
		h.Set("Authorization", "Bearer "+token)
	}
	return h, nil
}

// RewriteBody rewrites model field per ModelMap if present.
func RewriteBody(body []byte, modelMap map[string]string) []byte {
	if len(modelMap) == 0 || len(body) == 0 {
		return body
	}
	// naive JSON replace "model":"old" -> "model":"new"
	// parse via string search to avoid full unmarshal for latency
	s := string(body)
	for from, to := range modelMap {
		s = strings.ReplaceAll(s, "\"model\":\""+from+"\"", "\"model\":\""+to+"\"")
		s = strings.ReplaceAll(s, "\"model\": \""+from+"\"", "\"model\": \""+to+"\"")
		s = strings.ReplaceAll(s, "'model':'"+from+"'", "'model':'"+to+"'")
	}
	return []byte(s)
}

func itoa(n int) string {
	// fast itoa
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// CloneBody reads and returns bytes, leaving req.Body readable.
func CloneBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}
