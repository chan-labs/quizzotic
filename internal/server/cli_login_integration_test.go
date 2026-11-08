// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/cli"
	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

const (
	loginHeadlessSecret = "headless/s3cret+x"
	loginHumanSub       = "cli-sub"
)

// loginIDP is a fake OIDC issuer for the CLI login flows: discovery, JWKS,
// authorize (redirects to the loopback), device authorization, and a token
// endpoint for the authorization-code, device-code, refresh-token and
// client-credentials grants. Only the issuer is fake; the server that
// verifies its tokens is the production handler.
type loginIDP struct {
	t    *testing.T
	srv  *httptest.Server
	priv ed25519.PrivateKey

	mu            sync.Mutex
	codes         map[string]loginCode // authorization code -> request
	refreshTokens map[string]string    // refresh token -> client
	devicePending int                  // device polls to answer authorization_pending
	deviceSlow    bool                 // answer slow_down once
	deviceClient  string
	refreshCalls  int
	nextRefresh   int
	issueExpired  bool // authorization-code tokens are already expired
	noDevice      bool // discovery omits the device endpoint
	revoked       bool // refresh grants answer invalid_grant
	grants        []string
}

type loginCode struct{ client, redirect, challenge string }

func newLoginIDP(t *testing.T) *loginIDP {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &loginIDP{t: t, priv: priv, codes: map[string]loginCode{}, refreshTokens: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
		}
		if !f.noDevice {
			doc["device_authorization_endpoint"] = f.srv.URL + "/device"
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "OKP", "crv": "Ed25519", "kid": "k1", "alg": "EdDSA", "x": b64.EncodeToString(pub)},
		}})
	})
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/device", f.device)
	mux.HandleFunc("/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *loginIDP) sign(claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "k1", "typ": "JWT"})
	pl, _ := json.Marshal(claims)
	signed := b64.EncodeToString(hdr) + "." + b64.EncodeToString(pl)
	sig, err := f.priv.Sign(rand.Reader, []byte(signed), crypto.Hash(0))
	if err != nil {
		f.t.Fatal(err)
	}
	return signed + "." + b64.EncodeToString(sig)
}

func (f *loginIDP) access(client, sub string, exp time.Time) string {
	return f.sign(map[string]any{
		"iss": f.srv.URL, "aud": itAudience, "sub": sub, "client_id": client,
		"iat": time.Now().Unix(), "exp": exp.Unix(), "jti": strconv.Itoa(f.nextRefresh) + client,
	})
}

func (f *loginIDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" || redirect.Path != "/callback" ||
		q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		len(q.Get("code_challenge")) < 32 || len(q.Get("state")) < 16 ||
		!strings.Contains(" "+q.Get("scope")+" ", " offline_access ") {
		http.Error(w, "bad authorization request: "+r.URL.RawQuery, http.StatusBadRequest)
		return
	}
	code := "code-" + q.Get("state")[:8]
	f.mu.Lock()
	f.codes[code] = loginCode{q.Get("client_id"), q.Get("redirect_uri"), q.Get("code_challenge")}
	f.mu.Unlock()
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
}

func (f *loginIDP) device(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.deviceClient = r.PostForm.Get("client_id")
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device_code": "dev-code", "user_code": "ABCD-EFGH", "verification_uri": f.srv.URL + "/activate",
		"expires_in": 600, "interval": 1,
	})
}

func (f *loginIDP) oauthErr(w http.ResponseWriter, code string, status int) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func (f *loginIDP) tokens(w http.ResponseWriter, client, sub string, expired bool) {
	exp := time.Now().Add(time.Hour)
	expiresIn := 3600
	if expired {
		exp, expiresIn = time.Now().Add(-time.Hour), 1
	}
	f.nextRefresh++
	rt := "refresh-" + strconv.Itoa(f.nextRefresh)
	f.refreshTokens[rt] = client
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": f.access(client, sub, exp), "refresh_token": rt,
		"token_type": "Bearer", "expires_in": expiresIn,
	})
}

