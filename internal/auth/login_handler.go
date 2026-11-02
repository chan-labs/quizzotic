// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/chan-labs/quizzotic/internal/platform"
)

// SessionCookieName is the web session cookie.
const SessionCookieName = "quizzotic_session"

// LoginCookieName is the pre-login cookie that binds an authorization request
// to the browser that started it (login CSRF defence). The __Host- prefix
// makes browsers require Secure, Path=/ and no Domain.
const LoginCookieName = "__Host-quizzotic_login"

// WebRoutes are the web auth routes, served at the host root (outside
// /api/v1). The OpenAPI contract test checks them against the spec.
var WebRoutes = []string{"GET /auth/login", "GET /auth/callback", "POST /auth/logout"}

// LoginHandler serves the web auth endpoints and session middleware.
type LoginHandler struct {
	svc    *LoginService
	logger *slog.Logger
}

// NewLoginHandler returns a handler over svc.
func NewLoginHandler(svc *LoginService, logger *slog.Logger) *LoginHandler {
	return &LoginHandler{svc: svc, logger: logger}
}

// Register mounts WebRoutes on mux.
func (h *LoginHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", h.Login)
	mux.HandleFunc("GET /auth/callback", h.Callback)
	mux.HandleFunc("POST /auth/logout", h.Logout)
}

// Login starts the authorization code flow.
func (h *LoginHandler) Login(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	loc, err := h.svc.BeginLogin(r.Context(), r.URL.Query().Get("return_to"))
	if err != nil {
		h.fail(w, r, "begin login", err, platform.NewProblem(platform.TypeInternal, http.StatusBadGateway, "identity provider unavailable"))
		return
	}
	// The cookie carries the (single-use, unguessable) state sent to the
	// issuer; the callback only accepts a state its own browser holds.
	if u, perr := url.Parse(loc); perr == nil && u.Query().Get("state") != "" {
		h.setLoginCookie(w, u.Query().Get("state"))
	} else {
		h.fail(w, r, "begin login", errors.New("authorization URL has no state"), nil)
		return
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

// Callback completes the flow and sets the session cookie.
func (h *LoginHandler) Callback(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	q := r.URL.Query()
	// The pre-login cookie is single-use: clear it however the callback ends.
	bound := h.loginCookieMatches(r, q.Get("state"))
	h.clearLoginCookie(w)
	if e := q.Get("error"); e != "" {
		// Burn the state so a rejected attempt cannot be replayed.
		h.svc.AbandonLogin(r.Context(), q.Get("state"))
		platform.WriteError(w, r, platform.Unauthorized("the identity provider rejected the login"))
		return
	}
	if !bound {
		// The state was issued to another browser (or the cookie expired).
		// Do not consume it: only its rightful browser may.
		platform.WriteError(w, r, platform.BadRequest("login was not started in this browser; start again at /auth/login"))
		return
	}
	res, err := h.svc.CompleteLogin(r.Context(), q.Get("state"), q.Get("code"))
	switch {
	case errors.Is(err, ErrInvalidState):
		platform.WriteError(w, r, platform.BadRequest("login state is invalid or expired; start again at /auth/login"))
		return
	case errors.Is(err, ErrInvalidToken):
		h.fail(w, r, "verify id token", err, platform.Unauthorized("the identity provider's token was not accepted"))
		return
	case errors.Is(err, ErrExchange):
		h.fail(w, r, "code exchange", err, platform.NewProblem(platform.TypeInternal, http.StatusBadGateway, "code exchange failed"))
		return
	case err != nil:
		h.fail(w, r, "complete login", err, nil)
		return
	}
	h.setCookie(w, res.Token, res.ExpiresAt)
	http.Redirect(w, r, res.ReturnTo, http.StatusSeeOther)
}

// Logout deletes the session after a CSRF check and clears the cookie.
func (h *LoginHandler) Logout(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	ws, _, err := h.authenticate(r)
	switch {
	case errors.Is(err, ErrNoSession):
		h.clearCookie(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	case err != nil:
		h.fail(w, r, "logout", err, nil)
		return
	}
	if !CheckCSRF(r, ws) {
		platform.WriteError(w, r, platform.Forbidden("missing or invalid CSRF token"))
		return
	}
	if err := h.svc.Logout(r.Context(), ws); err != nil {
		h.fail(w, r, "logout", err, nil)
		return
	}
	h.clearCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type sessionKey struct{}

// SessionFrom returns the web session RequireSession attached to ctx.
func SessionFrom(ctx context.Context) (WebSession, bool) {
	ws, ok := ctx.Value(sessionKey{}).(WebSession)
	return ws, ok
}

// WithSession returns ctx carrying ws, as RequireSession does. Pages and tests
// that sit behind a different gate use it to hand the session to SessionFrom.
func WithSession(ctx context.Context, ws WebSession) context.Context {
	return context.WithValue(ctx, sessionKey{}, ws)
}

// RequireSession rejects requests without a live session (401) and otherwise
// attaches the session to the context. It re-issues the cookie when the
// sliding expiry advanced, so the browser's copy slides with the server's.
func (h *LoginHandler) RequireSession(next http.Handler) http.Handler {
	return h.RequireSessionOr(func(w http.ResponseWriter, r *http.Request) {
		platform.WriteError(w, r, platform.Unauthorized("sign in at /auth/login"))
	})(next)
}

// RequireSessionOr is RequireSession with a caller-chosen answer for requests
// that have no live session; the website uses it to redirect to login.
func (h *LoginHandler) RequireSessionOr(anonymous http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return h.requireSession(anonymous, next) }
}

func (h *LoginHandler) requireSession(anonymous http.HandlerFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, refreshed, err := h.authenticate(r)
		if errors.Is(err, ErrNoSession) {
			anonymous(w, r)
			return
		}
		if err != nil {
			h.fail(w, r, "authenticate", err, nil)
			return
		}
		if refreshed {
			if c, err := r.Cookie(SessionCookieName); err == nil {
				h.setCookie(w, c.Value, ws.ExpiresAt)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, ws)))
	})
}

func (h *LoginHandler) authenticate(r *http.Request) (WebSession, bool, error) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return WebSession{}, false, ErrNoSession
	}
	return h.svc.Authenticate(r.Context(), c.Value)
}

func (h *LoginHandler) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(h.svc.SessionTTL().Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *LoginHandler) setLoginCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     LoginCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   int(DefaultStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *LoginHandler) clearLoginCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     LoginCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *LoginHandler) loginCookieMatches(r *http.Request, state string) bool {
	c, err := r.Cookie(LoginCookieName)
	if err != nil || state == "" || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) == 1
}

func (h *LoginHandler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *LoginHandler) fail(w http.ResponseWriter, r *http.Request, what string, err error, p *platform.Problem) {
	h.logger.ErrorContext(r.Context(), "web auth: "+what, slog.String("error", err.Error()))
	if p == nil {
		p = platform.NewProblem(platform.TypeInternal, http.StatusInternalServerError, "")
	}
	platform.WriteError(w, r, p)
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }
