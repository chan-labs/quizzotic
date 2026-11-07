// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxOAuthBody = 1 << 20
	loginTimeout = 5 * time.Minute
	// refreshSkew renews a token this long before it actually expires.
	refreshSkew = 30 * time.Second
	// loginScope asks for a refresh token (offline_access) alongside the
	// OpenID scopes the server's config lists.
	loginScope = "openid profile email offline_access"

	grantDeviceCode = "urn:ietf:params:oauth:grant-type:device_code"
)

var b64url = base64.RawURLEncoding

// discovery is the part of the OIDC discovery document the CLI uses.
type discovery struct {
	Issuer                      string `json:"issuer"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

// tokenResponse is an OAuth token endpoint success body (RFC 6749 5.1).
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// oauthError is an OAuth error response (RFC 6749 5.2).
type oauthError struct {
	Code, Description string
	Status            int
}

func (e *oauthError) Error() string {
	s := "identity provider error: " + e.Code
	if e.Description != "" {
		s += ": " + e.Description
	}
	return s
}

// checkIssuerURL requires https, except for loopback hosts used in
// development and tests.
func checkIssuerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("issuer %q is not a valid URL", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("issuer %q must use https", raw)
}

// isLoopbackHost reports whether h is localhost or a loopback IP
// (127.0.0.0/8, ::1).
func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func getJSON(ctx context.Context, hc *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.Unmarshal(body, out)
}

// discover fetches and validates the issuer's discovery document.
func discover(ctx context.Context, hc *http.Client, issuer string) (*discovery, error) {
	if err := checkIssuerURL(issuer); err != nil {
		return nil, err
	}
	var d discovery
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	if err := getJSON(ctx, hc, u, &d); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if d.Issuer != issuer {
		return nil, fmt.Errorf("oidc discovery: document names issuer %q, expected %q", d.Issuer, issuer)
	}
	if d.TokenEndpoint == "" {
		return nil, errors.New("oidc discovery: no token_endpoint")
	}
	for name, v := range map[string]string{
		"token_endpoint":                d.TokenEndpoint,
		"authorization_endpoint":        d.AuthorizationEndpoint,
		"device_authorization_endpoint": d.DeviceAuthorizationEndpoint,
	} {
		if v != "" {
			if err := checkIssuerURL(v); err != nil {
				return nil, fmt.Errorf("oidc discovery: %s: %w", name, err)
			}
		}
	}
	return &d, nil
}

// postForm posts a form to an IdP endpoint and decodes a JSON success body
// into out, or returns an *oauthError.
func postForm(ctx context.Context, hc *http.Client, endpoint string, form url.Values, basic [2]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic[0] != "" {
		// RFC 6749 2.3.1: id and secret are form-urlencoded before Basic.
		req.SetBasicAuth(url.QueryEscape(basic[0]), url.QueryEscape(basic[1]))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &oauthError{Code: e.Error, Description: e.Description, Status: resp.StatusCode}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("identity provider sent an unreadable response: %w", err)
	}
	return nil
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64url.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64url.EncodeToString(sum[:])
}

// credentialFrom turns a token response into a stored credential.
func credentialFrom(now time.Time, issuer, clientID, tokenEndpoint string, t tokenResponse, prevRefresh string) (Credential, error) {
	if t.AccessToken == "" {
		return Credential{}, errors.New("identity provider returned no access token")
	}
	c := Credential{
		Issuer: issuer, ClientID: clientID, TokenEndpoint: tokenEndpoint,
		AccessToken: t.AccessToken, RefreshToken: t.RefreshToken,
	}
	if c.RefreshToken == "" {
		c.RefreshToken = prevRefresh // the IdP did not rotate it
	}
	exp := t.ExpiresIn
	if exp <= 0 {
		exp = 3600
	}
	c.ExpiresAt = now.Add(time.Duration(exp) * time.Second).UTC()
	return c, nil
}

// refresh exchanges the refresh token for a new access token.
func refresh(ctx context.Context, hc *http.Client, now time.Time, c Credential) (Credential, error) {
	var t tokenResponse
	err := postForm(ctx, hc, c.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
		"client_id":     {c.ClientID},
	}, [2]string{}, &t)
	if err != nil {
		return Credential{}, err
	}
	return credentialFrom(now, c.Issuer, c.ClientID, c.TokenEndpoint, t, c.RefreshToken)
}

// clientCredentials fetches a token for a headless client.
func clientCredentials(ctx context.Context, hc *http.Client, issuer, id, secret string) (string, error) {
	d, err := discover(ctx, hc, issuer)
	if err != nil {
		return "", err
	}
	var t tokenResponse
	if err := postForm(ctx, hc, d.TokenEndpoint, url.Values{"grant_type": {"client_credentials"}},
		[2]string{id, secret}, &t); err != nil {
		return "", err
	}
	if t.AccessToken == "" {
		return "", errors.New("identity provider returned no access token")
	}
	return t.AccessToken, nil
}

// loopbackLogin runs Authorization Code + PKCE with a loopback redirect on a
// random 127.0.0.1 port (RFC 8252 7.3). announce is handed the authorization
// URL (to open a browser and print it); if it fails the login is abandoned.
func loopbackLogin(ctx context.Context, hc *http.Client, d *discovery, clientID string, announce func(authURL string) error) (tokenResponse, error) {
	var zero tokenResponse
	if d.AuthorizationEndpoint == "" {
		return zero, errors.New("the issuer advertises no authorization_endpoint")
	}
	verifier, err := randomString(32)
	if err != nil {
		return zero, err
	}
	state, err := randomString(24)
	if err != nil {
		return zero, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return zero, fmt.Errorf("opening loopback listener: %w", err)
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	type result struct {
		code string
		err  error
	}
	got := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("login callback had an unexpected state; aborting")
		case q.Get("error") != "":
			res.err = fmt.Errorf("login refused: %s %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = errors.New("login callback carried no code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, "<p>Quizzotic login failed. You can close this tab.</p>")
		} else {
			fmt.Fprintln(w, "<p>Quizzotic login complete. You can close this tab.</p>")
		}
		select {
		case got <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"scope":                 {loginScope},
		"state":                 {state},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	if err := announce(d.AuthorizationEndpoint + sep + q.Encode()); err != nil {
		return zero, err
	}

	tctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	var res result
	select {
	case res = <-got:
	case <-tctx.Done():
		return zero, errors.New("timed out waiting for the browser login")
	}
	if res.err != nil {
		return zero, res.err
	}
	var t tokenResponse
	err = postForm(ctx, hc, d.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}, [2]string{}, &t)
	return t, err
}

// deviceLogin runs the device authorization grant (RFC 8628).
func deviceLogin(ctx context.Context, hc *http.Client, d *discovery, clientID string, out io.Writer, sleep func(context.Context, time.Duration) error) (tokenResponse, error) {
	var zero tokenResponse
	if d.DeviceAuthorizationEndpoint == "" {
		return zero, errors.New("the issuer does not advertise device flow (no device_authorization_endpoint)")
	}
	var da struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := postForm(ctx, hc, d.DeviceAuthorizationEndpoint,
		url.Values{"client_id": {clientID}, "scope": {loginScope}}, [2]string{}, &da); err != nil {
		return zero, err
	}
	if da.DeviceCode == "" || da.UserCode == "" {
		return zero, errors.New("identity provider returned an incomplete device authorization")
	}
	if da.VerificationURIComplete != "" {
		fmt.Fprintf(out, "To sign in, open %s\nand confirm the code %s.\n", da.VerificationURIComplete, da.UserCode)
	} else {
		fmt.Fprintf(out, "To sign in, open %s\nand enter the code %s.\n", da.VerificationURI, da.UserCode)
	}
	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	life := time.Duration(da.ExpiresIn) * time.Second
	if life <= 0 {
		life = 15 * time.Minute
	}
	dctx, cancel := context.WithTimeout(ctx, life)
	defer cancel()
	for {
		if err := sleep(dctx, interval); err != nil {
			return zero, errors.New("timed out waiting for the device login")
		}
		var t tokenResponse
		err := postForm(dctx, hc, d.TokenEndpoint, url.Values{
			"grant_type":  {grantDeviceCode},
			"device_code": {da.DeviceCode},
			"client_id":   {clientID},
		}, [2]string{}, &t)
		var oe *oauthError
		switch {
		case err == nil:
			return t, nil
		case errors.As(err, &oe) && oe.Code == "authorization_pending":
		case errors.As(err, &oe) && oe.Code == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &oe) && oe.Code == "expired_token":
			return zero, errors.New("the device code expired; run login again")
		case errors.As(err, &oe) && oe.Code == "access_denied":
			return zero, errors.New("login was denied")
		default:
			return zero, err
		}
	}
}
