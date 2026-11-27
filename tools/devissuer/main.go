// SPDX-License-Identifier: Apache-2.0

// Command devissuer is a fake OIDC issuer for local development, the
// docker-free alternative to navikt/mock-oauth2-server that `mise run dev`
// uses when docker is missing. It signs in one fixed user (dev@example.com)
// with no password and accepts any client secret. DEV ONLY: it is not part of
// the quizzotic binary and must never face a network.
//
//	go run ./tools/devissuer -listen 127.0.0.1:8099 -issuer http://localhost:8099/dev
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address to listen on (keep it on loopback)")
	issuer := flag.String("issuer", "http://localhost:8099/dev", "issuer identifier; endpoints are served under its path")
	flag.Parse()
	if err := run(*listen, *issuer); err != nil {
		fmt.Fprintln(os.Stderr, "devissuer:", err)
		os.Exit(1)
	}
}

func run(listen, issuer string) error {
	iss, err := New(issuer, DevUser)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: listen, Handler: iss.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("dev issuer listening", "addr", listen, "issuer", issuer, "user", DevUser.Email)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
