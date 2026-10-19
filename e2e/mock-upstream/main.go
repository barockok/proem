// Command mock-upstream stands in for api.anthropic.com in the e2e suite. It
// answers the Messages API with canned JSON or SSE and records every request
// it receives, without bodies, for the driver to inspect at GET /_requests.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type recorded struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Authorization string `json:"authorization"`
	APIKey        string `json:"x_api_key"`
	Stream        bool   `json:"stream"`
}

type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

func (rec *recorder) add(r recorded) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.reqs = append(rec.reqs, r)
}

func (rec *recorder) list() []recorded {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]recorded{}, rec.reqs...)
}

func (rec *recorder) reset() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.reqs = nil
}

func main() {
	addr := flag.String("listen", ":8443", "listen address")
	cert := flag.String("tls-cert", "/certs/proem.pem", "TLS certificate")
	key := flag.String("tls-key", "/certs/proem.key", "TLS key")
	health := flag.Bool("healthcheck", false, "probe a running instance and exit")
	flag.Parse()

	// proem only accepts https upstreams. The healthcheck trusts the e2e CA
	// through SSL_CERT_FILE.
	if *health {
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("https://localhost" + *addr + "/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}

	rec := &recorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /_requests", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rec.list())
	})
	mux.HandleFunc("DELETE /_requests", func(w http.ResponseWriter, r *http.Request) { rec.reset() })
	mux.HandleFunc("POST /v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		rec.add(recordOf(r, false))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":12}`))
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		rec.add(recordOf(r, body.Stream))
		if body.Model == "" {
			body.Model = "claude-mock"
		}
		if body.Stream {
			streamReply(w, body.Model)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"msg_mock","type":"message","role":"assistant","model":%q,`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,`+
			`"usage":{"input_tokens":12,"output_tokens":1}}`, body.Model)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec.add(recordOf(r, false))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"not mocked"}}`))
	})
	log.Printf("mock upstream listening %s", *addr)
	log.Fatal(http.ListenAndServeTLS(*addr, *cert, *key, mux))
}

func recordOf(r *http.Request, stream bool) recorded {
	return recorded{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		APIKey:        r.Header.Get("x-api-key"),
		Stream:        stream,
	}
}

// streamReply writes the event sequence of a one-word streamed reply.
func streamReply(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	events := []struct{ name, data string }{
		{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_mock","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":0}}}`, model)},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"ping", `{"type":"ping"}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	for _, e := range events {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, strings.TrimSpace(e.data))
		if flusher != nil {
			flusher.Flush()
		}
	}
}
