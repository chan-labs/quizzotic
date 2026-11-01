// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

var b64u = base64.RawURLEncoding

const (
	testClientID     = "quizzotic-web"
	testClientSecret = "s3cret/with+chars"
)

// loginIdP is a minimal OIDC provider: discovery, JWKS, an authorization
// endpoint the test drives by hand, and a token endpoint that enforces PKCE,
// client authentication and the redirect URI.
type loginIdP struct {
	t     *testing.T
	srv   *httptest.Server
	clock *domain.FakeClock
	priv  ed25519.PrivateKey

	mu            sync.Mutex
	codes         map[string]authRequest // code -> request it was issued for
	tokenCalls    int
	breakExchange bool   // token endpoint answers 500
	forceNonce    string // when set, ID tokens carry this nonce instead
}

type authRequest struct {
	clientID, redirectURI, nonce, challenge string
}

func newLoginIdP(t *testing.T, clock *domain.FakeClock) *loginIdP {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &loginIdP{t: t, clock: clock, priv: priv, codes: map[string]authRequest{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 f.srv.URL,
			"authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint":         f.srv.URL + "/token",
			"jwks_uri":               f.srv.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "OKP", "crv": "Ed25519", "kid": "k1", "alg": "EdDSA", "x": b64u.EncodeToString(pub)},
		}})
	})
	mux.HandleFunc("/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// authorize plays the browser's trip to the IdP: it checks the authorization
// request and returns the callback URL the IdP would redirect to.
func (f *loginIdP) authorize(location string) string {
	f.t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, f.srv.URL+"/authorize?") {
		f.t.Fatalf("login redirect = %q, want the IdP authorization endpoint", location)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": testClientID, "code_challenge_method": "S256",
		"redirect_uri": "https://quizzotic.example.com/auth/callback", "scope": "openid profile email",
	} {
		if q.Get(k) != want {
			f.t.Fatalf("authorize param %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q.Get("state")) < 32 || len(q.Get("nonce")) < 32 || len(q.Get("code_challenge")) < 32 {
		f.t.Fatalf("state/nonce/challenge too short: %v", q)
	}
	code := "code-" + q.Get("state")[:8]
	f.mu.Lock()
	f.codes[code] = authRequest{q.Get("client_id"), q.Get("redirect_uri"), q.Get("nonce"), q.Get("code_challenge")}
	f.mu.Unlock()
	return "/auth/callback?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

func (f *loginIdP) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++
	if f.breakExchange {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok || id != testClientID || secret != url.QueryEscape(testClientSecret) {
		http.Error(w, "bad client auth", http.StatusUnauthorized)
		return
	}
	_ = r.ParseForm()
	req, found := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code")) // codes are single use
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || r.PostForm.Get("grant_type") != "authorization_code" ||
		r.PostForm.Get("redirect_uri") != req.redirectURI || b64u.EncodeToString(sum[:]) != req.challenge {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	nonce := req.nonce
	if f.forceNonce != "" {
		nonce = f.forceNonce
	}
	idToken := f.sign(map[string]any{
		"iss": f.srv.URL, "sub": "subject-1", "aud": testClientID, "nonce": nonce,
		"exp": f.clock.Now().Add(time.Hour).Unix(), "name": "Ada Lovelace", "email": "ada@example.com",
	})
	_ = json.NewEncoder(w).Encode(map[string]string{"id_token": idToken, "access_token": "unused", "token_type": "Bearer"})
}

func (f *loginIdP) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *loginIdP) sign(claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "k1", "typ": "JWT"})
	pl, _ := json.Marshal(claims)
	signed := b64u.EncodeToString(hdr) + "." + b64u.EncodeToString(pl)
	sig, err := f.priv.Sign(rand.Reader, []byte(signed), crypto.Hash(0))
	if err != nil {
		f.t.Fatal(err)
	}
	return signed + "." + b64u.EncodeToString(sig)
}

// app is Quizzotic's web side wired over a store file, so a test can "restart"
// by building a second app over the same file.
type app struct {
	t     *testing.T
	st    *sqlite.Store
	svc   *auth.LoginService
	srv   *httptest.Server
	idp   *loginIdP
	clock *domain.FakeClock
}

func newApp(t *testing.T, dbPath string, idp *loginIdP, clock *domain.FakeClock) *app {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ids := domain.NewIDGenerator(clock)
	prov := auth.NewProvider(idp.srv.URL)
	ver := auth.NewVerifier(idp.srv.URL, prov, auth.WithVerifierClock(clock.Now))
	svc := auth.NewLoginService(auth.LoginConfig{
		ClientID: testClientID, ClientSecret: testClientSecret,
		Scopes: []string{"openid", "profile", "email"}, PublicURL: "https://quizzotic.example.com/",
	}, st, prov, ver, auth.NewService(store.AuthTx(st), clock, ids), clock, ids, nil)
	h := auth.NewLoginHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))

	mux := http.NewServeMux()
	h.Register(mux)
	mux.Handle("GET /me", h.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, _ := auth.SessionFrom(r.Context())
		_, _ = io.WriteString(w, ws.UserID.String()+" "+ws.CSRFToken)
	})))
	a := &app{t: t, st: st, svc: svc, srv: httptest.NewServer(mux), idp: idp, clock: clock}
	t.Cleanup(a.stop)
	return a
}

