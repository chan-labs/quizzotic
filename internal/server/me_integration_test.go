// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/server"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

type meBody struct {
	User struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		CreatedAt   string `json:"created_at"`
	} `json:"user"`
	Kind     string   `json:"kind"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

func (e *itEnv) me(t *testing.T, hdr map[string]string, cookie string) meBody {
	t.Helper()
	r := e.do(t, "GET", "/api/v1/me", hdr, cookie)
	if r.code != 200 {
		t.Fatalf("GET /me = %d (%s)", r.code, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var m meBody
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return m
}

func TestMe(t *testing.T) {
	e := newITEnv(t)
	catalog, err := openapi.SpecScopes()
	if err != nil || len(catalog) == 0 {
		t.Fatalf("spec scopes = %v, %v", catalog, err)
	}

	t.Run("web cookie", func(t *testing.T) {
		cookie := e.webLogin(t)
		uid, _ := e.userFor(t, "web-sub")
		m := e.me(t, nil, cookie)
		if m.User.ID != uid || m.User.DisplayName != "Web User" || m.User.Email != "web@example.com" || m.User.CreatedAt == "" {
			t.Errorf("user = %+v (want id %s)", m.User, uid)
		}
		if m.Kind != "human" || m.ClientID != "web-session" || !slices.Equal(m.Scopes, catalog) {
			t.Errorf("me = %+v", m)
		}
	})

	t.Run("CLI token", func(t *testing.T) {
		tok := e.idp.access("quizzotic-cli", "cli-sub", with("name", "Cli User"), with("email", "cli@example.com"))
		m := e.me(t, bearer(tok), "")
		uid, _ := e.userFor(t, "cli-sub")
		if m.User.ID != uid || m.User.DisplayName != "Cli User" || m.User.Email != "cli@example.com" {
			t.Errorf("user = %+v (want id %s)", m.User, uid)
		}
		if m.Kind != "human" || m.ClientID != "quizzotic-cli" || !slices.Equal(m.Scopes, catalog) {
			t.Errorf("me = %+v", m)
		}
	})

	t.Run("agent client token, narrowed by scope claim", func(t *testing.T) {
		tok := e.idp.access("quizzotic-agent", "cli-sub", with("scope", "problems:read attempts:write"))
		m := e.me(t, bearer(tok), "")
		uid, _ := e.userFor(t, "cli-sub")
		if m.User.ID != uid {
			t.Errorf("agent must act as the human in sub: %s != %s", m.User.ID, uid)
		}
		if m.Kind != "agent" || m.ClientID != "quizzotic-agent" || !slices.Equal(m.Scopes, []string{"problems:read"}) {
			t.Errorf("me = %+v", m)
		}
	})

	t.Run("agent client token, no scope claim", func(t *testing.T) {
		m := e.me(t, bearer(e.idp.access("quizzotic-agent", "cli-sub")), "")
		if !slices.Equal(m.Scopes, []string{"problems:read", "problems:write"}) {
			t.Errorf("scopes = %v", m.Scopes)
		}
	})

	t.Run("headless token acts for its configured human", func(t *testing.T) {
		tok := e.idp.access("quizzotic-headless", "svc-account", with("scope", "problems:read problems:write"))
		m := e.me(t, bearer(tok), "")
		uid, _ := e.userFor(t, "cli-sub")
		if m.User.ID != uid || m.Kind != "agent" || m.ClientID != "quizzotic-headless" ||
			!slices.Equal(m.Scopes, []string{"problems:read"}) {
			t.Errorf("me = %+v (want user %s)", m, uid)
		}
	})
}

func TestMeRequiresAuthentication(t *testing.T) {
	e := newITEnv(t)
	for name, hdr := range map[string]map[string]string{
		"no credentials": nil,
		"garbage bearer": bearer("garbage"),
		"unknown client": bearer(e.idp.access("stranger", "x")),
	} {
		t.Run(name, func(t *testing.T) {
			r := e.do(t, "GET", "/api/v1/me", hdr, "")
			r.wantProblem(t, 401, "unauthorized")
			r.wantChallenge(t)
		})
	}
	r := e.do(t, "GET", "/api/v1/me", nil, "forged-cookie")
	r.wantProblem(t, 401, "unauthorized")
	r.wantChallenge(t)
}

func TestProtectedResourceMetadata(t *testing.T) {
	e := newITEnv(t)
	catalog, _ := openapi.SpecScopes()

	// Public: no credentials, and junk credentials are not even looked at.
	for _, hdr := range []map[string]string{nil, bearer("garbage")} {
		r := e.do(t, "GET", "/.well-known/oauth-protected-resource", hdr, "")
		if r.code != 200 {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		if ct := r.header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		if cc := r.header.Get("Cache-Control"); cc != "public, max-age=300" {
			t.Errorf("Cache-Control = %q", cc)
		}
		var doc struct {
			Resource               string   `json:"resource"`
			AuthorizationServers   []string `json:"authorization_servers"`
			ScopesSupported        []string `json:"scopes_supported"`
			BearerMethodsSupported []string `json:"bearer_methods_supported"`
		}
		if err := json.Unmarshal(r.body, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Resource != "https://quizzotic.example.com" {
			t.Errorf("resource = %q", doc.Resource)
		}
		if !slices.Equal(doc.AuthorizationServers, []string{e.idp.srv.URL}) {
			t.Errorf("authorization_servers = %v, want [%s]", doc.AuthorizationServers, e.idp.srv.URL)
		}
		if !slices.Equal(doc.ScopesSupported, catalog) {
			t.Errorf("scopes_supported = %v, want %v", doc.ScopesSupported, catalog)
		}
		if !slices.Equal(doc.BearerMethodsSupported, []string{"header"}) {
			t.Errorf("bearer_methods_supported = %v", doc.BearerMethodsSupported)
		}
	}
	// Root path only: not under /api/v1.
	if r := e.do(t, "GET", "/api/v1/.well-known/oauth-protected-resource", nil, ""); r.code != 404 {
		t.Errorf("under /api/v1 = %d, want 404", r.code)
	}
}

type stubAuth struct{}

func (stubAuth) Authenticate(_ http.ResponseWriter, r *http.Request) (auth.Principal, error) {
	if r.Header.Get("X-Test-Auth") == "" {
		return auth.Principal{}, auth.ErrNoCredentials
	}
	return auth.Principal{Kind: auth.KindHuman, ClientID: "stub"}, nil
}

// Without WithIdentity the identity endpoints are not served: the public
// metadata is 404, and /me is still guarded (401) before it answers 404.
func TestIdentityEndpointsDisabledWithoutWithIdentity(t *testing.T) {
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := server.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)), st,
		auth.NewProvider("https://idp.example.com"), server.WithAPIAuth(stubAuth{}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	get := func(path string, hdr map[string]string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get("/.well-known/oauth-protected-resource", nil); c != 404 {
		t.Errorf("metadata = %d, want 404", c)
	}
	if c := get("/api/v1/me", nil); c != 401 {
		t.Errorf("/me without credentials = %d, want 401", c)
	}
	if c := get("/api/v1/me", map[string]string{"X-Test-Auth": "1"}); c != 404 {
		t.Errorf("/me authenticated = %d, want 404", c)
	}
}
