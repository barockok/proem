package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/barockok/pro-ant/internal/failover"
	"github.com/barockok/pro-ant/internal/metrics"
	"github.com/barockok/pro-ant/internal/pool"
	"github.com/barockok/pro-ant/internal/router"
	"github.com/barockok/pro-ant/internal/store"
	"github.com/prometheus/client_golang/prometheus"
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
	return &Handler{
		loader:     loader,
		router:     r,
		store:      s,
		client:     &http.Client{Timeout: upstreamTimeout},
		stickyMode: stickyMode,
	}
}

// ServeHTTP implements failover loop.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	poolObj := h.loader.Active()
	if poolObj == nil || len(poolObj.Members) == 0 {
		http.Error(w, "no pool members", http.StatusServiceUnavailable)
		return
	}
	clientName := ClientName(req.Context())
	bodyBytes, _ := CloneBody(req)
	// also capture session id
	sessionID := req.Header.Get("x-claude-code-session-id")
	if sessionID == "" {
		sessionID = req.Header.Get("x-session-id")
	}

	tried := make(map[string]bool)
	var lastStatus int
	var lastBody []byte

	for attempt := 0; attempt < len(poolObj.Members); attempt++ {
		// pick next member (filter cooldown internally)
		// we need to filter tried manually
		candidates := filterNotTried(poolObj.Members, tried)
		if len(candidates) == 0 {
			break
		}
		trySticky := h.stickyMode == "redis" && attempt == 0
		member, err := h.router.Pick(req.Context(), candidates, sessionID, trySticky)
		if err != nil || member == nil {
			break
		}

		// sticky hit metric
		if trySticky && h.store != nil {
			if pinned, ok := h.store.GetSticky(req.Context(), sessionID); ok && pinned == member.ID {
				metrics.StickyHits.WithLabelValues("hit").Inc()
			} else if ok {
				metrics.StickyHits.WithLabelValues("miss").Inc()
			}
		}

		tried[member.ID] = true

		status, respHeader, respBody, err := h.forward(req.Context(), req, bodyBytes, *member)
		if err != nil {
			metrics.Requests.WithLabelValues(clientName, member.ID, "error").Inc()
			// treat transport error as failover
			h.cooldown(member, 0)
			metrics.Failovers.WithLabelValues(clientName, member.ID, "transport").Inc()
			lastStatus = http.StatusBadGateway
			continue
		}
		lastStatus = status
		lastBody = respBody

		should, ttl, reason := failover.ShouldFailover(status, respBody, respHeader)
		if should {
			coaTTL := failover.CooldownTTL(ttl, member.CooldownSec, member.Type == pool.TypeAnthropicOAuth || member.Type == pool.TypeAnthropicAPI)
			h.cooldown(member, coaTTL)
			metrics.Failovers.WithLabelValues(clientName, member.ID, reason).Inc()
			metrics.CooldownGauge.WithLabelValues(member.ID).Set(1)
			continue
		}
		// success: pin sticky if redis mode
		if h.stickyMode == "redis" && sessionID != "" && h.store != nil {
			_ = h.store.SetSticky(req.Context(), sessionID, member.ID, time.Hour)
		}
		// record tokens from usage if present
		recordTokens(clientName, member.ID, respBody)
		metrics.Requests.WithLabelValues(clientName, member.ID, itoa(status)).Inc()
		metrics.Latency.WithLabelValues(clientName, member.ID).Observe(time.Since(start).Seconds())
		// write response
		for k, vals := range respHeader {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		// clear cooldown gauge on success
		metrics.CooldownGauge.WithLabelValues(member.ID).Set(0)
		return
	}
	// all failed
	if lastBody != nil {
		w.WriteHeader(lastStatus)
		_, _ = w.Write(lastBody)
		return
	}
	http.Error(w, "all upstreams failed", http.StatusBadGateway)
}

func (h *Handler) forward(ctx context.Context, orig *http.Request, body []byte, m pool.Member) (int, http.Header, []byte, error) {
	target := strings.TrimRight(m.BaseURL, "/") + orig.URL.Path
	if orig.URL.RawQuery != "" {
		target += "?" + orig.URL.RawQuery
	}
	rwBody := RewriteBody(body, m.ModelMap)
	req, err := http.NewRequestWithContext(ctx, orig.Method, target, bytes.NewReader(rwBody))
	if err != nil {
		return 0, nil, nil, err
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

	resp, err := h.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b, nil
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

func recordTokens(clientName, memberID string, body []byte) {
	var env struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Usage == nil {
		return
	}
	if env.Usage.InputTokens > 0 {
		metrics.Tokens.WithLabelValues(clientName, memberID, "input").Add(float64(env.Usage.InputTokens))
	}
	if env.Usage.OutputTokens > 0 {
		metrics.Tokens.WithLabelValues(clientName, memberID, "output").Add(float64(env.Usage.OutputTokens))
	}
	_ = prometheus.NewCounter
}
