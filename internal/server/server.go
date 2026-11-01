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

	var provOpts []auth.Option
	if opts.HTTPClient != nil {
		provOpts = append(provOpts, auth.WithHTTPClient(opts.HTTPClient))
	}
	idp := auth.NewProvider(cfg.Auth.Issuers[0].Issuer, provOpts...)

	hopts, err := authOptions(cfg, logger, st, idp, opts.HTTPClient)
	if err != nil {
		return err
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

// authOptions wires API and web authentication for the configured issuer:
// bearer tokens always, the session cookie when a web client is configured.
func authOptions(cfg *config.Config, logger *slog.Logger, st store.Store, idp *auth.Provider, httpClient *http.Client) ([]HandlerOption, error) {
	// v1 validates exactly one issuer (config.Validate), so Issuers[0] is it.
	issuer := cfg.Auth.Issuers[0]
	clock := domain.SystemClock{}
	ids := domain.NewIDGenerator(clock)
	authSvc := auth.NewService(store.AuthTx(st), clock, ids)

	// Bearer tokens: verified against the issuer, classified by client.
	clients, err := auth.NewClients(issuer.Clients)
	if err != nil {
		return nil, fmt.Errorf("auth clients: %w", err)
	}
	hopts := []HandlerOption{WithAPIAuth(&auth.BearerAuthenticator{
		Verifier: auth.NewVerifier(issuer.Issuer, idp),
		Audience: issuer.Audience,
		Resolver: auth.NewResolver(clients, authSvc),
	})}

	if wc, ok := webClient(issuer); ok {
		svc := auth.NewLoginService(auth.LoginConfig{
			ClientID:     wc.ID,
			ClientSecret: wc.Secret,
			Scopes:       issuer.Scopes,
			PublicURL:    cfg.Server.PublicURL,
			SessionTTL:   cfg.Server.SessionTTL.Std(),
		}, st, idp, auth.NewVerifier(issuer.Issuer, idp), authSvc,
			clock, ids, httpClient)
		login := auth.NewLoginHandler(svc, logger)
		hopts = append(hopts, WithWebLogin(login), WithAPIAuth(auth.SessionAuthenticator{Login: login}))
	} else {
		logger.Warn("web login disabled: no human client with a secret under auth.issuers[0].clients")
	}
	return hopts, nil
}

// HandlerOption adds optional features to Handler.
type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	web  *auth.LoginHandler
	auth []auth.Authenticator
	// extra operations and routes registered beside the spec's; set only by
	// tests, through the same router and guard as production routes.
	extraOps      []openapi.Operation
	extraRegister func(*platform.Router)
}

// WithAPIAuth adds an authenticator for the API, tried after those already
// added (see auth.Chain). With none, every scoped operation answers 401.
func WithAPIAuth(a auth.Authenticator) HandlerOption {
	return func(o *handlerOptions) { o.auth = append(o.auth, a) }
}

// NewAPIRouter returns the API router with every feature registered and the
// scope guard installed, so each request is authorized against the spec
// operation of the pattern the router matched. It panics if the embedded spec
// cannot be parsed (a build-time invariant covered by tests).
func NewAPIRouter(a auth.Authenticator) (*platform.Router, *auth.ScopeGuard) {
	return newAPIRouter(a, nil, nil)
}

func newAPIRouter(a auth.Authenticator, extraOps []openapi.Operation, extraRegister func(*platform.Router), gopts ...auth.GuardOption) (*platform.Router, *auth.ScopeGuard) {
	ops, err := openapi.SpecOperations()
	if err != nil {
		panic("server: embedded OpenAPI spec: " + err.Error())
	}
	guard := auth.NewScopeGuard(a, append(ops, extraOps...), gopts...)
	rt := platform.NewRouter()
	rt.SetAuthorizer(guard.Authorize)
	openapi.Register(rt)
	if extraRegister != nil {
		extraRegister(rt)
	}
	return rt, guard
}

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

	rt, _ := newAPIRouter(auth.Chain(ho.auth), ho.extraOps, ho.extraRegister, auth.WithGuardLogger(logger))

	mux := http.NewServeMux()
	health.Register(mux)
	rt.Mount(mux)
	if ho.web != nil {
		ho.web.Register(mux)
	}

	return platform.Tracing(nil)(platform.RequestID(logger)(platform.TraceRoute(mux)))
}
