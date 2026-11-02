// SPDX-License-Identifier: Apache-2.0

// Command devissuer is a throwaway OIDC issuer for local development and
// agent-driven UI testing. It signs in one fixed test user without asking for
// a password, issues RS256 tokens, and listens on loopback over plain http.
//
// NOT FOR PRODUCTION: it has no real authentication, regenerates its signing
// key on every start, and keeps all state in memory. It is not part of the
// quizzotic binary; run it with `mise run dev-issuer`.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type config struct {
	issuer    string // base URL, also the "iss" claim
	audience  string // access-token "aud"
	subject   string
	name      string
	email     string
	clients   map[string]string // client id -> secret ("" = public client)
	tokenTTL  time.Duration
	codeTTL   time.Duration
	kid       string
	signerKey *rsa.PrivateKey
}

type authCode struct {
	clientID, redirectURI, nonce, challenge, scope string
	expires                                        time.Time
}

type issuer struct {
	cfg   config
	mu    sync.Mutex
	codes map[string]authCode
}

func main() {
	addr := flag.String("addr", env("DEVISSUER_ADDR", "127.0.0.1:8099"), "listen address (loopback only)")
	subject := flag.String("sub", env("DEVISSUER_SUB", "dev-user-1"), "subject of the fixed test user")
	name := flag.String("name", env("DEVISSUER_NAME", "Dev User"), "display name of the test user")
	email := flag.String("email", env("DEVISSUER_EMAIL", "dev@example.com"), "email of the test user")
	audience := flag.String("audience", env("DEVISSUER_AUDIENCE", "quizzotic"), "audience of access tokens")
	clients := flag.String("clients", env("DEVISSUER_CLIENTS", "quizzotic-web:dev-secret,quizzotic-cli:,quizzotic-agent:"), "comma-separated id:secret pairs; empty secret = public client")
	flag.Parse()

	host, port, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("devissuer: bad -addr: %v", err)
	}
	if !loopback(host) {
		log.Fatalf("devissuer: -addr must be a loopback address, got %q", host)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	cs := map[string]string{}
	for _, p := range strings.Split(*clients, ",") {
		if id, secret, _ := strings.Cut(strings.TrimSpace(p), ":"); id != "" {
			cs[id] = secret
		}
	}
	is := &issuer{
		cfg: config{
			issuer: "http://" + net.JoinHostPort(host, port), audience: *audience,
			subject: *subject, name: *name, email: *email, clients: cs,
			tokenTTL: time.Hour, codeTTL: time.Minute, kid: "dev-key-1", signerKey: key,
		},
		codes: map[string]authCode{},
	}
	log.Printf("devissuer: DEV ONLY fake OIDC issuer %s (user %q)", is.cfg.issuer, *subject)
	srv := &http.Server{Addr: *addr, Handler: is.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (is *issuer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /.well-known/openid-configuration", is.discovery)
	mux.HandleFunc("GET /jwks", is.jwks)
	mux.HandleFunc("GET /authorize", is.authorize)
	mux.HandleFunc("POST /token", is.token)
	return mux
}

func (is *issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                is.cfg.issuer,
		"authorization_endpoint":                is.cfg.issuer + "/authorize",
		"token_endpoint":                        is.cfg.issuer + "/token",
		"jwks_uri":                              is.cfg.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "client_credentials"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
	})
}