func (a *app) stop() {
	if a.srv != nil {
		a.srv.Close()
		a.st.Close()
		a.srv = nil
	}
}

func (a *app) do(method, path, cookie string, form url.Values) *http.Response {
	a.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, a.srv.URL+path, body)
	if err != nil {
		a.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (a *app) body(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	return nil
}

// login runs login -> IdP -> callback and returns the callback response.
func (a *app) login(returnTo string) *http.Response {
	a.t.Helper()
	path := "/auth/login"
	if returnTo != "" {
		path += "?return_to=" + url.QueryEscape(returnTo)
	}
	resp := a.do("GET", path, "", nil)
	if resp.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("login status = %d", resp.StatusCode)
	}
	return a.do("GET", a.idp.authorize(resp.Header.Get("Location")), "", nil)
}

func setup(t *testing.T) (*loginIdP, *domain.FakeClock, string) {
	t.Helper()
	clock := domain.NewFakeClock(time.Now().UTC())
	return newLoginIdP(t, clock), clock, filepath.Join(t.TempDir(), "q.db")
}

func TestLoginSessionSurvivesRestartThenLogout(t *testing.T) {
	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)

	cb := a.login("/problems?tag=go")
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/problems?tag=go" {
		t.Fatalf("callback = %d -> %q", cb.StatusCode, cb.Header.Get("Location"))
	}
	c := sessionCookie(cb)
	if c == nil || c.Value == "" {
		t.Fatal("callback set no session cookie")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 30*24*3600 {
		t.Fatalf("cookie attributes = %+v", c)
	}

	me := a.do("GET", "/me", c.Value, nil)
	who := a.body(me)
	if me.StatusCode != 200 || who == "" {
		t.Fatalf("/me = %d %q", me.StatusCode, who)
	}
	userID, csrf, _ := strings.Cut(who, " ")

	// JIT provisioning copied the claims.
	u, err := a.st.Users().Get(context.Background(), mustID(t, userID))
	if err != nil || u.DisplayName != "Ada Lovelace" || u.Email != "ada@example.com" {
		t.Fatalf("user = %+v err=%v", u, err)
	}

	// Only a hash of the cookie token is stored.
	var n int
	if err := a.st.DB().QueryRow(`SELECT count(*) FROM web_sessions WHERE token_hash = ?`, c.Value).Scan(&n); err != nil || n != 0 {
		t.Fatalf("raw cookie token found in web_sessions (n=%d err=%v)", n, err)
	}
	sum := sha256.Sum256([]byte(c.Value))
	if err := a.st.DB().QueryRow(`SELECT count(*) FROM web_sessions WHERE token_hash = ?`, hex.EncodeToString(sum[:])).Scan(&n); err != nil || n != 1 {
		t.Fatalf("hashed token not stored (n=%d err=%v)", n, err)
	}

	// Restart: new process state over the same store file.
	a.stop()
	b := newApp(t, db, idp, clock)
	if r := b.do("GET", "/me", c.Value, nil); r.StatusCode != 200 || !strings.HasPrefix(b.body(r), userID) {
		t.Fatalf("session did not survive restart: %d", r.StatusCode)
	}
	b.idp.mu.Lock()
	got := b.idp.tokenCalls
	b.idp.mu.Unlock()
	if got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", got)
	}

	// Logout needs the CSRF token.
	if r := b.do("POST", "/auth/logout", c.Value, url.Values{}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without csrf = %d, want 403", r.StatusCode)
	}
	if r := b.do("POST", "/auth/logout", c.Value, url.Values{"csrf_token": {"wrong"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("logout with wrong csrf = %d, want 403", r.StatusCode)
	}
	if r := b.do("GET", "/me", c.Value, nil); r.StatusCode != 200 {
		t.Fatal("failed logout attempts must not end the session")
	}
	out := b.do("POST", "/auth/logout", c.Value, url.Values{"csrf_token": {csrf}})
	if out.StatusCode != http.StatusSeeOther || out.Header.Get("Location") != "/" {
		t.Fatalf("logout = %d -> %q", out.StatusCode, out.Header.Get("Location"))
	}
	if cc := sessionCookie(out); cc == nil || cc.MaxAge >= 0 {
		t.Fatalf("logout did not clear the cookie: %+v", cc)
	}
	if r := b.do("GET", "/me", c.Value, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout /me = %d, want 401", r.StatusCode)
	}

	// Logging in again reuses the same user.
	again := sessionCookie(b.login(""))
	r := b.do("GET", "/me", again.Value, nil)
	if !strings.HasPrefix(b.body(r), userID) {
		t.Fatal("second login resolved to a different user")
	}
}

func TestLogoutHeaderCSRFAndNoSession(t *testing.T) {
	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)
	c := sessionCookie(a.login(""))
	csrf := strings.Fields(a.body(a.do("GET", "/me", c.Value, nil)))[1]

	req, _ := http.NewRequest("POST", a.srv.URL+"/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: c.Value})
	req.Header.Set(auth.CSRFHeader, csrf)
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("header csrf logout: %v %v", resp, err)
	}
	resp.Body.Close()

	// No session: logout is a harmless redirect that clears the cookie.
	r := a.do("POST", "/auth/logout", "", url.Values{})
	if r.StatusCode != http.StatusSeeOther || sessionCookie(r) == nil {
		t.Fatalf("anonymous logout = %d", r.StatusCode)
	}
}

