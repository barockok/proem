package config

import (
	"flag"
	"fmt"
	"io"
	"os"
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
	ConfigPath      string
	RedisURL        string
	ListenAddr      string
	MetricsAddr     string
	StickyMode      StickyMode
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
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

// ParseFlags parses os.Args into Config.
func ParseFlags() (Config, error) {
	return ParseArgs(os.Args[0], os.Args[1:])
}

// ParseArgs parses an explicit argument list into Config. Kept separate from
// ParseFlags so tests can drive it without touching the global flag set.
func ParseArgs(name string, args []string) (Config, error) {
	cfg := DefaultConfig()
	var sticky string
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "path to pool.yaml")
	fs.StringVar(&cfg.RedisURL, "redis-url", cfg.RedisURL, "redis URL (redis://...)")
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "proxy listen addr")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "metrics listen addr")
	fs.StringVar(&sticky, "sticky-mode", string(cfg.StickyMode), "sticky mode: lb|redis|none")
	fs.DurationVar(&cfg.ReadTimeout, "read-timeout", cfg.ReadTimeout, "read timeout")
	fs.DurationVar(&cfg.WriteTimeout, "write-timeout", cfg.WriteTimeout, "write timeout")
	fs.DurationVar(&cfg.UpstreamTimeout, "upstream-timeout", cfg.UpstreamTimeout, "upstream request timeout")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	switch StickyMode(sticky) {
	case StickyLB, StickyRedis, StickyNone:
		cfg.StickyMode = StickyMode(sticky)
	default:
		return cfg, fmt.Errorf("invalid --sticky-mode %q: want lb|redis|none", sticky)
	}
	return cfg, nil
}