func (f *loginIDP) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	grant := r.PostForm.Get("grant_type")
	f.grants = append(f.grants, grant)
	w.Header().Set("Content-Type", "application/json")
	switch grant {
	case "authorization_code":
		req, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || r.PostForm.Get("client_id") != req.client || r.PostForm.Get("redirect_uri") != req.redirect ||
			b64.EncodeToString(sum[:]) != req.challenge {
			f.oauthErr(w, "invalid_grant", http.StatusBadRequest)
			return
		}
		f.tokens(w, req.client, loginHumanSub, f.issueExpired)
	case "urn:ietf:params:oauth:grant-type:device_code":
		if r.PostForm.Get("device_code") != "dev-code" {
			f.oauthErr(w, "invalid_grant", http.StatusBadRequest)
			return
		}
		if f.devicePending > 0 {
			f.devicePending--
			f.oauthErr(w, "authorization_pending", http.StatusBadRequest)
			return
		}
		if f.deviceSlow {
			f.deviceSlow = false
			f.oauthErr(w, "slow_down", http.StatusBadRequest)
			return
		}
		f.tokens(w, r.PostForm.Get("client_id"), loginHumanSub, false)
	case "refresh_token":
		f.refreshCalls++
		old := r.PostForm.Get("refresh_token")
		client, ok := f.refreshTokens[old]
		delete(f.refreshTokens, old) // rotation: each refresh token is single use
		if f.revoked || !ok || client != r.PostForm.Get("client_id") {
			f.oauthErr(w, "invalid_grant", http.StatusBadRequest)
			return
		}
		f.tokens(w, client, loginHumanSub, false)
	case "client_credentials":
		id, secret, ok := r.BasicAuth()
		if !ok || id != "quizzotic-headless" || secret != url.QueryEscape(loginHeadlessSecret) {
			f.oauthErr(w, "invalid_client", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.access(id, "svc-account", time.Now().Add(time.Hour)),
			"token_type":   "Bearer", "expires_in": 3600,
		})
	default:
		f.oauthErr(w, "unsupported_grant_type", http.StatusBadRequest)
	}
}

func (f *loginIDP) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *loginIDP) grantCount(g string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.grants {
		if x == g {
			n++
		}
	}
	return n
}

// loginEnv is the production API (auth wiring and router) behind the fake IdP.
type loginEnv struct {
	idp   *loginIDP
	api   *httptest.Server
	creds string
	now   time.Time
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	ctx := context.Background()
	idp := newLoginIDP(t)
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
		Name: "test", Issuer: idp.srv.URL, Audiences: []string{itAudience},
		Scopes: []string{"openid", "profile", "email"},
		Clients: []config.Client{
			{ID: itWebClient, Kind: "human", Secret: itWebSecret},
			{ID: "quizzotic-cli", Kind: "human"},
			{ID: "quizzotic-agent", Kind: "agent", AllowedScopes: []string{"problems:read", "problems:write"}},
			{ID: "quizzotic-headless", Kind: "agent", ActsFor: loginHumanSub, AllowedScopes: []string{"problems:read"}},
		},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hopts, err := server.AuthOptions(&cfg, logger, st, auth.NewProvider(idp.srv.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(server.Handler(logger, st, auth.NewProvider(idp.srv.URL), hopts...))
	t.Cleanup(api.Close)
	return &loginEnv{idp: idp, api: api, creds: filepath.Join(t.TempDir(), "cfg", "credentials.json"), now: time.Now()}
}

type cliRun struct {
	env     map[string]string
	opener  func(string) error
	sleeps  int
	pollGap []time.Duration
}

func (e *loginEnv) run(t *testing.T, r *cliRun, args ...string) (int, string, string) {
	t.Helper()
	if r == nil {
		r = &cliRun{}
	}
	var o, er bytes.Buffer
	env := cli.Env{
		Stdout: &o, Stderr: &er, Stdin: strings.NewReader(""),
		Getenv: func(k string) string {
			if v, ok := r.env[k]; ok {
				return v
			}
			switch k {
			case "QUIZZOTIC_CREDENTIALS":
				return e.creds
			case "QUIZZOTIC_SERVER":
				return e.api.URL
			}
			return ""
		},
		Now: func() time.Time { return e.now },
		Sleep: func(_ context.Context, d time.Duration) error {
			r.sleeps++
			r.pollGap = append(r.pollGap, d)
			return nil
		},
		OpenBrowser: func(u string) error {
			if r.opener != nil {
				return r.opener(u)
			}
			return followToLoopback(u)
		},
	}
	var code int
	switch args[0] {
	case "login":
		code = cli.Login(context.Background(), args[1:], env)
	case "logout":
		code = cli.Logout(context.Background(), args[1:], env)
	default:
		code = cli.Run(context.Background(), args, env)
	}
	return code, o.String(), er.String()
}

// followToLoopback plays the browser: it visits the authorization URL, is
// redirected to the CLI's loopback callback, and loads that page.
func followToLoopback(u string) error {
	resp, err := http.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return errors.New("browser landed on " + resp.Status + ": " + string(b))
	}
	return nil
}

func (e *loginEnv) me(t *testing.T, r *cliRun, args ...string) meBody {
	t.Helper()
	code, out, errOut := e.run(t, r, append([]string{"me"}, args...)...)
	if code != 0 {
		t.Fatalf("quizzotic me %v = %d: %s %s", args, code, out, errOut)
	}
	var m meBody
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("me output %q: %v", out, err)
	}
	return m
}

