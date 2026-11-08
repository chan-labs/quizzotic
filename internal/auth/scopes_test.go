// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/openapi"
)

// fakeUsers maps (issuer, subject) to a stable user, recording the subjects asked for.
type fakeUsers struct {
	ids  map[string]domain.ID
	seen []string
}

func (f *fakeUsers) Provision(_ context.Context, c ProvisionClaims) (User, error) {
	f.seen = append(f.seen, c.Subject)
	if f.ids == nil {
		f.ids = map[string]domain.ID{}
	}
	id, ok := f.ids[c.Subject]
	if !ok {
		id = domain.ID{byte(len(f.ids) + 1)}
		f.ids[c.Subject] = id
	}
	return User{ID: id}, nil
}

func testClients(t *testing.T) *Clients {
	t.Helper()
	cs, err := NewClients([]config.Client{
		{ID: "web", Kind: "human"},
		{ID: "agent-local", Kind: "agent", AllowedScopes: []string{"problems:read", "problems:write", "tags:apply"}},
		{ID: "agent-bot", Kind: "agent", ActsFor: "human-sub", AllowedScopes: []string{"problems:read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func claimsFor(client, sub string, extra map[string]any) *Claims {
	raw := map[string]any{"client_id": client}
	for k, v := range extra {
		raw[k] = v
	}
	return &Claims{Issuer: jwtIss, Subject: sub, Raw: raw}
}

func TestResolveUnknownClientRejected(t *testing.T) {
	r := NewResolver(testClients(t), &fakeUsers{})
	for _, c := range []*Claims{claimsFor("stranger", "u1", nil), {Issuer: jwtIss, Subject: "u1", Raw: map[string]any{}}} {
		if _, err := r.Resolve(context.Background(), c); !errors.Is(err, ErrUnknownClient) {
			t.Errorf("err = %v, want ErrUnknownClient", err)
		}
	}
}

func TestResolveHumanHasAllScopes(t *testing.T) {
	r := NewResolver(testClients(t), &fakeUsers{})
	p, err := r.Resolve(context.Background(), claimsFor("web", "u1", map[string]any{"scope": "openid"}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindHuman || p.ClientID != "web" || !p.Has("admin") || !p.Has("attempts:write") {
		t.Errorf("principal = %+v", p)
	}
}

func TestResolveAgentScopeNarrowing(t *testing.T) {
	r := NewResolver(testClients(t), &fakeUsers{})
	cases := []struct {
		name  string
		extra map[string]any
		want  []string
		deny  []string
	}{
		{"no scope claim: allowed_scopes", nil,
			[]string{"problems:read", "problems:write", "tags:apply"}, []string{"admin", "attempts:write", "quizzes:read"}},
		{"scope claim narrows", map[string]any{"scope": "openid problems:read"},
			[]string{"problems:read"}, []string{"problems:write", "tags:apply"}},
		{"scope claim cannot widen", map[string]any{"scope": "problems:read admin attempts:write quizzes:read"},
			[]string{"problems:read"}, []string{"admin", "attempts:write", "quizzes:read"}},
		{"scp array", map[string]any{"scp": []any{"tags:apply"}},
			[]string{"tags:apply"}, []string{"problems:read"}},
		{"empty scope claim grants nothing", map[string]any{"scope": ""},
			nil, []string{"problems:read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := r.Resolve(context.Background(), claimsFor("agent-local", "u1", tc.extra))
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != KindAgent {
				t.Fatalf("kind = %v", p.Kind)
			}
			for _, s := range tc.want {
				if !p.Has(s) {
					t.Errorf("missing %s", s)
				}
			}
			for _, s := range tc.deny {
				if p.Has(s) {
					t.Errorf("unexpected %s", s)
				}
			}
		})
	}
}

func TestResolveActsFor(t *testing.T) {
	users := &fakeUsers{}
	r := NewResolver(testClients(t), users)
	// Client-credentials: sub is the client itself; the user comes from acts_for.
	bot, err := r.Resolve(context.Background(), claimsFor("agent-bot", "agent-bot", nil))
	if err != nil {
		t.Fatal(err)
	}
	human, err := r.Resolve(context.Background(), claimsFor("web", "human-sub", nil))
	if err != nil {
		t.Fatal(err)
	}
	if bot.UserID != human.UserID {
		t.Errorf("acts_for user %v != human user %v", bot.UserID, human.UserID)
	}
	if users.seen[0] != "human-sub" {
		t.Errorf("provisioned subject %q, want acts_for", users.seen[0])
	}
	// A delegated agent without acts_for resolves to the sub in its token.
	del, err := r.Resolve(context.Background(), claimsFor("agent-local", "human-sub", nil))
	if err != nil {
		t.Fatal(err)
	}
	if del.UserID != human.UserID {
		t.Error("delegated agent should resolve to the human in sub")
	}
}

func TestNewClientsRefusesForbiddenAgentScopes(t *testing.T) {
	for _, s := range []string{"attempts:write", "sessions:write", "admin"} {
		_, err := NewClients([]config.Client{{ID: "a", Kind: "agent", AllowedScopes: []string{s}}})
		if err == nil {
			t.Errorf("%s accepted on agent", s)
		}
	}
}

type fakeAuth struct {
	p   Principal
	err error
}

func (f fakeAuth) Authenticate(http.ResponseWriter, *http.Request) (Principal, error) {
	return f.p, f.err
}

const guardSpec = `
openapi: 3.1.0
paths:
  /open:
    get: {operationId: open, x-quizzotic-scopes: []}
  /whoami:
    get: {operationId: whoami, security: [{bearer: []}], x-quizzotic-scopes: []}
  /problems:
    get: {operationId: problems.list, x-quizzotic-scopes: [problems:read]}
    post: {operationId: problems.create, x-quizzotic-scopes: [problems:write]}
  /problems/similar:
    post: {operationId: problems.similar, x-quizzotic-scopes: [problems:read]}
  /problems/{id}:
    get: {operationId: problems.get, x-quizzotic-scopes: [problems:read, quizzes:read]}
  /attempts:
    post: {operationId: attempts.create, x-quizzotic-scopes: [attempts:write]}
  /healthz:
    servers: [{url: /}]
    get: {operationId: healthz, x-quizzotic-scopes: [admin]}
`

func TestBearerAuthenticatorEndToEnd(t *testing.T) {
	signers, keys := newSigners(t)
	s := signers["ES256"]
	hdr := map[string]any{"alg": "ES256", "kid": "ES256"}
	ba := &BearerAuthenticator{
		Verifier: newTestVerifier(keys), Audiences: []string{jwtAud},
		Resolver: NewResolver(testClients(t), &fakeUsers{}),
	}
	req := func(tok string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tok != "" {
			r.Header.Set("Authorization", tok)
		}
		return r
	}

	if _, err := ba.Authenticate(nil, req("")); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("no header: %v", err)
	}
	if _, err := ba.Authenticate(nil, req("Basic abc")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("basic: %v", err)
	}

	c := goodClaims()
	c["client_id"] = "agent-local"
	c["scope"] = "problems:read"
	p, err := ba.Authenticate(nil, req("Bearer "+s.token(t, hdr, c)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindAgent || !p.Has("problems:read") || p.Has("problems:write") {
		t.Errorf("principal = %+v", p)
	}

	c["client_id"] = "stranger"
	if _, err := ba.Authenticate(nil, req("Bearer "+s.token(t, hdr, c))); !errors.Is(err, ErrUnknownClient) {
		t.Errorf("unknown client: %v", err)
	}
}

func TestEmbeddedSpecOperationsParse(t *testing.T) {
	ops, err := openapi.SpecOperations()
	if err != nil || len(ops) == 0 {
		t.Fatalf("ops=%d err=%v", len(ops), err)
	}
}

func TestResolveClassifiesByAudienceWithoutClientClaim(t *testing.T) {
	r := NewResolver(testClients(t), &fakeUsers{})
	c := &Claims{Issuer: jwtIss, Subject: "u1", Audience: []string{"agent-local"}, Raw: map[string]any{}}
	p, err := r.Resolve(context.Background(), c)
	if err != nil || p.Kind != KindAgent || p.ClientID != "agent-local" {
		t.Fatalf("p = %+v, err = %v", p, err)
	}
	// Two declared clients in aud is ambiguous; an undeclared aud is unknown.
	for _, aud := range [][]string{{"web", "agent-local"}, {"stranger"}} {
		c := &Claims{Issuer: jwtIss, Subject: "u1", Audience: aud, Raw: map[string]any{}}
		if _, err := r.Resolve(context.Background(), c); !errors.Is(err, ErrUnknownClient) {
			t.Errorf("aud %v: err = %v, want ErrUnknownClient", aud, err)
		}
	}
}
