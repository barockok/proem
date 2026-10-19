// Package app wires the proxy's components together. Keeping the wiring here
// (rather than in main) lets tests exercise startup, routing and shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/barockok/proem/internal/client"
	"github.com/barockok/proem/internal/clientip"
	"github.com/barockok/proem/internal/config"
	"github.com/barockok/proem/internal/gateway"
	"github.com/barockok/proem/internal/metrics"
	"github.com/barockok/proem/internal/pool"
	"github.com/barockok/proem/internal/proxy"
	"github.com/barockok/proem/internal/router"
	"github.com/barockok/proem/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// App holds the running proxy's components.
type App struct {
	cfg     config.Config
	loader  *pool.Loader
	clients *client.Loader
	store   *store.Store
	gwRedis redis.UniversalClient
	done    chan struct{}

	handler     http.Handler
	metricsMux  http.Handler
	shutdownDur time.Duration
}

// New builds an App from cfg: loads the pool, starts the config watcher and
// connects to Redis. A Redis failure is non-fatal: the proxy runs fail-open
// with cooldown and sticky disabled. With the SSO gateway enabled, Redis
// holds device grants, so an unreachable Redis is a startup error.
func New(cfg config.Config) (*App, error) {
	return NewWithProvider(cfg, nil)
}

// NewWithProvider is New with an injected identity provider for the SSO
// gateway; nil builds the OpenID Connect provider from cfg.
func NewWithProvider(cfg config.Config, provider gateway.Provider) (*App, error) {
	logger := newLogger(cfg)

	resolver, err := clientip.NewResolver(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("trusted proxies: %w", err)
	}

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

	var gw *gateway.Gateway
	var gwRedis redis.UniversalClient
	if cfg.Gateway.Enabled() {
		gw, gwRedis, err = newGateway(cfg, provider, clients, logger)
		if err != nil {
			close(done)
			_ = loader.Close()
			_ = clients.Close()
			if st != nil {
				_ = st.Close()
			}
			return nil, err
		}
	}

	h := proxy.NewHandler(loader, router.New(st), st, string(cfg.StickyMode), cfg.UpstreamTimeout)

	mux := chi.NewRouter()
	// Claude Code probes /api/hello for reachability before it sends a request,
	// and does so without credentials. Answering locally keeps that probe out
	// of the auth-failure metric without letting unauthenticated traffic reach
	// an upstream.
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	// RealIP runs outermost so both the access log and auth rejections see the
	// caller's address; AccessLog wraps Auth so rejected requests are logged too.
	var sessions proxy.SessionVerifier
	if gw != nil {
		sessions = gw.Sessions()
	}
	var handler http.Handler = proxy.AuthWithSessions(clients, sessions, logger, h)
	if cfg.AccessLog {
		handler = proxy.AccessLog(logger, handler)
	}
	mux.Handle("/*", proxy.RealIP(resolver, handler))
	if gw != nil {
		// Gateway routes get the same RealIP and access log as the proxy, so
		// rate limits and logs use the caller's address.
		mux.Group(func(r chi.Router) {
			r.Use(func(next http.Handler) http.Handler {
				if cfg.AccessLog {
					next = proxy.AccessLog(logger, next)
				}
				return proxy.RealIP(resolver, next)
			})
			gw.Routes(r)
		})
	}

	return &App{
		cfg:         cfg,
		loader:      loader,
		clients:     clients,
		store:       st,
		gwRedis:     gwRedis,
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
		log.Printf("proxy listening %s (sticky=%s, tls=%t, gateway=%t)",
			a.cfg.ListenAddr, a.cfg.StickyMode, a.cfg.TLSCert != "", a.cfg.Gateway.Enabled())
		var err error
		if a.cfg.TLSCert != "" {
			err = srv.ListenAndServeTLS(a.cfg.TLSCert, a.cfg.TLSKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// newGateway builds the SSO gateway and checks that Redis answers: without
// it no one can log in.
func newGateway(cfg config.Config, provider gateway.Provider, clients *client.Loader, logger *slog.Logger) (*gateway.Gateway, redis.UniversalClient, error) {
	g := cfg.Gateway
	rdb, err := gateway.NewRedisClient(cfg.RedisURL)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway redis: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, nil, fmt.Errorf("gateway redis unreachable: %w", err)
	}
	if provider == nil {
		provider = gateway.NewOIDCProvider(gateway.OIDCConfig{
			Issuer:       g.OIDCIssuer,
			ClientID:     g.OIDCClientID,
			ClientSecret: g.OIDCClientSecret,
			RedirectURL:  gateway.RedirectURL(g.PublicURL),
			Scopes:       g.OIDCScopes,
			AuthParams:   g.OIDCAuthParams,
		})
	}
	gw, err := gateway.New(gateway.Config{
		PublicURL:     g.PublicURL,
		AllowedDomain: g.OIDCAllowedDomain,
		JWTSecrets:    g.JWTSecrets,
		RefreshKeys:   g.RefreshKeys,
		SessionTTL:    g.SessionTTL,
		SessionMax:    g.SessionMax,
		GrantTTL:      g.DeviceGrantTTL,
		RateLimit:     g.DeviceRateLimit,
		RateWindow:    g.DeviceRateWindow,
	}, provider, rdb, clients, logger)
	if err != nil {
		_ = rdb.Close()
		return nil, nil, err
	}
	return gw, rdb, nil
}

// newLogger builds the structured logger used for access and auth logging.
func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
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
	if a.gwRedis != nil {
		_ = a.gwRedis.Close()
	}
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}