func (e *loginEnv) mustLogin(t *testing.T, r *cliRun, args ...string) string {
	t.Helper()
	code, out, errOut := e.run(t, r, append([]string{"login"}, args...)...)
	if code != 0 {
		t.Fatalf("login %v = %d: %s %s", args, code, out, errOut)
	}
	return out + errOut
}

func readCreds(t *testing.T, path string) map[string]cli.Credential {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cf map[string]cli.Credential
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatal(err)
	}
	return cf
}

func TestCLILoginPKCELoopback(t *testing.T) {
	e := newLoginEnv(t)
	// The issuer is found through the server's protected-resource metadata.
	e.mustLogin(t, nil)

	info, err := os.Stat(e.creds)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode = %v, want 0600", info.Mode().Perm())
	}
	if dir, _ := os.Stat(filepath.Dir(e.creds)); dir.Mode().Perm() != 0o700 {
		t.Errorf("credentials dir mode = %v, want 0700", dir.Mode().Perm())
	}
	cf := readCreds(t, e.creds)
	if cf["human"].ClientID != "quizzotic-cli" || cf["human"].RefreshToken == "" || cf["agent"].AccessToken != "" {
		t.Errorf("credentials = %+v", cf)
	}

	m := e.me(t, nil)
	if m.Kind != "human" || m.ClientID != "quizzotic-cli" || m.User.ID == "" {
		t.Errorf("me = %+v", m)
	}
}

func TestCLILoginRejectsWrongState(t *testing.T) {
	e := newLoginEnv(t)
	r := &cliRun{opener: func(authURL string) error {
		u, _ := url.Parse(authURL)
		resp, err := http.Get(u.Query().Get("redirect_uri") + "?code=stolen&state=wrong")
		if err == nil {
			resp.Body.Close()
		}
		return nil
	}}
	code, _, errOut := e.run(t, r, "login")
	if code != 1 || !strings.Contains(errOut, "unexpected state") {
		t.Errorf("code %d err %q", code, errOut)
	}
	if _, err := os.Stat(e.creds); err == nil {
		t.Error("credentials written after a failed login")
	}
}

func TestCLILoginAgentStoredSeparately(t *testing.T) {
	e := newLoginEnv(t)
	e.mustLogin(t, nil)
	e.mustLogin(t, nil, "--agent")

	cf := readCreds(t, e.creds)
	if cf["human"].ClientID != "quizzotic-cli" || cf["agent"].ClientID != "quizzotic-agent" ||
		cf["human"].AccessToken == cf["agent"].AccessToken {
		t.Errorf("credentials = %+v", cf)
	}
	human := e.me(t, nil)
	agent := e.me(t, nil, "--agent")
	if human.Kind != "human" || human.ClientID != "quizzotic-cli" {
		t.Errorf("human me = %+v", human)
	}
	if agent.Kind != "agent" || agent.ClientID != "quizzotic-agent" {
		t.Errorf("agent me = %+v", agent)
	}
	if agent.User.ID != human.User.ID {
		t.Errorf("agent must act as the human in sub: %s != %s", agent.User.ID, human.User.ID)
	}
	// QUIZZOTIC_AGENT selects the agent login too.
	if m := e.me(t, &cliRun{env: map[string]string{"QUIZZOTIC_AGENT": "1"}}); m.Kind != "agent" {
		t.Errorf("QUIZZOTIC_AGENT me = %+v", m)
	}
}

