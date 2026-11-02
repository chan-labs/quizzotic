// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
)

// TestDiscoveryRequiresHTTPSEndpoints checks that an http endpoint on a
// non-loopback host is rejected, while loopback http (httptest) is accepted.
func TestDiscoveryRequiresHTTPSEndpoints(t *testing.T) {
	endpoints := []string{"authorization_endpoint", "token_endpoint", "jwks_uri",
		"device_authorization_endpoint", "end_session_endpoint"}
	for _, bad := range endpoints {
		t.Run(bad, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				doc := map[string]string{
					"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a",
					"token_endpoint": srv.URL + "/t", "jwks_uri": srv.URL + "/j",
				}
				doc[bad] = "http://idp.example.com/x"
				_ = json.NewEncoder(w).Encode(doc)
			}))
			defer srv.Close()
			if _, err := auth.NewProvider(srv.URL).Discovery(context.Background()); err == nil {
				t.Fatalf("http %s on a non-loopback host must be rejected", bad)
			}
		})
	}
}

func TestDiscoveryAllowsLoopbackHTTP(t *testing.T) {
	f := newFakeIdP(t, rsaJWK(t, "a"))
	if _, err := auth.NewProvider(f.srv.URL).Discovery(context.Background()); err != nil {
		t.Fatal(err)
	}
}
