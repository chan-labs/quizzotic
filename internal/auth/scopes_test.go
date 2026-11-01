// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
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

func (f fakeAuth) Authenticate(*http.Request) (Principal, error) { return f.p, f.err }

const guardSpec = `
openapi: 3.1.0
paths:
  /open:
    get: {operationId: open, x-quizzotic-scopes: []}
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

func guard(t *testing.T, a Authenticator) http.Handler {
	t.Helper()
	ops, err := openapi.Operations([]byte(guardSpec))
	if err != nil {
		t.Fatal(err)
	}
	return NewScopeGuard(a, ops).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFrom(r.Context()); ok {
			w.Header().Set("X-Principal", "yes")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1"+path, nil))
	return rec
}

func TestScopeGuard(t *testing.T) {
	agent := Principal{Kind: KindAgent, Scopes: map[string]bool{"problems:read": true}}
	human := Principal{Kind: KindHuman}
	other := Principal{Kind: KindAgent, Scopes: map[string]bool{"tags:read": true}}
	cases := []struct {
		name   string
		a      Authenticator
		method string
		path   string
		want   int
	}{
		{"no scopes needed, no credentials", fakeAuth{err: ErrNoCredentials}, "GET", "/open", 204},
		{"no credentials", fakeAuth{err: ErrNoCredentials}, "GET", "/problems", 401},
		{"bad token", fakeAuth{err: invalid("x")}, "GET", "/problems", 401},
		{"unknown client", fakeAuth{err: ErrUnknownClient}, "GET", "/problems", 401},
		{"infra failure", fakeAuth{err: errors.New("db down")}, "GET", "/problems", 500},
		{"agent has scope", fakeAuth{p: agent}, "GET", "/problems", 204},
		{"agent lacks scope", fakeAuth{p: agent}, "POST", "/problems", 403},
		{"literal beats wildcard", fakeAuth{p: agent}, "POST", "/problems/similar", 204},
		{"any one of scopes", fakeAuth{p: agent}, "GET", "/problems/abc", 204},
		{"agent never attempts:write", fakeAuth{p: agent}, "POST", "/attempts", 403},
		{"human holds all", fakeAuth{p: human}, "POST", "/attempts", 204},
		{"HEAD unauthenticated", fakeAuth{err: ErrNoCredentials}, "HEAD", "/problems", 401},
		{"HEAD missing scope", fakeAuth{p: other}, "HEAD", "/problems", 403},
		{"HEAD with scope", fakeAuth{p: agent}, "HEAD", "/problems", 204},
		{"OPTIONS unauthenticated", fakeAuth{err: ErrNoCredentials}, "OPTIONS", "/problems", 401},
		{"OPTIONS missing scope", fakeAuth{p: other}, "OPTIONS", "/problems", 403},
		{"OPTIONS with scope", fakeAuth{p: agent}, "OPTIONS", "/problems", 204},
		{"wrong method unauthenticated", fakeAuth{err: ErrNoCredentials}, "DELETE", "/problems/abc", 401},
		{"wrong method missing scope", fakeAuth{p: other}, "DELETE", "/problems/abc", 403},
		{"wrong method with scope", fakeAuth{p: agent}, "DELETE", "/problems/abc", 204},
		{"scope-free op on same path: wrong method passes to router (405)", fakeAuth{err: ErrNoCredentials}, "POST", "/open", 204},
		{"encoded slash in id GET", fakeAuth{err: ErrNoCredentials}, "GET", "/problems/a%2Fb", 400},
		{"encoded slash in id HEAD", fakeAuth{err: ErrNoCredentials}, "HEAD", "/problems/a%2fb", 400},
		{"encoded slash in id POST", fakeAuth{err: ErrNoCredentials}, "POST", "/problems/a%2Fb", 400},
		{"encoded slash as separator", fakeAuth{err: ErrNoCredentials}, "GET", "/problems%2Fabc", 400},
		{"encoded backslash", fakeAuth{err: ErrNoCredentials}, "GET", "/problems/a%5Cb", 400},
		{"escaped literal still guarded", fakeAuth{err: ErrNoCredentials}, "GET", "/%70roblems", 401},
		{"escaped id is one segment", fakeAuth{p: agent}, "GET", "/problems/a%20b", 204},
		{"unmatched passes to router", fakeAuth{err: ErrNoCredentials}, "GET", "/nope", 204},
		{"root ops are not guarded here", fakeAuth{err: ErrNoCredentials}, "GET", "/healthz", 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(guard(t, tc.a), tc.method, tc.path)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 401 && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
			if tc.want == 204 && tc.path != "/open" && !strings.HasPrefix(tc.name, "unmatched") && !strings.HasPrefix(tc.name, "root") &&
				rec.Header().Get("X-Principal") != "yes" {
				t.Error("principal not in context")
			}
		})
	}
}

func TestBearerAuthenticatorEndToEnd(t *testing.T) {
	signers, keys := newSigners(t)
	s := signers["ES256"]
	hdr := map[string]any{"alg": "ES256", "kid": "ES256"}
	ba := &BearerAuthenticator{
		Verifier: newTestVerifier(keys), Audience: jwtAud,
		Resolver: NewResolver(testClients(t), &fakeUsers{}),
	}
	req := func(tok string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if tok != "" {
			r.Header.Set("Authorization", tok)
		}
		return r
	}

	if _, err := ba.Authenticate(req("")); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("no header: %v", err)
	}
	if _, err := ba.Authenticate(req("Basic abc")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("basic: %v", err)
	}

	c := goodClaims()
	c["client_id"] = "agent-local"
	c["scope"] = "problems:read"
	p, err := ba.Authenticate(req("Bearer " + s.token(t, hdr, c)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindAgent || !p.Has("problems:read") || p.Has("problems:write") {
		t.Errorf("principal = %+v", p)
	}

	c["client_id"] = "stranger"
	if _, err := ba.Authenticate(req("Bearer " + s.token(t, hdr, c))); !errors.Is(err, ErrUnknownClient) {
		t.Errorf("unknown client: %v", err)
	}
}

// TestScopeGuardInFrontOfRouter puts the guard before the real router and mux
// patterns: no request without credentials may reach a handler, whatever its
// path encoding or method.
func TestScopeGuardInFrontOfRouter(t *testing.T) {
	ops, err := openapi.Operations([]byte(guardSpec))
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ })
	rt := platform.NewRouter()
	rt.Handle("GET /open", h)
	rt.Handle("GET /problems", h)
	rt.Handle("POST /problems", h)
	rt.Handle("POST /problems/similar", h)
	rt.Handle("GET /problems/{id}", h)
	rt.Handle("POST /attempts", h)
	mux := http.NewServeMux()
	mux.Handle(platform.APIPrefix+"/", NewScopeGuard(fakeAuth{err: ErrNoCredentials}, ops).Middleware(rt))

	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"} {
		for _, path := range []string{
			"/problems", "/problems/abc", "/problems/a%2Fb", "/problems/a%2fb", "/problems%2Fabc",
			"/problems/similar", "/problems/%73imilar", "/problems/", "/problems//abc", "/attempts", "/./problems", "/x/../problems",
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1"+path, nil))
			if reached != 0 {
				t.Fatalf("%s %s reached a handler without credentials (status %d)", method, path, rec.Code)
			}
		}
	}
	// Sanity: the scope-free operation is served.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/open", nil))
	if reached != 1 || rec.Code != 200 {
		t.Errorf("GET /open: reached=%d status=%d", reached, rec.Code)
	}
}

func TestEmbeddedSpecOperationsParse(t *testing.T) {
	ops, err := openapi.SpecOperations()
	if err != nil || len(ops) == 0 {
		t.Fatalf("ops=%d err=%v", len(ops), err)
	}
}
