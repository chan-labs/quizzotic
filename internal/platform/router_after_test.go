// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type ctxMarker struct{}

// UseAfterAuth middleware runs after the authorizer (it sees what the
// authorizer put in the context), only for requests that pass it.
func TestUseAfterAuth(t *testing.T) {
	rt := NewRouter()
	rt.SetAuthorizer(func(_ string, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
		if r.Header.Get("X-Allow") == "" {
			WriteError(w, r, Unauthorized("no"))
			return nil, false
		}
		return r.WithContext(context.WithValue(r.Context(), ctxMarker{}, "authed")), true
	})
	var order []string
	for _, name := range []string{"outer", "inner"} {
		rt.UseAfterAuth(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Context().Value(ctxMarker{}) != "authed" {
					t.Errorf("%s ran before the authorizer", name)
				}
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		})
	}
	rt.HandleFunc("GET /x", func(w http.ResponseWriter, _ *http.Request) { order = append(order, "handler") })
	mux := http.NewServeMux()
	rt.Mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/x", nil))
	if rec.Code != 401 || len(order) != 0 {
		t.Fatalf("unauthorized: code %d, ran %v", rec.Code, order)
	}

	req := httptest.NewRequest("GET", "/api/v1/x", nil)
	req.Header.Set("X-Allow", "1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || len(order) != 3 || order[0] != "outer" || order[1] != "inner" || order[2] != "handler" {
		t.Fatalf("authorized: code %d, order %v", rec.Code, order)
	}
}
