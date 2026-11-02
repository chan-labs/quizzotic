// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
)

// TestSessionAuthenticatorCSRF covers the CSRF branch of the cookie
// authenticator: state-changing methods need the session's token, safe
// methods are exempt.
func TestSessionAuthenticatorCSRF(t *testing.T) {
	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)
	cookie := sessionCookie(a.login(""))
	if cookie == nil {
		t.Fatal("no session cookie after login")
	}
	// The session's CSRF token, as a page would embed it.
	me := a.do("GET", "/me", cookie.Value, nil)
	_, token, ok := strings.Cut(a.body(me), " ")
	if !ok || token == "" {
		t.Fatal("no csrf token for the session")
	}

	h := auth.NewLoginHandler(a.svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	authn := auth.SessionAuthenticator{Login: h}

	newReq := func(method, header, field string) *http.Request {
		var body io.Reader
		if field != "" {
			body = strings.NewReader(url.Values{auth.CSRFFormField: {field}}.Encode())
		}
		r := httptest.NewRequest(method, "/api/v1/x", body)
		if field != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if header != "" {
			r.Header.Set(auth.CSRFHeader, header)
		}
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie.Value})
		return r
	}

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(m+" missing token", func(t *testing.T) {
			_, err := authn.Authenticate(httptest.NewRecorder(), newReq(m, "", ""))
			if !errors.Is(err, auth.ErrCSRF) {
				t.Fatalf("err = %v, want ErrCSRF", err)
			}
		})
		t.Run(m+" wrong header token", func(t *testing.T) {
			_, err := authn.Authenticate(httptest.NewRecorder(), newReq(m, "wrong", ""))
			if !errors.Is(err, auth.ErrCSRF) {
				t.Fatalf("err = %v, want ErrCSRF", err)
			}
		})
		t.Run(m+" wrong form token", func(t *testing.T) {
			_, err := authn.Authenticate(httptest.NewRecorder(), newReq(m, "", "wrong"))
			if !errors.Is(err, auth.ErrCSRF) {
				t.Fatalf("err = %v, want ErrCSRF", err)
			}
		})
		t.Run(m+" correct header token", func(t *testing.T) {
			p, err := authn.Authenticate(httptest.NewRecorder(), newReq(m, token, ""))
			if err != nil || p.Kind != auth.KindHuman || p.UserID.String() == "" {
				t.Fatalf("principal %+v, err %v", p, err)
			}
		})
		if m == http.MethodDelete {
			// net/http does not parse DELETE bodies, so a form token is never
			// seen there (fails closed; HTML forms cannot send DELETE anyway).
			continue
		}
		t.Run(m+" correct form token", func(t *testing.T) {
			if _, err := authn.Authenticate(httptest.NewRecorder(), newReq(m, "", token)); err != nil {
				t.Fatalf("err = %v", err)
			}
		})
	}

	// Safe methods carry no token and are not checked.
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(m+" exempt", func(t *testing.T) {
			if _, err := authn.Authenticate(httptest.NewRecorder(), newReq(m, "", "")); err != nil {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