func TestCLILoginDeviceFlow(t *testing.T) {
	e := newLoginEnv(t)
	e.idp.set(func() { e.idp.devicePending, e.idp.deviceSlow = 2, true })
	r := &cliRun{opener: func(string) error { t.Error("device flow must not open a browser"); return nil }}
	out := e.mustLogin(t, r, "--device")
	if !strings.Contains(out, "ABCD-EFGH") || !strings.Contains(out, e.idp.srv.URL+"/activate") {
		t.Errorf("instructions missing the code/URL: %q", out)
	}
	// Two pending polls, then slow_down (+5s), then success.
	if r.sleeps != 4 || r.pollGap[0] != time.Second || r.pollGap[3] != 6*time.Second {
		t.Errorf("polls = %d gaps %v", r.sleeps, r.pollGap)
	}
	if m := e.me(t, nil); m.Kind != "human" || m.ClientID != "quizzotic-cli" {
		t.Errorf("me = %+v", m)
	}

	// The agent client over device flow.
	e.mustLogin(t, nil, "--device", "--agent")
	if m := e.me(t, nil, "--agent"); m.Kind != "agent" || m.ClientID != "quizzotic-agent" {
		t.Errorf("agent me = %+v", m)
	}
}

func TestCLILoginFallsBackToDeviceWithoutBrowser(t *testing.T) {
	e := newLoginEnv(t)
	r := &cliRun{opener: func(string) error { return errors.New("no display") }}
	e.mustLogin(t, r)
	if e.idp.grantCount("urn:ietf:params:oauth:grant-type:device_code") == 0 {
		t.Error("expected the device flow after the browser failed")
	}
	if m := e.me(t, nil); m.Kind != "human" {
		t.Errorf("me = %+v", m)
	}

	// Without device support the loopback flow keeps waiting for a pasted URL.
	e2 := newLoginEnv(t)
	e2.idp.set(func() { e2.idp.noDevice = true })
	var seen string
	r2 := &cliRun{opener: func(u string) error { seen = u; _ = followToLoopback(u); return errors.New("no display") }}
	out := e2.mustLogin(t, r2)
	if !strings.Contains(out, "Open this URL") || !strings.Contains(out, seen) {
		t.Errorf("manual URL not printed: %q", out)
	}
}

func TestCLIHeadlessClientCredentials(t *testing.T) {
	e := newLoginEnv(t)
	human := func() meBody { // provision the human the client acts for
		e.mustLogin(t, nil)
		return e.me(t, nil)
	}()
	e.run(t, nil, "logout")

	r := &cliRun{env: map[string]string{
		"QUIZZOTIC_CLIENT_ID": "quizzotic-headless", "QUIZZOTIC_CLIENT_SECRET": loginHeadlessSecret,
	}}
	m := e.me(t, r)
	if m.Kind != "agent" || m.ClientID != "quizzotic-headless" || m.User.ID != human.User.ID {
		t.Errorf("me = %+v, want agent acting for %s", m, human.User.ID)
	}
	if _, err := os.Stat(e.creds); err == nil {
		t.Error("headless login must not write credentials")
	}

	r.env["QUIZZOTIC_CLIENT_SECRET"] = "wrong"
	code, _, errOut := e.run(t, r, "me")
	if code != 1 || !strings.Contains(errOut, "invalid_client") {
		t.Errorf("wrong secret: code %d err %q", code, errOut)
	}
	r.env["QUIZZOTIC_CLIENT_SECRET"] = ""
	if code, _, errOut := e.run(t, r, "me"); code != 1 || !strings.Contains(errOut, "both QUIZZOTIC_CLIENT_ID") {
		t.Errorf("half-set headless: code %d err %q", code, errOut)
	}
}

