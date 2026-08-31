package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	Requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proant_requests_total",
		Help: "Total requests by member and status",
	}, []string{"member", "code"})

	Failovers = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proant_failovers_total",
		Help: "Failovers by from_member and reason",
	}, []string{"from_member", "reason"})

	Tokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proant_tokens_total",
		Help: "Tokens by member and type",
	}, []string{"member", "type"})

	Latency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proant_upstream_latency_seconds",
		Help:    "Upstream latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"member"})

	CooldownGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "proant_member_cooldown",
		Help: "1 if member in cooldown",
	}, []string{"member"})

	StickyHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proant_sticky_hits_total",
		Help: "Sticky hits",
	}, []string{"result"})

	ConfigReloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proant_config_reloads_total",
		Help: "Config reloads",
	}, []string{"result"})
)