func TestSlidingExpiry(t *testing.T) {
	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)
	c := sessionCookie(a.login(""))

	// Active use every 20 days keeps the session alive well past 30 days, and
	// re-issues the cookie each time the expiry slides.
	for i := 0; i < 3; i++ {
		clock.Advance(20 * 24 * time.Hour)
		r := a.do("GET", "/me", c.Value, nil)
		if r.StatusCode != 200 {
			t.Fatalf("visit %d: status %d, want 200 (sliding)", i, r.StatusCode)
		}
		rc := sessionCookie(r)
		if rc == nil || rc.Value != c.Value || rc.MaxAge != 30*24*3600 {
			t.Fatalf("visit %d: cookie not re-issued: %+v", i, rc)
		}
	}
	// Idle for more than the TTL: gone, and the row is deleted.
	clock.Advance(31 * 24 * time.Hour)
	if r := a.do("GET", "/me", c.Value, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("idle session status = %d, want 401", r.StatusCode)
	}
	var n int
	_ = a.st.DB().QueryRow(`SELECT count(*) FROM web_sessions`).Scan(&n)
	if n != 0 {
		t.Fatalf("expired session row not deleted (n=%d)", n)
	}
}

func TestSlidingWritesAreThrottled(t *testing.T) {
	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)
	c := sessionCookie(a.login(""))
	clock.Advance(10 * time.Second)
	if r := a.do("GET", "/me", c.Value, nil); r.StatusCode != 200 || sessionCookie(r) != nil {
		t.Fatalf("a request within a minute should not re-issue the cookie (status %d)", r.StatusCode)
	}
}