func TestCLISilentRefresh(t *testing.T) {
	t.Run("clock passes expiry", func(t *testing.T) {
		e := newLoginEnv(t)
		e.mustLogin(t, nil)
		before := readCreds(t, e.creds)["human"]
		if m := e.me(t, nil); m.Kind != "human" || e.idp.grantCount("refresh_token") != 0 {
			t.Fatalf("fresh token must not refresh: %+v", m)
		}

		e.now = e.now.Add(2 * time.Hour)
		m := e.me(t, nil)
		if m.Kind != "human" || m.ClientID != "quizzotic-cli" {
			t.Errorf("me after expiry = %+v", m)
		}
		after := readCreds(t, e.creds)["human"]
		if e.idp.grantCount("refresh_token") != 1 || after.AccessToken == before.AccessToken ||
			after.RefreshToken == before.RefreshToken || !after.ExpiresAt.After(e.now) {
			t.Errorf("refresh calls %d, before %+v after %+v", e.idp.grantCount("refresh_token"), before, after)
		}
		// The rotated token is stored, so the next run does not refresh again.
		e.me(t, nil)
		if n := e.idp.grantCount("refresh_token"); n != 1 {
			t.Errorf("refresh calls = %d, want 1", n)
		}
		if info, _ := os.Stat(e.creds); info.Mode().Perm() != 0o600 {
			t.Errorf("mode after refresh = %v", info.Mode().Perm())
		}
	})

	t.Run("server rejects the expired token the IdP first issued", func(t *testing.T) {
		e := newLoginEnv(t)
		e.idp.set(func() { e.idp.issueExpired = true })
		e.mustLogin(t, nil)
		stale := readCreds(t, e.creds)["human"].AccessToken
		// The stored token really is expired for the API...
		var o, er bytes.Buffer
		code := cli.Run(context.Background(), []string{"me", "--server", e.api.URL}, cli.Env{
			Stdout: &o, Stderr: &er,
			Getenv: func(k string) string {
				if k == "QUIZZOTIC_TOKEN" {
					return stale
				}
				return ""
			},
		})
		if code != 1 || !strings.Contains(er.String(), "401") {
			t.Fatalf("stale token accepted: code %d %s", code, er.String())
		}
		// ...yet the CLI notices and swaps it for a fresh one on its own.
		if m := e.me(t, nil); m.Kind != "human" {
			t.Errorf("me = %+v", m)
		}
		if n := e.idp.grantCount("refresh_token"); n != 1 {
			t.Errorf("refresh calls = %d, want 1", n)
		}
	})

	t.Run("agent profile refreshes too", func(t *testing.T) {
		e := newLoginEnv(t)
		e.mustLogin(t, nil, "--agent")
		e.now = e.now.Add(2 * time.Hour)
		if m := e.me(t, nil, "--agent"); m.Kind != "agent" || e.idp.grantCount("refresh_token") != 1 {
			t.Errorf("me = %+v refreshes %d", m, e.idp.grantCount("refresh_token"))
		}
	})

	t.Run("revoked refresh token asks for a new login", func(t *testing.T) {
		e := newLoginEnv(t)
		e.mustLogin(t, nil)
		e.idp.set(func() { e.idp.revoked = true })
		e.now = e.now.Add(2 * time.Hour)
		code, _, errOut := e.run(t, nil, "me")
		if code != 1 || !strings.Contains(errOut, "quizzotic login") {
			t.Errorf("code %d err %q", code, errOut)
		}
	})
}

func TestCLITokenEnvOverridesStoredLogin(t *testing.T) {
	e := newLoginEnv(t)
	e.mustLogin(t, nil)
	tok := e.idp.access("quizzotic-agent", loginHumanSub, time.Now().Add(time.Hour))
	m := e.me(t, &cliRun{env: map[string]string{"QUIZZOTIC_TOKEN": tok}})
	if m.Kind != "agent" || m.ClientID != "quizzotic-agent" {
		t.Errorf("me = %+v, want the QUIZZOTIC_TOKEN agent token", m)
	}
}

func TestCLILogout(t *testing.T) {
	e := newLoginEnv(t)
	e.mustLogin(t, nil)
	e.mustLogin(t, nil, "--agent")

	if code, out, _ := e.run(t, nil, "logout"); code != 0 || !strings.Contains(out, "Logged out") {
		t.Fatalf("logout: %d %q", code, out)
	}
	cf := readCreds(t, e.creds)
	if _, ok := cf["human"]; ok || cf["agent"].AccessToken == "" {
		t.Errorf("after logout credentials = %+v", cf)
	}
	// Logged out: the API answers 401.
	if code, _, errOut := e.run(t, nil, "me"); code != 1 || !strings.Contains(errOut, "401") {
		t.Errorf("me after logout: %d %q", code, errOut)
	}
	if m := e.me(t, nil, "--agent"); m.Kind != "agent" {
		t.Errorf("agent login should survive: %+v", m)
	}

	if code, _, _ := e.run(t, nil, "logout", "--agent"); code != 0 {
		t.Fatal("logout --agent failed")
	}
	if _, err := os.Stat(e.creds); !os.IsNotExist(err) {
		t.Errorf("credentials file should be gone: %v", err)
	}
	if code, out, _ := e.run(t, nil, "logout"); code != 0 || !strings.Contains(out, "Not logged in") {
		t.Errorf("repeat logout: %d %q", code, out)
	}
	code, _, errOut := e.run(t, nil, "me", "--agent")
	if code != 1 || !strings.Contains(errOut, "login --agent") {
		t.Errorf("agent without login: %d %q", code, errOut)
	}
}
