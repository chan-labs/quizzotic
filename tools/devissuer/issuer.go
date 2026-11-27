// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// User is the one identity the dev issuer signs in, with no password.
type User struct {
	Subject string
	Name    string
	Email   string
}

// DevUser matches the docker issuer's fixed user (dev/mock-oauth2.json).
var DevUser = User{Subject: "dev-user-1", Name: "Dev User", Email: "dev@example.com"}

// tokenTTL is the lifetime of every token the issuer mints.
const tokenTTL = time.Hour

var b64 = base64.RawURLEncoding

// Issuer is a fake, non-interactive OIDC issuer for local development: every
// authorization request signs DevUser in at once and every grant succeeds.
// Tokens are EdDSA JWTs whose aud and azp are the requesting client's ID,
// like Pocket ID and the docker issuer. It accepts any client secret. Never
// run it anywhere but a developer's machine.
type Issuer struct {
	url  string // issuer identifier, e.g. http://localhost:8099/dev
	user User
	priv ed25519.PrivateKey
	kid  string
	now  func() time.Time

	mu    sync.Mutex
	codes map[string]grant // authorization code or device code -> grant
}

type grant struct {
	clientID, nonce, challenge string
}

// New returns an issuer with identifier issuerURL and a fresh signing key.
// The key ID is derived from the key, so a restarted issuer publishes a new
// kid and servers refetch its JWKS.
func New(issuerURL string, u User) (*Issuer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pub)
	return &Issuer{
		url: strings.TrimSuffix(issuerURL, "/"), user: u, priv: priv,
		kid: b64.EncodeToString(sum[:8]), now: time.Now, codes: map[string]grant{},
	}, nil
}

// Handler serves the issuer's endpoints under the path of its URL.
func (s *Issuer) Handler() http.Handler {
	u, _ := url.Parse(s.url)
	p := strings.TrimSuffix(u.Path, "/")
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+p+"/.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET "+p+"/jwks", s.jwks)
	mux.HandleFunc("GET "+p+"/authorize", s.authorize)
	mux.HandleFunc("POST "+p+"/token", s.token)
	mux.HandleFunc("POST "+p+"/device", s.device)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, code, desc string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": desc})
}

func (s *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.url,
		"authorization_endpoint":                s.url + "/authorize",
		"token_endpoint":                        s.url + "/token",
		"device_authorization_endpoint":         s.url + "/device",
		"jwks_uri":                              s.url + "/jwks",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "client_credentials", "urn:ietf:params:oauth:grant-type:device_code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
	})
}

func (s *Issuer) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := s.priv.Public().(ed25519.PublicKey)
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": s.kid, "x": b64.EncodeToString(pub),
	}}})
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return b64.EncodeToString(b)
}

func (s *Issuer) remember(code string, g grant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[code] = g
}

func (s *Issuer) take(code string) (grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.codes[code]
	delete(s.codes, code)
	return g, ok
}

// authorize signs the dev user in without a prompt and redirects back with a
// code, as an IdP with an existing session would.
func (s *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || !redirect.IsAbs() || q.Get("client_id") == "" || q.Get("response_type") != "code" {
		http.Error(w, "dev issuer: need response_type=code, client_id and an absolute redirect_uri", http.StatusBadRequest)
		return
	}
	code := randomToken()
	s.remember(code, grant{clientID: q.Get("client_id"), nonce: q.Get("nonce"), challenge: q.Get("code_challenge")})
	rq := redirect.Query()
	rq.Set("code", code)
	if st := q.Get("state"); st != "" {
		rq.Set("state", st)
	}
	rq.Set("iss", s.url)
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// device starts a device authorization that is already approved: the first
// token poll succeeds.
func (s *Issuer) device(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") == "" {
		oauthError(w, "invalid_request", "client_id is required")
		return
	}
	code := randomToken()
	s.remember(code, grant{clientID: r.PostForm.Get("client_id")})
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code": code, "user_code": "DEV-CODE",
		"verification_uri": s.url + "/device", "expires_in": 600, "interval": 1,
	})
}

func (s *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", err.Error())
		return
	}
	f := r.PostForm
	clientID := f.Get("client_id")
	if id, _, ok := r.BasicAuth(); ok {
		clientID = id
	}
	var nonce string
	switch f.Get("grant_type") {
	case "authorization_code", "urn:ietf:params:oauth:grant-type:device_code":
		code := f.Get("code")
		if code == "" {
			code = f.Get("device_code")
		}
		g, ok := s.take(code)
		if !ok {
			oauthError(w, "invalid_grant", "unknown or used code")
			return
		}
		if g.challenge != "" {
			sum := sha256.Sum256([]byte(f.Get("code_verifier")))
			if b64.EncodeToString(sum[:]) != g.challenge {
				oauthError(w, "invalid_grant", "PKCE verifier does not match")
				return
			}
		}
		if clientID == "" {
			clientID = g.clientID
		}
		nonce = g.nonce
	case "refresh_token", "client_credentials":
	default:
		oauthError(w, "unsupported_grant_type", f.Get("grant_type"))
		return
	}
	if clientID == "" {
		oauthError(w, "invalid_client", "client_id is required")
		return
	}
	now := s.now()
	claims := map[string]any{
		"iss": s.url, "sub": s.user.Subject, "aud": clientID, "azp": clientID,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(tokenTTL).Unix(),
		"name": s.user.Name, "preferred_username": s.user.Name,
		"email": s.user.Email, "email_verified": true,
	}
	resp := map[string]any{
		"access_token": s.sign(claims), "token_type": "Bearer",
		"expires_in": int(tokenTTL.Seconds()), "refresh_token": randomToken(),
	}
	if nonce != "" || f.Get("grant_type") != "client_credentials" {
		id := map[string]any{}
		for k, v := range claims {
			id[k] = v
		}
		if nonce != "" {
			id["nonce"] = nonce
		}
		resp["id_token"] = s.sign(id)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Issuer) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": s.kid})
	c, _ := json.Marshal(claims)
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	return in + "." + b64.EncodeToString(ed25519.Sign(s.priv, []byte(in)))
}
