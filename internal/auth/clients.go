// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chan-labs/quizzotic/internal/config"
)

// ErrUnknownClient is returned for a token whose client is not declared in
// auth.clients (or carries no client claim at all).
var ErrUnknownClient = errors.New("auth: unknown client")

// ClientInfo is a declared client.
type ClientInfo struct {
	ID            string
	Kind          Kind
	ActsFor       string // subject of the human a client-credentials client acts for
	AllowedScopes map[string]bool
}

// Clients indexes the declared clients of one issuer by ID.
type Clients struct {
	byID map[string]ClientInfo
}

// NewClients builds the index from config. It re-checks what config
// validation guarantees, so a hand-built list cannot smuggle in a forbidden
// agent scope.
func NewClients(cs []config.Client) (*Clients, error) {
	out := &Clients{byID: map[string]ClientInfo{}}
	for _, c := range cs {
		if c.ID == "" {
			return nil, errors.New("auth: client without id")
		}
		if _, dup := out.byID[c.ID]; dup {
			return nil, fmt.Errorf("auth: duplicate client %q", c.ID)
		}
		info := ClientInfo{ID: c.ID, ActsFor: c.ActsFor}
		switch c.Kind {
		case "human":
			info.Kind = KindHuman
		case "agent":
			info.Kind = KindAgent
			info.AllowedScopes = map[string]bool{}
			for _, s := range c.AllowedScopes {
				if config.ForbiddenAgentScopes[s] {
					return nil, fmt.Errorf("auth: client %q: scope %q is never allowed on an agent", c.ID, s)
				}
				info.AllowedScopes[s] = true
			}
		default:
			return nil, fmt.Errorf("auth: client %q: bad kind %q", c.ID, c.Kind)
		}
		out.byID[c.ID] = info
	}
	return out, nil
}

// Lookup returns the declared client, or ErrUnknownClient.
func (c *Clients) Lookup(id string) (ClientInfo, error) {
	info, ok := c.byID[id]
	if !ok || id == "" {
		return ClientInfo{}, ErrUnknownClient
	}
	return info, nil
}

// fromAudience returns the declared client named by a token's aud, for tokens
// with no client_id or azp claim (Pocket ID sets aud to the client ID). It
// answers only when exactly one aud entry is a declared client, so an
// ambiguous token stays unclassified.
func (c *Clients) fromAudience(aud []string) string {
	found := ""
	for _, a := range aud {
		if _, ok := c.byID[a]; ok && a != "" {
			if found != "" && found != a {
				return ""
			}
			found = a
		}
	}
	return found
}

// tokenClientID extracts the OAuth client ID from verified claims
// (client_id per RFC 9068, else azp).
func tokenClientID(raw map[string]any) string {
	for _, k := range []string{"client_id", "azp"} {
		if s, ok := raw[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// tokenScopes returns the token's scope claim (space-delimited "scope", or a
// "scp" array) and whether one was present.
func tokenScopes(raw map[string]any) (map[string]bool, bool) {
	if s, ok := raw["scope"].(string); ok {
		set := map[string]bool{}
		for _, f := range strings.Fields(s) {
			set[f] = true
		}
		return set, true
	}
	if a, ok := raw["scp"].([]any); ok {
		set := map[string]bool{}
		for _, e := range a {
			if s, ok := e.(string); ok {
				set[s] = true
			}
		}
		return set, true
	}
	return nil, false
}

// Provisioner resolves an (issuer, subject) to a user. *Service implements it.
type Provisioner interface {
	Provision(ctx context.Context, c ProvisionClaims) (User, error)
}

// Resolver turns verified token claims into a Principal.
type Resolver struct {
	clients *Clients
	users   Provisioner
}

// NewResolver builds a Resolver.
func NewResolver(clients *Clients, users Provisioner) *Resolver {
	return &Resolver{clients: clients, users: users}
}

// Resolve classifies the token by its client and finds its user. Unknown
// clients are rejected. A human token resolves to its sub and holds every
// scope. An agent token resolves to its sub, or to the client's acts_for when
// set (client-credentials), and holds the client's allowed_scopes narrowed by
// the token's scope claim when present.
func (r *Resolver) Resolve(ctx context.Context, c *Claims) (Principal, error) {
	id := tokenClientID(c.Raw)
	if id == "" {
		id = r.clients.fromAudience(c.Audience)
	}
	client, err := r.clients.Lookup(id)
	if err != nil {
		return Principal{}, err
	}
	pc := ProvisionClaims{Issuer: c.Issuer, Subject: c.Subject}
	p := Principal{Kind: client.Kind, ClientID: client.ID}

	switch client.Kind {
	case KindHuman:
		pc.DisplayName, _ = c.Raw["name"].(string)
		pc.Email, _ = c.Raw["email"].(string)
	case KindAgent:
		if client.ActsFor != "" {
			pc.Subject = client.ActsFor
		}
		p.Scopes = map[string]bool{}
		tok, present := tokenScopes(c.Raw)
		for s := range client.AllowedScopes {
			if !present || tok[s] {
				p.Scopes[s] = true
			}
		}
	}
	if pc.Subject == "" {
		return Principal{}, invalid("missing sub")
	}
	u, err := r.users.Provision(ctx, pc)
	if err != nil {
		return Principal{}, err
	}
	p.UserID = u.ID
	return p, nil
}
