// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/web"
)

const testCSRF = "csrf-token-value"

// fakeGate admits requests carrying the "ok" cookie, mimicking RequireSessionOr.
func fakeGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("ok"); err != nil {
			web.RedirectToLogin(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), auth.WebSession{CSRFToken: testCSRF})))
	})
}

func newMux(t *testing.T) *http.ServeMux {
	t.Helper()
	w, err := web.New(web.Config{Gate: fakeGate})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Register(mux)
	return mux
}

func get(mux *http.ServeMux, path string, authed bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authed {
		req.AddCookie(&http.Cookie{Name: "ok", Value: "1"})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestLoggedInUserSeesEmptyShell(t *testing.T) {
	rec := get(newMux(t), "/", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<nav", "Today", "Nothing here yet", `/static/app.css`, `action="/auth/logout"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestEveryFormCarriesCSRFToken(t *testing.T) {
	body := get(newMux(t), "/", true).Body.String()
	forms := strings.Count(body, "<form")
	tokens := strings.Count(body, `name="csrf_token" value="`+testCSRF+`"`)
	if forms == 0 || forms != tokens {
		t.Fatalf("forms = %d, csrf fields = %d", forms, tokens)
	}
}

func TestAnonymousRedirectsToLogin(t *testing.T) {
	rec := get(newMux(t), "/", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login?return_to=%2F" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestStaticAssetsEmbeddedAndPublic(t *testing.T) {
	rec := get(newMux(t), "/static/app.css", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "prefers-color-scheme: dark") {
		t.Fatalf("status = %d, body lacks dark tokens", rec.Code)
	}
}

func TestMarkdownRendersInert(t *testing.T) {
	cases := map[string][]string{
		"hello <script>alert(1)</script> world": {"<script"},
		"<script>alert(1)</script>":             {"<script"},
		"<img src=x onerror=alert(1)>":          {"<img", "onerror="},
		"[x](javascript:alert(1))":              {"javascript:"},
	}
	for src, banned := range cases {
		out := string(web.RenderMarkdown(src))
		for _, b := range banned {
			if strings.Contains(out, b) {
				t.Errorf("RenderMarkdown(%q) = %q contains %q", src, out, b)
			}
		}
	}
	if out := string(web.RenderMarkdown("**bold**")); !strings.Contains(out, "<strong>bold</strong>") {
		t.Errorf("markdown not rendered: %q", out)
	}
}
