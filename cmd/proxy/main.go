package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/barockok/pro-ant/internal/config"
	"github.com/barockok/pro-ant/internal/metrics"
	"github.com/barockok/pro-ant/internal/pool"
	"github.com/barockok/pro-ant/internal/proxy"
	"github.com/barockok/pro-ant/internal/router"
	"github.com/barockok/pro-ant/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	cfg, err := config.ParseFlags()
	if err != nil { log.Fatal(err) }

	loader, err := pool.NewLoader(cfg.ConfigPath)
	if err != nil { log.Fatalf("pool load: %v", err) }
	defer loader.Close()

	done := make(chan struct{})
	loader.Watch(done, func(e error) {
		log.Printf("pool reload error: %v", e)
		metrics.ConfigReloads.WithLabelValues("error").Inc()
	})
	// count success reload via wrapper? For now inc on every watch event is not tracked; we inc success on reload not error.
	// simpler: inc success when no error (handled inside Watch error callback only)
	// also track initial load
	metrics.ConfigReloads.WithLabelValues("success").Inc()

	var st *store.Store
	if cfg.RedisURL != "" {
		st, err = store.New(cfg.RedisURL)
		if err != nil { log.Printf("redis disabled: %v", err); st=nil }
	}

	rtr := router.New(st)
	h := proxy.NewHandler(loader, rtr, st, string(cfg.StickyMode))

	// main router
	mux := chi.NewRouter()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("/*", h)

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: mux,
		ReadTimeout: cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
	metricsSrv := &http.Server{
		Addr: cfg.MetricsAddr,
		Handler: promhttp.Handler(),
	}

	go func() {
		log.Printf("proxy listening %s (sticky=%s)", cfg.ListenAddr, cfg.StickyMode)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed { log.Fatal(err) }
	}()
	go func() {
		log.Printf("metrics listening %s", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed { log.Printf("metrics: %v", err) }
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	close(done)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	_ = metricsSrv.Shutdown(ctx)
	if st!=nil { _ = st.Close() }
}
