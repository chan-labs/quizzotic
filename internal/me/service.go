// SPDX-License-Identifier: Apache-2.0

// Package me serves the caller's own identity (GET /me) and the public
// protected-resource metadata (RFC 9728).
package me

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
)

// Users looks up a user by ID. *auth.Service implements it.
type Users interface {
	GetUser(ctx context.Context, userID domain.ID) (auth.User, error)
}

// Service holds the rules for the identity endpoints.
type Service struct {
	users     Users
	issuer    string
	catalog   []string // every scope the spec declares, sorted
	publicURL string
}

// NewService builds a Service. catalog is the spec's scope catalog; issuer is
// the configured OIDC issuer; publicURL is the server's public base URL.
func NewService(users Users, issuer, publicURL string, catalog []string) *Service {
	c := append([]string(nil), catalog...)
	sort.Strings(c)
	return &Service{users: users, issuer: issuer, catalog: c, publicURL: strings.TrimRight(publicURL, "/")}
}

// UserView is the user part of Me.
type UserView struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	CreatedAt   time.Time `json:"created_at"`
}

// Me describes the calling principal.
type Me struct {
	User     UserView `json:"user"`
	Kind     string   `json:"kind"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

// Me returns who p is. The user is looked up by the principal's own ID, so it
// can only ever describe the caller. A human holds every scope in the
// catalog; an agent holds the intersection of its scopes and the catalog.
func (s *Service) Me(ctx context.Context, p auth.Principal) (Me, error) {
	u, err := s.users.GetUser(ctx, p.UserID)
	if err != nil {
		return Me{}, err
	}
	scopes := []string{}
	for _, sc := range s.catalog {
		if p.Has(sc) {
			scopes = append(scopes, sc)
		}
	}
	return Me{
		User: UserView{
			ID: u.ID.String(), DisplayName: u.DisplayName, Email: u.Email, CreatedAt: u.CreatedAt,
		},
		Kind:     string(p.Kind),
		ClientID: p.ClientID,
		Scopes:   scopes,
	}, nil
}

// ResourceMetadata is the RFC 9728 document.
type ResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// Metadata returns the protected-resource metadata. The resource is the
// configured public URL (required by config validation); request-derived
// values such as Host are untrusted and never used.
func (s *Service) Metadata() ResourceMetadata {
	return ResourceMetadata{
		Resource:               s.publicURL,
		AuthorizationServers:   []string{s.issuer},
		ScopesSupported:        append([]string{}, s.catalog...),
		BearerMethodsSupported: []string{"header"},
	}
}
