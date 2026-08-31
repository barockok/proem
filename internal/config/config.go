package config

import (
	"flag"
	"fmt"
	"time"
)

// StickyMode controls session affinity.
type StickyMode string

const (
	StickyLB    StickyMode = "lb"
	StickyRedis StickyMode = "redis"
	StickyNone  StickyMode = "none"
)

// Config holds proxy runtime options.
type Config struct {
	ConfigPath    string
	RedisURL      string
	ListenAddr    string
	MetricsAddr   string
	StickyMode    StickyMode
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
	UpstreamTimeout time.Duration
}

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		ConfigPath:      "./pool.yaml",
		RedisURL:        "redis://localhost:6379/0",
		ListenAddr:      ":8080",
		MetricsAddr:     ":9090",
		StickyMode:      StickyLB,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    60 * time.Second,
		UpstreamTimeout: 60 * time.Second,
	}
}

// ParseFlags parses CLI flags into Config.
func ParseFlags() (Config, error) {
	cfg := DefaultConfig()
	var sticky string
	flag.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "path to pool.yaml")
	flag.StringVar(&cfg.RedisURL, "redis-url", cfg.RedisURL, "redis URL (redis://...)")
	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "proxy listen addr")
	flag.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "metrics listen addr")
	flag.StringVar(&sticky, "sticky-mode", string(cfg.StickyMode), "sticky mode: lb|redis|none")
	flag.DurationVar(&cfg.ReadTimeout, "read-timeout", cfg.ReadTimeout, "read timeout")
	flag.DurationVar(&cfg.WriteTimeout, "write-timeout", cfg.WriteTimeout, "write timeout")
	flag.DurationVar(&cfg.UpstreamTimeout, "upstream-timeout", cfg.UpstreamTimeout, "upstream request timeout")
	flag.Parse()

	switch StickyMode(sticky) {
	case StickyLB, StickyRedis, StickyNone:
		cfg.StickyMode = StickyMode(sticky)
	default:
		return cfg, fmt.Errorf("invalid --sticky-mode %q: want lb|redis|none", sticky)
	}
	return cfg, nil
}
