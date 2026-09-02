package proxy

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/barockok/proem/internal/metrics"
	"github.com/barockok/proem/internal/pool"
	"github.com/barockok/proem/internal/router"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// sseUpstream emits Anthropic-shaped stream events, pausing between them so a
// buffering proxy is distinguishable from a streaming one.
func sseUpstream(t *testing.T, gap time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := http.NewResponseController(w)

		events := []string{
			`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_read_input_tokens":1200,"cache_creation_input_tokens":30,"output_tokens":1}}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello "}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}`,
			`event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":42,"output_tokens_details":{"thinking_tokens":7}}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "%s\n\n", e)
			_ = fl.Flush()
			time.Sleep(gap)
		}
	}))
}

func streamHandler(t *testing.T, upstreamURL, memberID string) *Handler {
	t.Helper()
	os.Setenv("TEST_OAT", "tok")
	t.Cleanup(func() { os.Unsetenv("TEST_OAT") })
	m := pool.Member{ID: memberID, Type: pool.TypeGeneric, Cred: pool.CredRef{Env: "TEST_OAT"}, BaseURL: upstreamURL}
	return NewHandler(&mockLoader{pool: &pool.Pool{Members: []pool.Member{m}}}, router.New(nil), nil, "none", 5*time.Second)
}

// The proxy must not hold a stream back: chunks have to reach the client as
// the upstream produces them.
func TestStreamReachesClientIncrementally(t *testing.T) {
	srv := sseUpstream(t, 120*time.Millisecond)
	defer srv.Close()

	proxy := httptest.NewServer(streamHandler(t, srv.URL, "stream-a"))
	defer proxy.Close()

	start := time.Now()
	resp, err := http.Post(proxy.URL+"/v1/messages", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content type not preserved: %q", ct)
	}

	var firstChunk time.Duration
	var lines int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			if lines == 0 {
				firstChunk = time.Since(start)
			}
			lines++
		}
	}
	if lines != 5 {
		t.Fatalf("expected 5 data lines, got %d", lines)
	}
	// Five events, 120ms apart: a buffering proxy could not deliver the first
	// in well under the ~600ms the whole stream takes.
	if firstChunk > 400*time.Millisecond {
		t.Fatalf("first chunk arrived after %v, so the proxy buffered the stream", firstChunk)
	}
}

func TestStreamingUsageIsRecorded(t *testing.T) {
	srv := sseUpstream(t, 0)
	defer srv.Close()
	h := streamHandler(t, srv.URL, "stream-usage")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"stream":true}`)))

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	for _, c := range []struct {
		kind string
		want float64
	}{
		{"input", 11}, {"output", 42}, {"cache_read", 1200}, {"cache_creation", 30},
	} {
		got := testutil.ToFloat64(metrics.Tokens.WithLabelValues(UnknownForTest, "stream-usage", c.kind))
		if got != c.want {
			t.Fatalf("%s tokens: got %v want %v", c.kind, got, c.want)
		}
	}
	if got := testutil.ToFloat64(metrics.ThinkingTokens.WithLabelValues(UnknownForTest, "stream-usage")); got != 7 {
		t.Fatalf("thinking tokens: %v", got)
	}
}

// The same counters must be filled by a plain JSON response, so accounting does
// not depend on which shape the client asked for.
func TestNonStreamingUsageIsRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","usage":{"input_tokens":5,"output_tokens":9,
			"cache_read_input_tokens":700,"cache_creation_input_tokens":12,
			"output_tokens_details":{"thinking_tokens":3}}}`))
	}))
	defer srv.Close()
	h := streamHandler(t, srv.URL, "json-usage")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{}`)))

	for _, c := range []struct {
		kind string
		want float64
	}{
		{"input", 5}, {"output", 9}, {"cache_read", 700}, {"cache_creation", 12},
	} {
		got := testutil.ToFloat64(metrics.Tokens.WithLabelValues(UnknownForTest, "json-usage", c.kind))
		if got != c.want {
			t.Fatalf("%s tokens: got %v want %v", c.kind, got, c.want)
		}
	}
}

// Bytes must arrive byte-identical: the observer reads along, it does not edit.
func TestStreamBodyIsUnmodified(t *testing.T) {
	const payload = "event: x\ndata: {\"type\":\"noise\",\"v\":\"\\u00e9\\u00df\"}\n\nraw trailing bytes\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	h := streamHandler(t, srv.URL, "verbatim")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{}`)))

	if rec.Body.String() != payload {
		t.Fatalf("body altered.\n got: %q\nwant: %q", rec.Body.String(), payload)
	}
}

// A failover-eligible response is still inspected and retried, which is only
// possible because nothing was written to the client first.
func TestStreamingRequestStillFailsOver(t *testing.T) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
	}))
	defer limited.Close()
	good := sseUpstream(t, 0)
	defer good.Close()

	os.Setenv("TEST_OAT", "tok")
	defer os.Unsetenv("TEST_OAT")
	m1 := pool.Member{ID: "limited", Type: pool.TypeGeneric, Cred: pool.CredRef{Env: "TEST_OAT"}, BaseURL: limited.URL, Weight: 1}
	m2 := pool.Member{ID: "good", Type: pool.TypeGeneric, Cred: pool.CredRef{Env: "TEST_OAT"}, BaseURL: good.URL, Weight: 1}
	h := NewHandler(&mockLoader{pool: &pool.Pool{Members: []pool.Member{m1, m2}}}, router.New(nil), nil, "none", 5*time.Second)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"stream":true}`)))

	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "message_start") {
		t.Fatalf("did not fail over to the streaming member: %s", rec.Body.String())
	}
}
