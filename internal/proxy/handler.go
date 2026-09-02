package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/barockok/proem/internal/failover"
	"github.com/barockok/proem/internal/metrics"
	"github.com/barockok/proem/internal/pool"
	"github.com/barockok/proem/internal/router"
	"github.com/barockok/proem/internal/store"
)

type Handler struct {
	loader     interface{ Active() *pool.Pool }
	router     *router.Router
	store      *store.Store
	client     *http.Client
	stickyMode string // lb|redis|none
}

func NewHandler(loader interface{ Active() *pool.Pool }, r *router.Router, s *store.Store, stickyMode string, upstreamTimeout time.Duration) *Handler {
	if upstreamTimeout <= 0 {
		upstreamTimeout = 60 * time.Second
	}
	// The timeout bounds how long an upstream may take to respond, not how
	// long it may stream. http.Client.Timeout would cover the entire body
	// read and so would cut off any generation that ran longer than it.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = upstreamTimeout
	return &Handler{
		loader:     loader,
		router:     r,
		store:      s,
		client:     &http.Client{Transport: transport},
		stickyMode: stickyMode,
	}
}

// ServeHTTP routes a request to a pool member, failing over to another member
// when one is rate limited.
//
// The proxy is transparent to the response shape. Failover needs to inspect a
// response, but bytes already sent cannot be recalled, so the decision is made
// from the status and headers before anything is committed to the client:
// a response that could still fail over is buffered and examined, and any
// other response is streamed straight through, unbuffered and unmodified,
// whether it is a single JSON object or an event stream.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	poolObj := h.loader.Active()
	if poolObj == nil || len(poolObj.Members) == 0 {
		http.Error(w, "no pool members", http.StatusServiceUnavailable)
		return
	}
	clientName := ClientName(req.Context())
	bodyBytes, _ := CloneBody(req)
	sessionID := req.Header.Get("x-claude-code-session-id")
	if sessionID == "" {
		sessionID = req.Header.Get("x-session-id")
	}

	tried := make(map[string]bool)
	var last *bufferedResponse

	for attempt := 0; attempt < len(poolObj.Members); attempt++ {
		candidates := filterNotTried(poolObj.Members, tried)
		if len(candidates) == 0 {
			break
		}
		trySticky := h.stickyMode == "redis" && attempt == 0
		member, err := h.router.Pick(req.Context(), candidates, sessionID, trySticky)
		if err != nil || member == nil {
			break
		}

		if trySticky && h.store != nil {
			if pinned, ok := h.store.GetSticky(req.Context(), sessionID); ok && pinned == member.ID {
				metrics.StickyHits.WithLabelValues("hit").Inc()
			} else if ok {
				metrics.StickyHits.WithLabelValues("miss").Inc()
			}
		}

		tried[member.ID] = true

		resp, err := h.send(req, bodyBytes, *member)
		if err != nil {
			metrics.Requests.WithLabelValues(clientName, member.ID, "error").Inc()
			h.cooldown(member, 0)
			metrics.Failovers.WithLabelValues(clientName, member.ID, "transport").Inc()
			last = &bufferedResponse{status: http.StatusBadGateway}
			continue
		}

		// Only a response that could still fail over is read up front; every
		// other response is committed to the client without buffering.
		if failover.MayFailover(resp.StatusCode, resp.Header) {
			buffered := bufferResponse(resp)
			should, ttl, reason := failover.ShouldFailover(buffered.status, buffered.body, buffered.header)
			if should {
				ttlSec := failover.CooldownTTL(ttl, member.CooldownSec,
					member.Type == pool.TypeAnthropicOAuth || member.Type == pool.TypeAnthropicAPI)
				h.cooldown(member, ttlSec)
				metrics.Failovers.WithLabelValues(clientName, member.ID, reason).Inc()
				metrics.CooldownGauge.WithLabelValues(member.ID).Set(1)
				last = buffered
				continue
			}
			h.finish(w, req, clientName, member, sessionID, start, buffered.status,
				buffered.header, bytes.NewReader(buffered.body))
			return
		}

		h.finish(w, req, clientName, member, sessionID, start, resp.StatusCode, resp.Header, resp.Body)
		_ = resp.Body.Close()
		return
	}

	if last != nil && last.body != nil {
		writeHeaders(w, last.header)
		w.WriteHeader(last.status)
		_, _ = w.Write(last.body)
		return
	}
	http.Error(w, "all upstreams failed", http.StatusBadGateway)
}

