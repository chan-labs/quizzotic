// SPDX-License-Identifier: Apache-2.0

// Package serverrun starts the real server for tests the way `quizzotic
// serve` does: server.Run with a real config.Config on a bound listener.
package serverrun

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
)

// ShutdownWait bounds how long Stop waits for Run to return.
const ShutdownWait = 10 * time.Second

// Server is a running server.Run.
type Server struct {
	// URL is the base URL, e.g. "http://127.0.0.1:41234", without a slash.
	URL string
	// Client talks to URL.
	Client *http.Client

	stop func()
}

// Stop cancels Run and waits for it to return; it fails the test if Run
// returns an error or does not stop in time. It is idempotent and also runs
// at cleanup.
func (s *Server) Stop() { s.stop() }

// Start runs server.Run(cfg) on a fresh loopback listener and returns once the
// server answers /healthz. The listener is bound before Run starts, so
// requests queue until it serves; there is no polling. The test fails if Run
// returns before it is ready. cfg must not be changed after Start.
func Start(t testing.TB, cfg *config.Config) *Server {
	t.Helper()
	return StartWith(t, cfg, server.Options{})
}

// StartWith is Start with extra server options; opts.Listener is always
// replaced by Start's own.
func StartWith(t testing.TB, cfg *config.Config, opts server.Options) *Server {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// t.Context is cancelled just before cleanups run, which starts the
	// shutdown; Stop (a cleanup) then waits for it.
	ctx, cancel := context.WithCancel(t.Context())
	opts.Listener = ln
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = server.Run(ctx, cfg, slog.New(slog.DiscardHandler), opts)
	}()

	var (
		once  sync.Once
		early bool // Run already returned and was reported by Start
	)
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
				if runErr != nil && !early {
					t.Errorf("server.Run returned %v", runErr)
				}
			case <-time.After(ShutdownWait):
				t.Error("server did not shut down")
			}
		})
	}
	t.Cleanup(stop)

	s := &Server{URL: "http://" + ln.Addr().String(), Client: &http.Client{}, stop: stop}
	ready := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/healthz", nil)
		if err != nil {
			ready <- err
			return
		}
		resp, err := s.Client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("server did not start: %v", err)
		}
	case <-done:
		early = true
		_ = ln.Close()
		t.Fatalf("server.Run returned before serving: %v", runErr)
	}
	return s
}
