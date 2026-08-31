package config

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.ListenAddr != ":8080" {
		t.Fatalf("listen: %s", c.ListenAddr)
	}
	if c.MetricsAddr != ":9090" {
		t.Fatalf("metrics: %s", c.MetricsAddr)
	}
	if c.StickyMode != StickyLB {
		t.Fatalf("sticky: %s", c.StickyMode)
	}
	if c.ConfigPath != "./pool.yaml" {
		t.Fatalf("config path: %s", c.ConfigPath)
	}
}

func TestStickyModeConstants(t *testing.T) {
	if string(StickyLB) != "lb" || string(StickyRedis) != "redis" || string(StickyNone) != "none" {
		t.Fatal("sticky mode constants drifted")
	}
}

func TestParseArgsDefaults(t *testing.T) {
	cfg, err := ParseArgs("pro-ant", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != DefaultConfig() {
		t.Fatalf("empty args should equal defaults, got %+v", cfg)
	}
}

func TestParseArgsOverrides(t *testing.T) {
	cfg, err := ParseArgs("pro-ant", []string{
		"--config", "/etc/pool.yaml",
		"--redis-url", "redis://cache:6379/2",
		"--listen", ":9000",
		"--metrics-addr", ":9100",
		"--sticky-mode", "redis",
		"--read-timeout", "3s",
		"--write-timeout", "7s",
		"--upstream-timeout", "11s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != "/etc/pool.yaml" {
		t.Fatalf("config: %s", cfg.ConfigPath)
	}
	if cfg.RedisURL != "redis://cache:6379/2" {
		t.Fatalf("redis: %s", cfg.RedisURL)
	}
	if cfg.ListenAddr != ":9000" || cfg.MetricsAddr != ":9100" {
		t.Fatalf("addrs: %s %s", cfg.ListenAddr, cfg.MetricsAddr)
	}
	if cfg.StickyMode != StickyRedis {
		t.Fatalf("sticky: %s", cfg.StickyMode)
	}
	if cfg.ReadTimeout != 3*time.Second || cfg.WriteTimeout != 7*time.Second || cfg.UpstreamTimeout != 11*time.Second {
		t.Fatalf("timeouts: %v %v %v", cfg.ReadTimeout, cfg.WriteTimeout, cfg.UpstreamTimeout)
	}
}

func TestParseArgsEveryStickyMode(t *testing.T) {
	for _, mode := range []StickyMode{StickyLB, StickyRedis, StickyNone} {
		cfg, err := ParseArgs("pro-ant", []string{"--sticky-mode", string(mode)})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if cfg.StickyMode != mode {
			t.Fatalf("%s: got %s", mode, cfg.StickyMode)
		}
	}
}

func TestParseArgsInvalidStickyMode(t *testing.T) {
	if _, err := ParseArgs("pro-ant", []string{"--sticky-mode", "bogus"}); err == nil {
		t.Fatal("want error for invalid sticky mode")
	}
}

func TestParseArgsUnknownFlag(t *testing.T) {
	if _, err := ParseArgs("pro-ant", []string{"--nope"}); err == nil {
		t.Fatal("want error for unknown flag")
	}
}

func TestParseArgsBadDuration(t *testing.T) {
	if _, err := ParseArgs("pro-ant", []string{"--read-timeout", "not-a-duration"}); err == nil {
		t.Fatal("want error for bad duration")
	}
}
