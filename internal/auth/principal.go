// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Kind classifies a principal by its client.
type Kind string

const (
	KindHuman Kind = "human"
	KindAgent Kind = "agent"
)

// Principal is the authenticated caller of one request.
type Principal struct {
	UserID   domain.ID
	Kind     Kind
	ClientID string
	// Scopes are the effective scopes of an agent. Humans hold every scope and
	// leave this nil; always ask Has.
	Scopes map[string]bool
}

// Has reports whether the principal holds scope.
func (p Principal) Has(scope string) bool {
	if p.Kind == KindHuman {
		return true
	}
	return p.Scopes[scope]
}

// HasAny reports whether the principal holds at least one of scopes. An empty
// list means no particular scope is needed.
func (p Principal) HasAny(scopes []string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, s := range scopes {
		if p.Has(s) {
			return true
		}
	}
	return false
}

type principalKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the request's principal, if authenticated.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
