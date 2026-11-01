// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/server"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

const (
	itAudience  = "quizzotic"
	itWebClient = "quizzotic-web"
	itWebSecret = "web-secret"
)

var b64 = base64.RawURLEncoding

// fakeIdP is an OIDC issuer: discovery, JWKS, and a token endpoint for the web
// login's code exchange. Tests mint access tokens with sign.
type fakeIdP struct {
	t     *testing.T
	srv   *httptest.Server
	priv  ed25519.PrivateKey
	other ed25519.PrivateKey // a key the JWKS does not publish

	mu     sync.Mutex
	nonces map[string]string // authorization code -> nonce
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeIdP{t: t, priv: priv, other: other, nonces: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "OKP", "crv": "Ed25519", "kid": "k1", "alg": "EdDSA", "x": b64.EncodeToString(pub)},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		nonce, ok := f.nonces[r.PostForm.Get("code")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		id := f.sign(f.priv, map[string]any{
			"iss": f.srv.URL, "sub": "web-sub", "aud": itWebClient, "nonce": nonce,
			"exp": time.Now().Add(time.Hour).Unix(), "name": "Web User", "email": "web@example.com",
		})
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": id, "access_token": "unused", "token_type": "Bearer"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) sign(key ed25519.PrivateKey, claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "k1", "typ": "JWT"})
	pl, _ := json.Marshal(claims)
	signed := b64.EncodeToString(hdr) + "." + b64.EncodeToString(pl)
	sig, err := key.Sign(rand.Reader, []byte(signed), crypto.Hash(0))
	if err != nil {
		f.t.Fatal(err)
	}
	return signed + "." + b64.EncodeToString(sig)
}

// access mints a valid access token for client and sub; mutate adjusts claims.
func (f *fakeIdP) access(client, sub string, mutate ...func(map[string]any)) string {
	c := map[string]any{
		"iss": f.srv.URL, "aud": itAudience, "sub": sub, "client_id": client,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	for _, m := range mutate {
		m(c)
	}
	return f.sign(f.priv, c)
}

func with(k string, v any) func(map[string]any) { return func(c map[string]any) { c[k] = v } }

// allScopes are the scopes the test routes' principals are probed for.
var allScopes = []string{"problems:read", "problems:write", "attempts:write", "admin"}

type who struct {
	UserID   string   `json:"user_id"`
	Kind     string   `json:"kind"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

type itEnv struct {
	idp  *fakeIdP
	srv  *httptest.Server
	st   *sqlite.Store
	logs *bytes.Buffer
}

// newITEnv wires the production auth (server.AuthOptions) over a real SQLite
// store and a fake IdP, and mounts it behind the real router and scope guard
// with test-only scoped operations registered beside the spec's.
func newITEnv(t *testing.T) *itEnv {
	t.Helper()
	ctx := context.Background()
	idp := newFakeIdP(t)

	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Server.PublicURL = "https://quizzotic.example.com"
	cfg.Auth.Issuers = []config.Issuer{{
		Name: "test", Issuer: idp.srv.URL, Audience: itAudience,
		Scopes: []string{"openid", "profile", "email"},
		Clients: []config.Client{
			{ID: itWebClient, Kind: "human", Secret: itWebSecret},
			{ID: "quizzotic-cli", Kind: "human"},
			{ID: "quizzotic-agent", Kind: "agent", AllowedScopes: []string{"problems:read", "problems:write"}},
			{ID: "quizzotic-headless", Kind: "agent", ActsFor: "cli-sub", AllowedScopes: []string{"problems:read"}},
		},
	}}

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	hopts, err := server.AuthOptions(&cfg, logger, st, auth.NewProvider(idp.srv.URL), nil)
	if err != nil {
		t.Fatal(err)
	}

	scoped := func(method, path, id string, scopes ...string) openapi.Operation {
		return openapi.Operation{ID: id, Method: method, Path: path, Scopes: scopes}
	}
	ops := []openapi.Operation{
		scoped("GET", "/_test/open", "test.open"),
		scoped("GET", "/_test/read", "test.read", "problems:read"),
		scoped("POST", "/_test/write", "test.write", "problems:write"),
		scoped("POST", "/_test/attempt", "test.attempt", "attempts:write"),
	}
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		out := who{Scopes: []string{}}
		if ok {
			out.UserID, out.Kind, out.ClientID = p.UserID.String(), string(p.Kind), p.ClientID
			for _, s := range allScopes {
				if p.Has(s) {
					out.Scopes = append(out.Scopes, s)
				}
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	register := func(rt *platform.Router) {
		for _, o := range ops {
			rt.Handle(o.Method+" "+o.Path, echo)
		}
	}
	h := server.Handler(logger, st, auth.NewProvider(idp.srv.URL),
		append(hopts, server.WithExtraOperations(ops, register))...)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &itEnv{idp: idp, srv: srv, st: st, logs: logs}
}

type reply struct {
	code   int
	header http.Header
	body   []byte
}

func (e *itEnv) do(t *testing.T, method, path string, hdr map[string]string, cookie string) reply {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, b}
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func (r reply) who(t *testing.T) who {
	t.Helper()
	var w who
	if err := json.Unmarshal(r.body, &w); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return w
}

// wantProblem asserts an RFC 9457 body of the given status and type suffix.
func (r reply) wantProblem(t *testing.T, status int, typ string) platform.Problem {
	t.Helper()
	if r.code != status {
		t.Fatalf("status = %d, want %d (%s)", r.code, status, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != platform.ProblemContentType {
		t.Fatalf("content-type = %q", ct)
	}
	var p platform.Problem
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != "urn:quizzotic:problem:"+typ || p.Status != status {
		t.Fatalf("problem = %+v", p)
	}
	return p
}

func (r reply) wantChallenge(t *testing.T) {
	t.Helper()
	if got := r.header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}

// userFor returns the provisioned user ID for sub, or "" when none exists.
func (e *itEnv) userFor(t *testing.T, sub string) (string, auth.User) {
	t.Helper()
	id, err := e.st.Identities().GetByIssuerSubject(context.Background(), e.idp.srv.URL, sub)
	if err != nil {
		return "", auth.User{}
	}
	u, err := e.st.Users().Get(context.Background(), id.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID.String(), u
}

func TestBearerCLITokenProvisionsHuman(t *testing.T) {
	e := newITEnv(t)
	tok := e.idp.access("quizzotic-cli", "cli-sub", with("name", "Cli User"), with("email", "cli@example.com"))

	if uid, _ := e.userFor(t, "cli-sub"); uid != "" {
		t.Fatal("user exists before first login")
	}
	r := e.do(t, "GET", "/api/v1/_test/read", bearer(tok), "")
	if r.code != 200 {
		t.Fatalf("status = %d (%s)", r.code, r.body)
	}
	w := r.who(t)
	uid, u := e.userFor(t, "cli-sub")
	if uid == "" || w.UserID != uid {
		t.Fatalf("principal user %q, provisioned %q", w.UserID, uid)
	}
	if w.Kind != "human" || w.ClientID != "quizzotic-cli" || !slices.Equal(w.Scopes, allScopes) {
		t.Errorf("principal = %+v", w)
	}
	if u.DisplayName != "Cli User" || u.Email != "cli@example.com" {
		t.Errorf("user profile = %+v", u)
	}
	// A human holds every scope, including writes agents never get.
	if r := e.do(t, "POST", "/api/v1/_test/attempt", bearer(tok), ""); r.code != 200 {
		t.Errorf("human attempts:write = %d", r.code)
	}
	// The same token again resolves to the same user.
	if again := e.do(t, "GET", "/api/v1/_test/read", bearer(tok), "").who(t); again.UserID != uid {
		t.Errorf("second request user %q != %q", again.UserID, uid)
	}
}

func TestBearerAgentToken(t *testing.T) {
	e := newITEnv(t)
	human := e.do(t, "GET", "/api/v1/_test/read", bearer(e.idp.access("quizzotic-cli", "cli-sub")), "").who(t)

	t.Run("narrowed by scope claim, resolves to the human in sub", func(t *testing.T) {
		tok := e.idp.access("quizzotic-agent", "cli-sub", with("scope", "problems:read attempts:write"))
		r := e.do(t, "GET", "/api/v1/_test/read", bearer(tok), "")
		if r.code != 200 {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		w := r.who(t)
		// attempts:write is in the token but never in the client's allowed scopes.
		if w.Kind != "agent" || w.ClientID != "quizzotic-agent" || w.UserID != human.UserID ||
			!slices.Equal(w.Scopes, []string{"problems:read"}) {
			t.Errorf("principal = %+v (human %s)", w, human.UserID)
		}
		p := e.do(t, "POST", "/api/v1/_test/write", bearer(tok), "").wantProblem(t, 403, "forbidden")
		if !strings.Contains(p.Detail, "problems:write") {
			t.Errorf("detail = %q", p.Detail)
		}
		if r := e.do(t, "POST", "/api/v1/_test/write", bearer(tok), ""); r.header.Get("WWW-Authenticate") != "" {
			t.Error("403 must not challenge")
		}
	})
	t.Run("no scope claim: the client's allowed scopes", func(t *testing.T) {
		tok := e.idp.access("quizzotic-agent", "cli-sub")
		w := e.do(t, "POST", "/api/v1/_test/write", bearer(tok), "").who(t)
		if !slices.Equal(w.Scopes, []string{"problems:read", "problems:write"}) {
			t.Errorf("scopes = %v", w.Scopes)
		}
	})
	t.Run("never attempts:write", func(t *testing.T) {
		tok := e.idp.access("quizzotic-agent", "cli-sub", with("scope", "attempts:write"))
		e.do(t, "POST", "/api/v1/_test/attempt", bearer(tok), "").wantProblem(t, 403, "forbidden")
	})
}

func TestBearerHeadlessTokenActsFor(t *testing.T) {
	e := newITEnv(t)
	human := e.do(t, "GET", "/api/v1/_test/read", bearer(e.idp.access("quizzotic-cli", "cli-sub")), "").who(t)

	// A client-credentials token's sub is the service account, not the human.
	tok := e.idp.access("quizzotic-headless", "svc-account", with("scope", "problems:read problems:write"))
	r := e.do(t, "GET", "/api/v1/_test/read", bearer(tok), "")
	if r.code != 200 {
		t.Fatalf("status = %d (%s)", r.code, r.body)
	}
	w := r.who(t)
	if w.Kind != "agent" || w.ClientID != "quizzotic-headless" || w.UserID != human.UserID ||
		!slices.Equal(w.Scopes, []string{"problems:read"}) {
		t.Errorf("principal = %+v (human %s)", w, human.UserID)
	}
	if uid, _ := e.userFor(t, "svc-account"); uid != "" {
		t.Error("the service account's sub must not be provisioned as a user")
	}
	e.do(t, "POST", "/api/v1/_test/write", bearer(tok), "").wantProblem(t, 403, "forbidden")
}

func TestBearerRejections(t *testing.T) {
	e := newITEnv(t)
	good := func(m ...func(map[string]any)) string { return e.idp.access("quizzotic-cli", "evil-sub", m...) }
	noClient := func(c map[string]any) { delete(c, "client_id") }
	forged := e.idp.sign(e.idp.other, map[string]any{
		"iss": e.idp.srv.URL, "aud": itAudience, "sub": "evil-sub", "client_id": "quizzotic-cli",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	cases := []struct {
		name string
		hdr  map[string]string
		want string // detail
	}{
		{"missing credentials", nil, "authentication required"},
		{"expired token", bearer(good(with("exp", time.Now().Add(-time.Minute).Unix()))), "invalid credentials"},
		{"not yet valid", bearer(good(with("nbf", time.Now().Add(time.Hour).Unix()))), "invalid credentials"},
		{"unknown client", bearer(e.idp.access("stranger", "evil-sub")), "invalid credentials"},
		{"token without a client claim", bearer(good(noClient)), "invalid credentials"},
		{"bad signature", bearer(forged), "invalid credentials"},
		{"wrong audience", bearer(good(with("aud", "someone-else"))), "invalid credentials"},
		{"wrong issuer", bearer(good(with("iss", "https://other.example.com"))), "invalid credentials"},
		{"garbage token", bearer("not.a.jwt"), "invalid credentials"},
		{"wrong scheme", map[string]string{"Authorization": "Basic Zm9vOmJhcg=="}, "invalid credentials"},
		{"empty bearer", map[string]string{"Authorization": "Bearer "}, "invalid credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := e.do(t, "GET", "/api/v1/_test/read", tc.hdr, "")
			p := r.wantProblem(t, 401, "unauthorized")
			r.wantChallenge(t)
			if p.Detail != tc.want {
				t.Errorf("detail = %q, want %q", p.Detail, tc.want)
			}
			if p.RequestID == "" || p.Instance != "/api/v1/_test/read" {
				t.Errorf("problem lacks request id / instance: %+v", p)
			}
		})
	}
	if uid, _ := e.userFor(t, "evil-sub"); uid != "" {
		t.Error("a rejected token must not provision a user")
	}
}

func TestScopeFreeOperationsNeedNoCredentials(t *testing.T) {
	e := newITEnv(t)
	for _, path := range []string{"/api/v1/_test/open", "/api/v1/openapi.yaml"} {
		if r := e.do(t, "GET", path, nil, ""); r.code != 200 {
			t.Errorf("GET %s = %d", path, r.code)
		}
		// Bad credentials on a scope-free operation are not even looked at.
		if r := e.do(t, "GET", path, bearer("garbage"), ""); r.code != 200 {
			t.Errorf("GET %s with junk credentials = %d", path, r.code)
		}
	}
}

// webLogin drives the real login flow against the fake IdP and returns the
// session cookie value.
func (e *itEnv) webLogin(t *testing.T) string {
	t.Helper()
	r := e.do(t, "GET", "/auth/login", nil, "")
	if r.code != http.StatusSeeOther {
		t.Fatalf("login = %d", r.code)
	}
	loc, err := url.Parse(r.header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	e.idp.mu.Lock()
	e.idp.nonces["code-1"] = q.Get("nonce")
	e.idp.mu.Unlock()
	cb := e.do(t, "GET", "/auth/callback?"+url.Values{"code": {"code-1"}, "state": {q.Get("state")}}.Encode(), nil, "")
	if cb.code != http.StatusSeeOther {
		t.Fatalf("callback = %d (%s)", cb.code, cb.body)
	}
	for _, c := range (&http.Response{Header: cb.header}).Cookies() {
		if c.Name == auth.SessionCookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestWebCookieSession(t *testing.T) {
	e := newITEnv(t)
	cookie := e.webLogin(t)

	uid, u := e.userFor(t, "web-sub")
	if uid == "" || u.Email != "web@example.com" {
		t.Fatalf("login did not provision the user: %q %+v", uid, u)
	}

	r := e.do(t, "GET", "/api/v1/_test/read", nil, cookie)
	if r.code != 200 {
		t.Fatalf("status = %d (%s)", r.code, r.body)
	}
	if w := r.who(t); w.UserID != uid || w.Kind != "human" || w.ClientID != "web-session" || !slices.Equal(w.Scopes, allScopes) {
		t.Errorf("principal = %+v", w)
	}

	// Cookie-authenticated writes need the session's CSRF token.
	sum := sha256.Sum256([]byte(cookie))
	ws, err := e.st.WebSessions().GetByTokenHash(context.Background(), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	e.do(t, "POST", "/api/v1/_test/write", nil, cookie).wantProblem(t, 403, "forbidden")
	e.do(t, "POST", "/api/v1/_test/write", map[string]string{"X-CSRF-Token": "wrong"}, cookie).wantProblem(t, 403, "forbidden")
	if r := e.do(t, "POST", "/api/v1/_test/write", map[string]string{"X-CSRF-Token": ws.CSRFToken}, cookie); r.code != 200 {
		t.Errorf("write with CSRF token = %d (%s)", r.code, r.body)
	}

	// Unknown or forged cookie: 401 with a challenge.
	r = e.do(t, "GET", "/api/v1/_test/read", nil, "forged-cookie")
	r.wantProblem(t, 401, "unauthorized")
	r.wantChallenge(t)
}

func TestBearerAndCookieTogether(t *testing.T) {
	e := newITEnv(t)
	cookie := e.webLogin(t)
	webUser, _ := e.userFor(t, "web-sub")

	// A valid bearer token takes precedence over the cookie.
	w := e.do(t, "GET", "/api/v1/_test/read", bearer(e.idp.access("quizzotic-cli", "cli-sub")), cookie).who(t)
	if w.ClientID != "quizzotic-cli" || w.UserID == webUser {
		t.Errorf("bearer should win: %+v", w)
	}
	// A bad bearer token is not rescued by a valid cookie.
	e.do(t, "GET", "/api/v1/_test/read", bearer("garbage"), cookie).wantProblem(t, 401, "unauthorized")
}

func TestUsersAreIsolatedByIssuerSubject(t *testing.T) {
	e := newITEnv(t)
	a := e.do(t, "GET", "/api/v1/_test/read", bearer(e.idp.access("quizzotic-cli", "alice")), "").who(t)
	b := e.do(t, "GET", "/api/v1/_test/read", bearer(e.idp.access("quizzotic-cli", "bob")), "").who(t)
	if a.UserID == "" || a.UserID == b.UserID {
		t.Errorf("distinct subjects must be distinct users: %q %q", a.UserID, b.UserID)
	}
}

var _ store.Store = (*sqlite.Store)(nil)
