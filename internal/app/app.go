// Package app wires the proxy's components together. Keeping the wiring here
// (rather than in main) lets tests exercise startup, routing and shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/barockok/proem/internal/client"
	"github.com/barockok/proem/internal/config"
	"github.com/barockok/proem/internal/metrics"
	"github.com/barockok/proem/internal/pool"
	"github.com/barockok/proem/internal/proxy"
	"github.com/barockok/proem/internal/router"
	"github.com/barockok/proem/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// App holds the running proxy's components.
type App struct {
	cfg     config.Config
	loader  *pool.Loader
	clients *client.Loader
	store   *store.Store
	done    chan struct{}

	handler     http.Handler
	metricsMux  http.Handler
	shutdownDur time.Duration
}

// New builds an App from cfg: loads the pool, starts the config watcher and
// connects to Redis. A Redis failure is non-fatal: the proxy runs fail-open
// with cooldown and sticky disabled.
func New(cfg config.Config) (*App, error) {
	loader, err := pool.NewLoader(cfg.ConfigPath)
	if err != nil {
		return nil, err
	}

	clients, err := client.NewLoader(cfg.ClientsPath)
	if err != nil {
		_ = loader.Close()
		return nil, fmt.Errorf("clients: %w", err)
	}

	done := make(chan struct{})
	loader.Watch(done, func(e error) {
		log.Printf("pool reload error: %v", e)
		metrics.ConfigReloads.WithLabelValues("error").Inc()
	})
	clients.Watch(done, func(e error) {
		log.Printf("clients reload error: %v", e)
		metrics.ConfigReloads.WithLabelValues("error").Inc()
	})
	metrics.ConfigReloads.WithLabelValues("success").Inc()

	var st *store.Store
	if cfg.RedisURL != "" {
		st, err = store.New(cfg.RedisURL)
		if err != nil {
			log.Printf("redis disabled: %v", err)
			st = nil
		}
	}

	h := proxy.NewHandler(loader, router.New(st), st, string(cfg.StickyMode), cfg.UpstreamTimeout)

	mux := chi.NewRouter()
	mux.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/*", proxy.Auth(clients, h))

	return &App{
		cfg:         cfg,
		loader:      loader,
		clients:     clients,
		store:       st,
		done:        done,
		handler:     mux,
		metricsMux:  promhttp.Handler(),
		shutdownDur: 5 * time.Second,
	}, nil
}

// Handler returns the proxy HTTP handler (health route plus failover proxy).
func (a *App) Handler() http.Handler { return a.handler }

// MetricsHandler returns the Prometheus handler served on the metrics port.
func (a *App) MetricsHandler() http.Handler { return a.metricsMux }

// Run serves the proxy and metrics listeners until ctx is cancelled, then
// shuts both down gracefully.
func (a *App) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:         a.cfg.ListenAddr,
		Handler:      a.handler,
		ReadTimeout:  a.cfg.ReadTimeout,
		WriteTimeout: a.cfg.WriteTimeout,
	}
	metricsSrv := &http.Server{
		Addr:    a.cfg.MetricsAddr,
		Handler: a.metricsMux,
	}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("proxy listening %s (sticky=%s)", a.cfg.ListenAddr, a.cfg.StickyMode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		log.Printf("metrics listening %s", a.cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), a.shutdownDur)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	_ = metricsSrv.Shutdown(shutCtx)
	return runErr
}

// Close stops the config watcher and releases the Redis connection.
func (a *App) Close() error {
	select {
	case <-a.done:
	default:
		close(a.done)
	}
	if a.loader != nil {
		_ = a.loader.Close()
	}
	if a.clients != nil {
		_ = a.clients.Close()
	}
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}
