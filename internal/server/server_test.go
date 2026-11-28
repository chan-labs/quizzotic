// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
	"github.com/chan-labs/quizzotic/internal/testsupport/serverrun"
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

	srv := serverrun.Start(t, cfg)
	get := func(path string) int {
		t.Helper()
		resp, err := srv.Client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, p := range []string{"/healthz", "/readyz", "/api/v1/openapi.yaml"} {
		if got := get(p); got != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, got)
		}
	}
	if got := get("/api/v1/nope"); got != http.StatusNotFound {
		t.Errorf("unknown API path = %d, want 404", got)
	}

	// Cancelling Run's context stands in for SIGTERM; Stop fails the test
	// unless Run then returns nil promptly.
	srv.Stop()
}

func TestRunRejectsIncompleteConfig(t *testing.T) {
	cfg := config.Default() // no public_url, no issuer
	err := server.Run(t.Context(), &cfg, slog.New(slog.DiscardHandler), server.Options{})
	if err == nil {
		t.Fatal("want error")
	}
}
