// SPDX-License-Identifier: Apache-2.0

package me_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/me"
)

type fakeUsers struct{ err error }

func (f fakeUsers) GetUser(context.Context, domain.ID) (auth.User, error) {
	return auth.User{}, f.err
}

// A valid principal whose user row is gone must get an error response, never
// a 200 and never another user's data.
func TestMeUserNotFound(t *testing.T) {
	svc := me.NewService(fakeUsers{err: auth.ErrNotFound}, "https://idp.example.com", "https://q.example.com", []string{"admin"})
	h := me.NewHandler(svc)

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Kind: auth.KindHuman, ClientID: "c"}))
	rec := httptest.NewRecorder()
	h.Me(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200 (%s)", rec.Body)
	}
	if rec.Code < 400 {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"user"`) {
		t.Errorf("error body leaks user data: %s", rec.Body)
	}
}

func TestMetadataUsesConfiguredPublicURLOnly(t *testing.T) {
	svc := me.NewService(fakeUsers{}, "https://idp.example.com", "https://q.example.com/", []string{"b", "a"})
	req := httptest.NewRequest("GET", "http://evil.example.net/.well-known/oauth-protected-resource", nil)
	req.Host = "evil.example.net"
	rec := httptest.NewRecorder()
	me.NewHandler(svc).Metadata(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"resource":"https://q.example.com"`) || strings.Contains(body, "evil") {
		t.Errorf("body = %s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", got)
	}
}
