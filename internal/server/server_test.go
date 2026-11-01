// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
)

func TestServeStartsAndShutsDown(t *testing.T) {
	data, err := os.ReadFile("../../quizzotic.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(data, []string{})
	if err != nil {
		t.Fatalf("example config must load: %v", err)
	}
	cfg.Storage.URL = "sqlite://" + filepath.Join(t.TempDir(), "q.db")

	// A stand-in IdP: /readyz includes the JWKS check, so the issuer must answer.
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, idp.URL, idp.URL+"/jwks")
		case "/jwks":
			fmt.Fprint(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"k1","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	cfg.Auth.Issuers[0].Issuer = idp.URL

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, cfg, logger, server.Options{Listener: ln}) }()

	base := "http://" + ln.Addr().String()
	get := func(path string) int {
		t.Helper()
		var lastErr error
		for i := 0; i < 100; i++ {
			resp, err := http.Get(base + path)
			if err == nil {
				resp.Body.Close()
				return resp.StatusCode
			}
			lastErr = err
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("GET %s: %v", path, lastErr)
		return 0
	}
	for _, p := range []string{"/healthz", "/readyz", "/api/v1/openapi.yaml"} {
		if got := get(p); got != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, got)
		}
	}
	if got := get("/api/v1/nope"); got != http.StatusNotFound {
		t.Errorf("unknown API path = %d, want 404", got)
	}

	cancel() // stands in for SIGTERM, which cancels the same context
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestRunRejectsIncompleteConfig(t *testing.T) {
	cfg := config.Default() // no public_url, no issuer
	err := server.Run(context.Background(), &cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), server.Options{})
	if err == nil {
		t.Fatal("want error")
	}
}
