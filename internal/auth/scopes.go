// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Authenticator establishes the Principal of a request. Bearer tokens are
// handled by BearerAuthenticator; the web session authenticator (cookie)
// implements the same interface.
type Authenticator interface {
	// Authenticate returns ErrNoCredentials when the request carries none,
	// and any other error when credentials are present but unacceptable.
	Authenticate(r *http.Request) (Principal, error)
}

// ErrNoCredentials means the request carried no credentials.
var ErrNoCredentials = errors.New("auth: no credentials")

// BearerAuthenticator verifies an IdP access token and resolves its principal.
type BearerAuthenticator struct {
	Verifier *Verifier
	Audience string
	Resolver *Resolver
}

// Authenticate implements Authenticator.
func (b *BearerAuthenticator) Authenticate(r *http.Request) (Principal, error) {
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
func (c Chain) Authenticate(r *http.Request) (Principal, error) {
	for _, a := range c {
		p, err := a.Authenticate(r)
		if errors.Is(err, ErrNoCredentials) {
			continue
		}
		return p, err
	}
	return Principal{}, ErrNoCredentials
}

type route struct {
	op   openapi.Operation
	segs []string
}

// ScopeGuard enforces each operation's x-quizzotic-scopes.
type ScopeGuard struct {
	auth   Authenticator
	routes []route
}

// NewScopeGuard indexes the API operations in ops (root operations such as
// health probes are not under the guarded prefix and are skipped).
func NewScopeGuard(a Authenticator, ops []openapi.Operation) *ScopeGuard {
	g := &ScopeGuard{auth: a}
	for _, op := range ops {
		if op.Root {
			continue
		}
		g.routes = append(g.routes, route{op: op, segs: split(op.Path)})
	}
	// Most specific first: more literal segments win, as in net/http.
	sort.SliceStable(g.routes, func(i, j int) bool { return literals(g.routes[i].segs) > literals(g.routes[j].segs) })
	return g
}

func split(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }

func literals(segs []string) int {
	n := 0
	for _, s := range segs {
		if !strings.HasPrefix(s, "{") {
			n++
		}
	}
	return n
}

func (g *ScopeGuard) match(method, path string) (openapi.Operation, bool) {
	segs := split(path)
next:
	for _, rt := range g.routes {
		if rt.op.Method != method || len(rt.segs) != len(segs) {
			continue
		}
		for i, s := range rt.segs {
			if !strings.HasPrefix(s, "{") && s != segs[i] {
				continue next
			}
		}
		return rt.op, true
	}
	return openapi.Operation{}, false
}

// Middleware wraps the API handler (paths relative to platform.APIPrefix are
// matched after stripping the prefix). Operations with no required scopes pass
// through unauthenticated; a request matching no operation passes through to
// the router's own 404/405. Otherwise the caller must authenticate (401) and
// hold at least one required scope (403), and the Principal is placed in the
// request context.
func (g *ScopeGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, platform.APIPrefix)
		op, ok := g.match(r.Method, path)
		if !ok || len(op.Scopes) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		p, err := g.auth.Authenticate(r)
		if err != nil && !errors.Is(err, ErrNoCredentials) && !errors.Is(err, ErrInvalidToken) && !errors.Is(err, ErrUnknownClient) {
			platform.WriteError(w, r, err) // infrastructure failure: opaque 500
			return
		}
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="quizzotic"`)
			detail := "authentication required"
			if !errors.Is(err, ErrNoCredentials) {
				detail = "invalid credentials"
			}
			platform.WriteError(w, r, platform.Unauthorized(detail))
			return
		}
		if !p.HasAny(op.Scopes) {
			platform.WriteError(w, r, platform.Forbidden("missing required scope: one of "+strings.Join(op.Scopes, ", ")))
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}