func TestCallbackRejections(t *testing.T) {
	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)

	loginURL := func() (state, callback string) {
		resp := a.do("GET", "/auth/login", "", nil)
		cb := idp.authorize(resp.Header.Get("Location"))
		u, _ := url.Parse(cb)
		return u.Query().Get("state"), cb
	}

	t.Run("state is single use", func(t *testing.T) {
		_, cb := loginURL()
		if r := a.do("GET", cb, "", nil); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("first callback = %d", r.StatusCode)
		}
		if r := a.do("GET", cb, "", nil); r.StatusCode != http.StatusBadRequest || sessionCookie(r) != nil {
			t.Fatalf("replayed callback = %d, want 400 without a cookie", r.StatusCode)
		}
	})
	t.Run("unknown state", func(t *testing.T) {
		if r := a.do("GET", "/auth/callback?code=x&state=nope", "", nil); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d", r.StatusCode)
		}
		if r := a.do("GET", "/auth/callback", "", nil); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("missing params status = %d", r.StatusCode)
		}
	})
	t.Run("expired state", func(t *testing.T) {
		_, cb := loginURL()
		clock.Advance(11 * time.Minute)
		if r := a.do("GET", cb, "", nil); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", r.StatusCode)
		}
	})
	t.Run("issuer error burns the state", func(t *testing.T) {
		state, cb := loginURL()
		r := a.do("GET", "/auth/callback?error=access_denied&state="+state, "", nil)
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", r.StatusCode)
		}
		if r := a.do("GET", cb, "", nil); r.StatusCode != http.StatusBadRequest {
			t.Fatalf("state reusable after issuer error: %d", r.StatusCode)
		}
	})
	t.Run("nonce mismatch in id token", func(t *testing.T) {
		idp.set(func() { idp.forceNonce = "not-the-nonce" })
		defer idp.set(func() { idp.forceNonce = "" })
		_, cb := loginURL()
		r := a.do("GET", cb, "", nil)
		if r.StatusCode != http.StatusUnauthorized || sessionCookie(r) != nil {
			t.Fatalf("status = %d, want 401 without a cookie", r.StatusCode)
		}
	})
	t.Run("token endpoint failure", func(t *testing.T) {
		idp.set(func() { idp.breakExchange = true })
		defer idp.set(func() { idp.breakExchange = false })
		_, cb := loginURL()
		r := a.do("GET", cb, "", nil)
		if r.StatusCode != http.StatusBadGateway || sessionCookie(r) != nil {
			t.Fatalf("status = %d, want 502 without a cookie", r.StatusCode)
		}
	})
	t.Run("wrong pkce verifier is rejected by the issuer", func(t *testing.T) {
		// Swap the stored challenge so the verifier no longer matches.
		_, cb := loginURL()
		idp.mu.Lock()
		for k, v := range idp.codes {
			v.challenge = "different"
			idp.codes[k] = v
		}
		idp.mu.Unlock()
		if r := a.do("GET", cb, "", nil); r.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", r.StatusCode)
		}
	})
}

func TestReturnToIsLocalOnly(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "/",
		"/problems":              "/problems",
		"/a?b=c#d":               "/a?b=c#d",
		"//evil.example.com":     "/",
		"https://evil.example/":  "/",
		"/\\evil.example.com":    "/",
		"relative":               "/",
		"/ok\r\nSet-Cookie: x=1": "/",
	} {
		if got := auth.SafeReturnTo(in); got != want {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}

	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)
	if cb := a.login("//evil.example.com/x"); cb.Header.Get("Location") != "/" {
		t.Fatalf("open redirect: Location = %q", cb.Header.Get("Location"))
	}
}

func TestRequireSessionRejectsMissingOrForgedCookie(t *testing.T) {
	idp, clock, db := setup(t)
	a := newApp(t, db, idp, clock)
	for _, c := range []string{"", "forged", strings.Repeat("A", 43)} {
		if r := a.do("GET", "/me", c, nil); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("cookie %q: status = %d, want 401", c, r.StatusCode)
		}
	}
}

func TestCheckCSRF(t *testing.T) {
	ws := auth.WebSession{CSRFToken: "tok"}
	post := func(header, field string) *http.Request {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(url.Values{auth.CSRFFormField: {field}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if header != "" {
			r.Header.Set(auth.CSRFHeader, header)
		}
		return r
	}
	if !auth.CheckCSRF(post("tok", ""), ws) || !auth.CheckCSRF(post("", "tok"), ws) {
		t.Error("valid token rejected")
	}
	if auth.CheckCSRF(post("", ""), ws) || auth.CheckCSRF(post("bad", ""), ws) || auth.CheckCSRF(post("", "bad"), ws) {
		t.Error("invalid token accepted")
	}
	if auth.CheckCSRF(post("", ""), auth.WebSession{}) || auth.CheckCSRF(post("x", ""), auth.WebSession{}) {
		t.Error("session without a token must never match")
	}
}

func mustID(t *testing.T, s string) domain.ID {
	t.Helper()
	id, err := domain.ParseID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
