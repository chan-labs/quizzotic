// SPDX-License-Identifier: Apache-2.0

// Package server wires the HTTP service together: store, migrations, health
// probes, the API router, middleware and graceful shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/driver"
)

// ShutdownTimeout bounds how long in-flight requests get to finish.
const ShutdownTimeout = 15 * time.Second

// Options tunes Run. The zero value is valid.
type Options struct {
	// Listener, when set, replaces listening on cfg.Server.Listen.
	Listener net.Listener
	// HTTPClient, when set, is used for calls to the identity provider.
	HTTPClient *http.Client
}

// Run serves until ctx is cancelled, then shuts down gracefully. It returns
// nil after a clean shutdown.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger, opts Options) error {
	if err := cfg.ValidateForServe(); err != nil {
		return err
	}

	shutdownTracing, err := platform.SetupTracing(ctx)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
		defer cancel()
		if err := shutdownTracing(c); err != nil {
			logger.Warn("tracing shutdown", slog.String("error", err.Error()))
		}
	}()

	st, err := driver.Open(ctx, cfg.Storage.URL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	if cfg.Storage.AutoMigrate {
		applied, err := st.Migrate(ctx)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		logger.Info("migrations checked", slog.Int("applied", len(applied)))
	}

	// v1 validates exactly one issuer (config.Validate), so Issuers[0] is it.
	var provOpts []auth.Option
	if opts.HTTPClient != nil {
		provOpts = append(provOpts, auth.WithHTTPClient(opts.HTTPClient))
	}
	issuer := cfg.Auth.Issuers[0]
	idp := auth.NewProvider(issuer.Issuer, provOpts...)

	var hopts []HandlerOption
	if wc, ok := webClient(issuer); ok {
		clock := domain.SystemClock{}
		ids := domain.NewIDGenerator(clock)
		svc := auth.NewLoginService(auth.LoginConfig{
			ClientID:     wc.ID,
			ClientSecret: wc.Secret,
			Scopes:       issuer.Scopes,
			PublicURL:    cfg.Server.PublicURL,
			SessionTTL:   cfg.Server.SessionTTL.Std(),
		}, st, idp, auth.NewVerifier(issuer.Issuer, idp), auth.NewService(store.AuthTx(st), clock, ids),
			clock, ids, opts.HTTPClient)
		hopts = append(hopts, WithWebLogin(auth.NewLoginHandler(svc, logger)))
	} else {
		logger.Warn("web login disabled: no human client with a secret under auth.issuers[0].clients")
	}

	srv := &http.Server{
		Handler:           Handler(logger, st, idp, hopts...),
		ReadHeaderTimeout: 10 * time.Second,
		// In-flight requests must outlive the signal that starts shutdown.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	ln := opts.Listener
	if ln == nil {
		ln, err = net.Listen("tcp", cfg.Server.Listen)
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}
	logger.Info("listening", slog.String("addr", ln.Addr().String()))

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// HandlerOption adds optional features to Handler.
type HandlerOption func(*handlerOptions)

type handlerOptions struct{ web *auth.LoginHandler }

// WithWebLogin mounts /auth/login, /auth/callback and /auth/logout.
func WithWebLogin(h *auth.LoginHandler) HandlerOption {
	return func(o *handlerOptions) { o.web = h }
}

// webClient picks the confidential human client used for browser login: the
// first human client that has a secret.
func webClient(is config.Issuer) (config.Client, bool) {
	for _, c := range is.Clients {
		if c.Kind == "human" && c.Secret != "" {
			return c, true
		}
	}
	return config.Client{}, false
}

// Handler builds Tracing(RequestID(TraceRoute(mux))) with the health probes
// at the root and the API router under /api/v1. Each feature registers its
// routes on the router here as it lands.
func Handler(logger *slog.Logger, st store.Store, idp *auth.Provider, opts ...HandlerOption) http.Handler {
	var ho handlerOptions
	for _, o := range opts {
		o(&ho)
	}
	health := platform.NewHealth(logger)
	health.AddCheck("jwks", idp.Ready)
	health.AddCheck("store", func(ctx context.Context) error {
		// A no-op transaction proves a connection can be acquired.
		return st.WithTx(ctx, func(store.Store) error { return nil })
	})

	rt := platform.NewRouter()
	openapi.Register(rt)

	mux := http.NewServeMux()
	health.Register(mux)
	rt.Mount(mux)
	if ho.web != nil {
		ho.web.Register(mux)
	}

	return platform.Tracing(nil)(platform.RequestID(logger)(platform.TraceRoute(mux)))
}
