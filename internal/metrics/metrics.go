package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	Requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proem_requests_total",
		Help: "Total requests by client, member and status",
	}, []string{"client", "member", "code"})

	Failovers = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proem_failovers_total",
		Help: "Failovers by client, from_member and reason",
	}, []string{"client", "from_member", "reason"})

	Tokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proem_tokens_total",
		Help: "Tokens by client, member and type",
	}, []string{"client", "member", "type"})

	Latency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proem_upstream_latency_seconds",
		Help:    "Upstream latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"client", "member"})

	CooldownGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "proem_member_cooldown",
		Help: "1 if member in cooldown",
	}, []string{"member"})

	StickyHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proem_sticky_hits_total",
		Help: "Sticky hits",
	}, []string{"result"})

	ConfigReloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proem_config_reloads_total",
		Help: "Config reloads",
	}, []string{"result"})
)