func (is *issuer) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := is.cfg.signerKey.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": is.cfg.kid,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// authorize auto-approves: it asks for no password and signs in the fixed user.
func (is *issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID, redirect := q.Get("client_id"), q.Get("redirect_uri")
	if _, ok := is.cfg.clients[clientID]; !ok {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	}
	// Only loopback redirects: this issuer must never send codes elsewhere.
	ru, err := url.Parse(redirect)
	if err != nil || ru.Scheme != "http" || !loopback(ru.Hostname()) {
		http.Error(w, "redirect_uri must be an http loopback URL", http.StatusBadRequest)
		return
	}
	back := func(vals url.Values) {
		rq := ru.Query()
		for k, v := range vals {
			rq.Set(k, v[0])
		}
		if s := q.Get("state"); s != "" {
			rq.Set("state", s)
		}
		ru.RawQuery = rq.Encode()
		http.Redirect(w, r, ru.String(), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		back(url.Values{"error": {"unsupported_response_type"}})
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		back(url.Values{"error": {"invalid_request"}, "error_description": {"PKCE S256 is required"}})
		return
	}
	code := randomString()
	is.mu.Lock()
	is.codes[code] = authCode{clientID: clientID, redirectURI: redirect, nonce: q.Get("nonce"),
		challenge: q.Get("code_challenge"), scope: q.Get("scope"), expires: time.Now().Add(is.cfg.codeTTL)}
	is.mu.Unlock()
	back(url.Values{"code": {code}})
}

func (is *issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	clientID, secret, hasBasic := r.BasicAuth()
	if hasBasic { // RFC 6749 2.3.1: credentials are form-urlencoded before base64
		clientID, _ = url.QueryUnescape(clientID)
		secret, _ = url.QueryUnescape(secret)
	} else {
		clientID, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	want, ok := is.cfg.clients[clientID]
	if !ok || subtle.ConstantTimeCompare([]byte(want), []byte(secret)) != 1 {
		tokenError(w, http.StatusUnauthorized, "invalid_client", "unknown client or wrong secret")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		is.codeGrant(w, r, clientID)
	case "client_credentials":
		if want == "" {
			tokenError(w, http.StatusUnauthorized, "invalid_client", "public clients cannot use client_credentials")
			return
		}
		now := time.Now()
		at := is.sign(map[string]any{
			"iss": is.cfg.issuer, "sub": clientID, "aud": is.cfg.audience, "client_id": clientID,
			"scope": r.PostForm.Get("scope"), "iat": now.Unix(), "exp": now.Add(is.cfg.tokenTTL).Unix(),
		})
		writeJSON(w, http.StatusOK, map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": int(is.cfg.tokenTTL.Seconds())})
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (is *issuer) codeGrant(w http.ResponseWriter, r *http.Request, clientID string) {
	code := r.PostForm.Get("code")
	is.mu.Lock()
	c, ok := is.codes[code]
	delete(is.codes, code) // single use
	is.mu.Unlock()
	if !ok || time.Now().After(c.expires) || c.clientID != clientID || c.redirectURI != r.PostForm.Get("redirect_uri") {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "code unknown, expired, or for another client or redirect_uri")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(b64(sum[:])), []byte(c.challenge)) != 1 {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	now := time.Now()
	base := map[string]any{"iss": is.cfg.issuer, "sub": is.cfg.subject, "iat": now.Unix(), "exp": now.Add(is.cfg.tokenTTL).Unix()}
	idc := map[string]any{"aud": clientID, "name": is.cfg.name, "preferred_username": is.cfg.name, "email": is.cfg.email, "email_verified": true}
	if c.nonce != "" {
		idc["nonce"] = c.nonce
	}
	atc := map[string]any{"aud": is.cfg.audience, "client_id": clientID, "scope": c.scope}
	writeJSON(w, http.StatusOK, map[string]any{
		"token_type": "Bearer", "expires_in": int(is.cfg.tokenTTL.Seconds()), "scope": c.scope,
		"id_token":     is.sign(merge(base, idc)),
		"access_token": is.sign(merge(base, atc)),
	})
}

func (is *issuer) sign(claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": is.cfg.kid})
	pl, _ := json.Marshal(claims)
	in := b64(hdr) + "." + b64(pl)
	h := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, is.cfg.signerKey, crypto.SHA256, h[:])
	if err != nil {
		panic(err)
	}
	return in + "." + b64(sig)
}

func merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func loopback(h string) bool {
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b64(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func tokenError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
	if desc != "" {
		fmt.Fprintf(os.Stderr, "devissuer: token error %s: %s\n", code, desc)
	}
}
