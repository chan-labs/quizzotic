// SPDX-License-Identifier: Apache-2.0

package me_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/me"
	"github.com/chan-labs/quizzotic/internal/platform"
)

type fakeUsers struct{ err error }

func (f fakeUsers) GetUser(context.Context, domain.ID) (auth.User, error) {
	return auth.User{}, f.err
}

// A valid principal whose user row is gone must get an error response, never
// a 200 and never another user's data.
func TestMeUserNotFound(t *testing.T) {
	svc := me.NewService(fakeUsers{err: auth.ErrNotFound}, "https://idp.example.com", "https://q.example.com", []string{"admin"})
	var logs bytes.Buffer
	h := me.NewHandler(svc, slog.New(slog.NewTextHandler(&logs, nil)))

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Kind: auth.KindHuman, ClientID: "c"}))
	rec := httptest.NewRecorder()
	h.Me(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body)
	}
	var p platform.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != platform.TypeInternal || p.Status != 500 {
		t.Errorf("problem = %+v", p)
	}
	if strings.Contains(rec.Body.String(), `"user"`) || strings.Contains(rec.Body.String(), "not found") {
		t.Errorf("error body leaks detail: %s", rec.Body)
	}
	if !strings.Contains(logs.String(), "user lookup failed") || !strings.Contains(logs.String(), "not found") {
		t.Errorf("cause not logged: %s", logs.String())
	}
}

func TestMetadataUsesConfiguredPublicURLOnly(t *testing.T) {
	svc := me.NewService(fakeUsers{}, "https://idp.example.com", "https://q.example.com/", []string{"b", "a"})
	req := httptest.NewRequest(http.MethodGet, "http://evil.example.net/.well-known/oauth-protected-resource", nil)
	req.Host = "evil.example.net"
	rec := httptest.NewRecorder()
	me.NewHandler(svc, nil).Metadata(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"resource":"https://q.example.com"`) || strings.Contains(body, "evil") {
		t.Errorf("body = %s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", got)
	}
}
