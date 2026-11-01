// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Authenticator establishes the Principal of a request.
type Authenticator interface {
	// Authenticate returns ErrNoCredentials when the request carries none,
	// and any other error when credentials are present but unacceptable. It
	// may set response headers (the sliding session cookie) but never writes
	// the body or status.
	Authenticate(w http.ResponseWriter, r *http.Request) (Principal, error)
}

// ErrNoCredentials means the request carried no credentials.
var ErrNoCredentials = errors.New("auth: no credentials")

// ErrCSRF means a cookie-authenticated, state-changing request lacked a valid
// CSRF token.
var ErrCSRF = errors.New("auth: csrf token missing or invalid")

// BearerAuthenticator verifies an IdP access token and resolves its principal.
type BearerAuthenticator struct {
	Verifier *Verifier
	Audience string
	Resolver *Resolver
}

// Authenticate implements Authenticator.
func (b *BearerAuthenticator) Authenticate(_ http.ResponseWriter, r *http.Request) (Principal, error) {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if h == "" {
		return Principal{}, ErrNoCredentials
	}
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tok) == "" {
		return Principal{}, invalid("malformed Authorization header")
	}
	claims, err := b.Verifier.Verify(r.Context(), strings.TrimSpace(tok), VerifyOptions{Audience: b.Audience})
	if err != nil {
		return Principal{}, err
	}
	return b.Resolver.Resolve(r.Context(), claims)
}

// Chain tries each authenticator in order and uses the first that finds
// credentials.
type Chain []Authenticator

// Authenticate implements Authenticator.
func (c Chain) Authenticate(w http.ResponseWriter, r *http.Request) (Principal, error) {
	for _, a := range c {
		p, err := a.Authenticate(w, r)
		if errors.Is(err, ErrNoCredentials) {
			continue
		}
		return p, err
	}
	return Principal{}, ErrNoCredentials
}

// SessionAuthenticator authenticates the web session cookie. Sessions belong
// to humans, so the principal holds every scope. State-changing methods must
// also carry the session's CSRF token. A session whose expiry slid is
// re-issued on the response, as RequireSession does.
type SessionAuthenticator struct{ Login *LoginHandler }

// Authenticate implements Authenticator.
func (s SessionAuthenticator) Authenticate(w http.ResponseWriter, r *http.Request) (Principal, error) {
	ws, refreshed, err := s.Login.authenticate(r)
	if errors.Is(err, ErrNoSession) {
		if _, cerr := r.Cookie(SessionCookieName); cerr != nil {
			return Principal{}, ErrNoCredentials
		}
		return Principal{}, invalid("session expired or unknown")
	}
	if err != nil {
		return Principal{}, err
	}
	if refreshed {
		if c, cerr := r.Cookie(SessionCookieName); cerr == nil {
			s.Login.setCookie(w, c.Value, ws.ExpiresAt)
		}
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		if !CheckCSRF(r, ws) {
			return Principal{}, ErrCSRF
		}
	}
	return Principal{UserID: ws.UserID, Kind: KindHuman, ClientID: "web-session"}, nil
}

// ScopeGuard enforces each operation's x-quizzotic-scopes. It does no route
// matching of its own: the router reports the mux pattern it matched, and the
// guard looks that exact pattern up among the spec's operations. A pattern
// with no spec operation is denied (fail closed).
type ScopeGuard struct {
	auth   Authenticator
	ops    map[string]guardRule // "METHOD /path" -> rule
	logger *slog.Logger
}

type guardRule struct {
	scopes []string
	// authn: authentication is required even when scopes is empty.
	authn bool
}

// GuardOption tunes a ScopeGuard.
type GuardOption func(*ScopeGuard)

// WithGuardLogger sets where the guard logs the cause of an opaque 500.
func WithGuardLogger(l *slog.Logger) GuardOption { return func(g *ScopeGuard) { g.logger = l } }

// NewScopeGuard indexes the API operations in ops (root operations such as
// health probes are served outside the router and are skipped).
func NewScopeGuard(a Authenticator, ops []openapi.Operation, opts ...GuardOption) *ScopeGuard {
	g := &ScopeGuard{auth: a, ops: map[string]guardRule{}, logger: slog.Default()}
	for _, o := range opts {
		o(g)
	}
	for _, op := range ops {
		if !op.Root {
			g.ops[op.Method+" "+op.Path] = guardRule{scopes: op.Scopes, authn: op.Authenticated}
		}
	}
	return g
}

// Patterns returns the mux patterns the guard knows, sorted.
func (g *ScopeGuard) Patterns() []string {
	out := make([]string, 0, len(g.ops))
	for p := range g.ops {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Authorize implements platform.Authorizer. HEAD requests arrive here with
// the GET pattern, because that is what the mux matched. An operation with no
// required scopes and no security scheme needs no authentication; one that
// lists a scheme but no scopes needs any authenticated principal. Otherwise the caller must
// authenticate (401) and hold at least one required scope (403), and the
// Principal is placed in the request context. A pattern with no spec
// operation is a 404. Requests the router could not match (unknown paths,
// wrong methods, OPTIONS) never get here: the router rejects them with 404 or
// 405 without running any handler.
func (g *ScopeGuard) Authorize(pattern string, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	rule, ok := g.ops[pattern]
	if !ok {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return nil, false
	}
	scopes := rule.scopes
	if len(scopes) == 0 && !rule.authn {
		return r, true
	}
	p, err := g.auth.Authenticate(w, r)
	switch {
	case err == nil:
	case errors.Is(err, ErrCSRF):
		platform.WriteError(w, r, platform.Forbidden("csrf token missing or invalid"))
		return nil, false
	case errors.Is(err, ErrNoCredentials), errors.Is(err, ErrInvalidToken), errors.Is(err, ErrUnknownClient):
		w.Header().Set("WWW-Authenticate", `Bearer realm="quizzotic"`)
		detail := "authentication required"
		if !errors.Is(err, ErrNoCredentials) {
			detail = "invalid credentials"
		}
		platform.WriteError(w, r, platform.Unauthorized(detail))
		return nil, false
	default:
		// Infrastructure failure: log the cause, answer an opaque 500.
		g.logger.ErrorContext(r.Context(), "api auth failed", slog.String("error", err.Error()))
		platform.WriteError(w, r, platform.NewProblem(platform.TypeInternal, http.StatusInternalServerError, ""))
		return nil, false
	}
	if !p.HasAny(scopes) {
		platform.WriteError(w, r, platform.Forbidden("missing required scope: one of "+strings.Join(scopes, ", ")))
		return nil, false
	}
	return r.WithContext(WithPrincipal(r.Context(), p)), true
}