// finish commits a chosen response to the client and records what it carried.
// The body is copied through as it arrives, flushing each chunk so a stream
// reaches the caller incrementally, while a usage observer reads along without
// altering or delaying the bytes.
func (h *Handler) finish(w http.ResponseWriter, req *http.Request, clientName string, member *pool.Member,
	sessionID string, start time.Time, status int, header http.Header, body io.Reader) {

	if h.stickyMode == "redis" && sessionID != "" && h.store != nil {
		_ = h.store.SetSticky(req.Context(), sessionID, member.ID, time.Hour)
	}

	writeHeaders(w, header)
	w.WriteHeader(status)

	observer := newUsageObserver(header.Get("Content-Type"))
	copyStream(w, body, observer)

	recordUsage(clientName, member.ID, observer.Result())
	metrics.Requests.WithLabelValues(clientName, member.ID, itoa(status)).Inc()
	metrics.Latency.WithLabelValues(clientName, member.ID).Observe(time.Since(start).Seconds())
	metrics.CooldownGauge.WithLabelValues(member.ID).Set(0)
}

// copyStream forwards the body to the client, flushing after every chunk so
// streamed responses are not held back, and tees a copy to the observer.
func copyStream(w http.ResponseWriter, body io.Reader, observer io.Writer) {
	flusher := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = observer.Write(chunk)
			if _, writeErr := w.Write(chunk); writeErr != nil {
				return // client hung up
			}
			_ = flusher.Flush()
		}
		if readErr != nil {
			return
		}
	}
}

func writeHeaders(w http.ResponseWriter, header http.Header) {
	for k, vals := range header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
}

// bufferedResponse is a response read into memory so it can be inspected for
// failover before anything is sent to the client.
type bufferedResponse struct {
	status int
	header http.Header
	body   []byte
}

func bufferResponse(resp *http.Response) *bufferedResponse {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return &bufferedResponse{status: resp.StatusCode, header: resp.Header, body: body}
}

// send issues the upstream request and returns the live response with its
// body still unread, so the caller decides whether to buffer or stream it.
func (h *Handler) send(orig *http.Request, body []byte, m pool.Member) (*http.Response, error) {
	ctx := orig.Context()
	target := strings.TrimRight(m.BaseURL, "/") + orig.URL.Path
	if orig.URL.RawQuery != "" {
		target += "?" + orig.URL.RawQuery
	}
	rwBody := RewriteBody(body, m.ModelMap)
	req, err := http.NewRequestWithContext(ctx, orig.Method, target, bytes.NewReader(rwBody))
	if err != nil {
		return nil, err
	}
	// copy headers
	for k, vals := range orig.Header {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	// inject auth. The beta header is merged rather than replaced so the
	// client's own anthropic-beta values survive alongside the oauth beta.
	// Header keys are canonicalised by net/http, so compare case-insensitively.
	ah, _ := AuthHeaders(m)
	for k, vals := range ah {
		for _, v := range vals {
			if !strings.EqualFold(k, betaHeader) {
				req.Header.Set(k, v)
				continue
			}
			existing := req.Header.Get(betaHeader)
			switch {
			case existing == "":
				req.Header.Set(betaHeader, v)
			case !strings.Contains(existing, v):
				req.Header.Set(betaHeader, existing+","+v)
			}
		}
	}
	if len(rwBody) > 0 {
		req.Header.Set("Content-Length", itoa(len(rwBody)))
	}

	return h.client.Do(req)
}

func (h *Handler) cooldown(m *pool.Member, ttlSec int) {
	if h.store == nil || m == nil {
		return
	}
	if ttlSec <= 0 {
		ttlSec = m.CooldownSec
	}
	if ttlSec <= 0 {
		ttlSec = 60
	}
	_ = h.store.SetCooldown(context.Background(), m.ID, time.Duration(ttlSec)*time.Second)
}

func filterNotTried(members []pool.Member, tried map[string]bool) []pool.Member {
	var out []pool.Member
	for _, m := range members {
		if !tried[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

func recordUsage(clientName, memberID string, u tokenUsage) {
	if u.empty() {
		return
	}
	add := func(kind string, n int) {
		if n > 0 {
			metrics.Tokens.WithLabelValues(clientName, memberID, kind).Add(float64(n))
		}
	}
	add("input", u.Input)
	add("output", u.Output)
	add("cache_read", u.CacheRead)
	add("cache_creation", u.CacheCreation)
	// Thinking tokens are part of the output total, so they are reported on
	// their own metric rather than as a fourth type that would double-count.
	if u.Thinking > 0 {
		metrics.ThinkingTokens.WithLabelValues(clientName, memberID).Add(float64(u.Thinking))
	}
}
