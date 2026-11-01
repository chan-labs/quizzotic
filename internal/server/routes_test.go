// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/server"
)

// TestRouterPatternsAgreeWithSpec: every pattern registered on the API router
// has a spec operation (which carries its scopes) and every non-root spec
// operation is registered. The guard is deny-by-default, so a mismatch would
// otherwise surface only as a 404 at runtime.
func TestRouterPatternsAgreeWithSpec(t *testing.T) {
	rt, guard := server.NewAPIRouter(auth.Chain(nil))
	registered, spec := rt.Patterns(), guard.Patterns()
	for _, p := range registered {
		if !slices.Contains(spec, p) {
			t.Errorf("registered pattern %q has no spec operation", p)
		}
	}
	for _, p := range spec {
		if !slices.Contains(registered, p) {
			t.Errorf("spec operation %q has no registered route", p)
		}
	}
}

// TestAPIRouterServesPublicOperation: scope-free operations need no credentials.
func TestAPIRouterServesPublicOperation(t *testing.T) {
	rt, _ := server.NewAPIRouter(auth.Chain(nil))
	mux := http.NewServeMux()
	rt.Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/openapi.yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
