package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/barockok/pro-ant/internal/pool"
)

const oauthBeta = "oauth-2025-04-20"

// Forwarder builds reverse proxy director per member.
type Forwarder struct{}

// AuthHeaders returns headers to inject for member.
func AuthHeaders(m pool.Member) (http.Header, error) {
	h := http.Header{}
	var token string
	if m.Cred.Env != "" {
		token = os.Getenv(m.Cred.Env)
	} else if m.Cred.File != "" {
		b, err := os.ReadFile(m.Cred.File)
		if err != nil { return nil, err }
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return h, nil
	}
	switch m.Type {
	case pool.TypeAnthropicOAuth:
		h.Set("Authorization", "Bearer "+token)
		h.Set("anthropic-beta", oauthBeta)
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
	if len(modelMap)==0 || len(body)==0 { return body }
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

// NewProxy creates reverse proxy to target member.
func NewProxy(m pool.Member, origReq *http.Request, bodyBytes []byte) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(m.BaseURL)
	if err != nil { return nil, err }
	rwBody := RewriteBody(bodyBytes, m.ModelMap)
	authHeaders, err := AuthHeaders(m)
	if err != nil { return nil, err }

	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		// keep original path/query; override host
		req.Host = target.Host
		// inject auth
		for k, vals := range authHeaders {
			for _, v := range vals {
				if k == "anthropic-beta" {
					// merge with existing beta header if present
					existing := req.Header.Get("anthropic-beta")
					if existing != "" && !strings.Contains(existing, v) {
						req.Header.Set("anthropic-beta", existing+","+v)
					} else if existing == "" {
						req.Header.Set(k, v)
					}
				} else {
					req.Header.Set(k, v)
				}
			}
		}
		if len(rwBody)>0 {
			req.Body = io.NopCloser(bytes.NewReader(rwBody))
			req.ContentLength = int64(len(rwBody))
			req.Header.Set("Content-Length", string(rune(len(rwBody))))
			// actually set correctly
			req.Header.Set("Content-Length", itoa(len(rwBody)))
		}
		// strip hop headers handled by ReverseProxy
	}

	proxy := &httputil.ReverseProxy{
		Director: director,
		ModifyResponse: func(resp *http.Response) error {
			// preserve response as-is; failover detection happens in handler
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
	_ = origReq
	return proxy, nil
}

func itoa(n int) string {
	// fast itoa
	if n==0 { return "0" }
	buf := [20]byte{}
	pos := len(buf)
	for n>0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n/=10
	}
	return string(buf[pos:])
}

// CloneBody reads and returns bytes, leaving req.Body readable.
func CloneBody(r *http.Request) ([]byte, error) {
	if r.Body==nil { return nil, nil }
	b, err := io.ReadAll(r.Body)
	if err != nil { return nil, err }
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}
