// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Web login defaults.
const (
	DefaultSessionTTL = 30 * 24 * time.Hour
	DefaultStateTTL   = 10 * time.Minute
	// touchInterval throttles sliding-expiry writes: a session is extended at
	// most once per interval, not on every request.
	touchInterval = time.Minute
)

// Errors returned by LoginService.
var (
	// ErrInvalidState means the callback state is unknown, used or expired.
	ErrInvalidState = errors.New("auth: invalid or expired login state")
	// ErrNoSession means the cookie token names no live session.
	ErrNoSession = errors.New("auth: no valid session")
	// ErrExchange means the token endpoint call failed or returned no ID token.
	ErrExchange = errors.New("auth: code exchange failed")
)

// LoginConfig configures the web login flow.
type LoginConfig struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	// PublicURL is the externally visible base URL; the redirect URI is
	// PublicURL + "/auth/callback".
	PublicURL  string
	SessionTTL time.Duration // default 30 days
	StateTTL   time.Duration // default 10 minutes
}

// RedirectURL is the callback URL registered with the issuer.
func (c LoginConfig) RedirectURL() string {
	return strings.TrimRight(c.PublicURL, "/") + "/auth/callback"
}

// Discoverer returns the issuer's discovery document. *Provider implements it.
type Discoverer interface {
	Discovery(ctx context.Context) (*Discovery, error)
}

// IDTokenVerifier verifies ID tokens. *Verifier implements it.
type IDTokenVerifier interface {
	Verify(ctx context.Context, token string, opt VerifyOptions) (*Claims, error)
}

// UserProvisioner resolves verified claims to a user. *Service implements it.
type UserProvisioner interface {
	Provision(ctx context.Context, c ProvisionClaims) (User, error)
}

// LoginService runs the browser login flow and owns web sessions.
type LoginService struct {
	cfg    LoginConfig
	repos  SessionRepos
	idp    Discoverer
	ver    IDTokenVerifier
	users  UserProvisioner
	clock  domain.Clock
	ids    *domain.IDGenerator
	client *http.Client
}

// NewLoginService builds a LoginService. A nil client uses a client with a
// 10 s timeout.
func NewLoginService(cfg LoginConfig, repos SessionRepos, idp Discoverer, ver IDTokenVerifier,
	users UserProvisioner, clock domain.Clock, ids *domain.IDGenerator, client *http.Client) *LoginService {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.StateTTL <= 0 {
		cfg.StateTTL = DefaultStateTTL
	}
	if client == nil {
		client = &http.Client{Timeout: defaultRequestTimeout}
	}
	return &LoginService{cfg: cfg, repos: repos, idp: idp, ver: ver, users: users, clock: clock, ids: ids, client: client}
}

// SessionTTL returns the sliding session lifetime.
func (s *LoginService) SessionTTL() time.Duration { return s.cfg.SessionTTL }

// BeginLogin stores fresh state, nonce and PKCE verifier and returns the
// issuer URL to redirect the browser to. returnTo is kept only when it is a
// local path.
func (s *LoginService) BeginLogin(ctx context.Context, returnTo string) (string, error) {
	disc, err := s.idp.Discovery(ctx)
	if err != nil {
		return "", err
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" {
		return "", errors.New("auth: discovery document lacks authorization or token endpoint")
	}
	state, nonce, verifier := randomToken(), randomToken(), randomToken()

	now := s.clock.Now()
	if err := s.repos.LoginStates().PurgeExpired(ctx, now); err != nil {
		return "", err
	}
	err = s.repos.LoginStates().Create(ctx, LoginState{
		StateHash:    hashToken(state),
		Nonce:        nonce,
		CodeVerifier: verifier,
		ReturnTo:     SafeReturnTo(returnTo),
		ExpiresAt:    now.Add(s.cfg.StateTTL),
	})
	if err != nil {
		return "", err
	}

	u, err := url.Parse(disc.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("auth: bad authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", s.cfg.ClientID)
	q.Set("redirect_uri", s.cfg.RedirectURL())
	q.Set("scope", strings.Join(s.cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// LoginResult is a completed login.
type LoginResult struct {
	// Token is the opaque cookie value; it is never stored.
	Token     string
	Session   WebSession
	ReturnTo  string
	UserID    domain.ID
	ExpiresAt time.Time
}

// CompleteLogin consumes the one-time state, exchanges the code (with the PKCE
// verifier), verifies the ID token, provisions the user and creates a session.
func (s *LoginService) CompleteLogin(ctx context.Context, state, code string) (LoginResult, error) {
	if state == "" || code == "" {
		return LoginResult{}, ErrInvalidState
	}
	now := s.clock.Now()
	st, err := s.repos.LoginStates().Consume(ctx, hashToken(state), now)
	if errors.Is(err, ErrNotFound) {
		return LoginResult{}, ErrInvalidState
	}
	if err != nil {
		return LoginResult{}, err
	}

	disc, err := s.idp.Discovery(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	idToken, err := s.exchange(ctx, disc.TokenEndpoint, code, st.CodeVerifier)
	if err != nil {
		return LoginResult{}, err
	}
	claims, err := s.ver.Verify(ctx, idToken, VerifyOptions{Audience: s.cfg.ClientID, Nonce: st.Nonce})
	if err != nil {
		return LoginResult{}, err
	}
	if claims.Nonce == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(st.Nonce)) != 1 {
		return LoginResult{}, invalid("nonce mismatch")
	}
	user, err := s.users.Provision(ctx, ProvisionClaims{
		Issuer:      claims.Issuer,
		Subject:     claims.Subject,
		DisplayName: firstClaim(claims.Raw, "name", "preferred_username"),
		Email:       firstClaim(claims.Raw, "email"),
	})
	if err != nil {
		return LoginResult{}, err
	}

	token := randomToken()
	ws := WebSession{
		ID:         s.ids.New(),
		UserID:     user.ID,
		TokenHash:  hashToken(token),
		CSRFToken:  randomToken(),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.cfg.SessionTTL),
	}
	if err := s.repos.WebSessions().Create(ctx, ws); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, Session: ws, ReturnTo: st.ReturnTo, UserID: user.ID, ExpiresAt: ws.ExpiresAt}, nil
}

// AbandonLogin discards the state of a login the issuer rejected, so it cannot
// be replayed. Errors are ignored: the state expires on its own.
func (s *LoginService) AbandonLogin(ctx context.Context, state string) {
	if state != "" {
		_, _ = s.repos.LoginStates().Consume(ctx, hashToken(state), s.clock.Now())
	}
}

// exchange trades the authorization code for tokens using client_secret_basic
// and returns the ID token.
func (s *LoginService) exchange(ctx context.Context, endpoint, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {s.cfg.RedirectURL()},
		"client_id":     {s.cfg.ClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if s.cfg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(s.cfg.ClientID), url.QueryEscape(s.cfg.ClientSecret))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExchange, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIdPBody))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrExchange, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: token endpoint status %d", ErrExchange, resp.StatusCode)
	}
	var tr struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.IDToken == "" {
		return "", fmt.Errorf("%w: no id_token in response", ErrExchange)
	}
	return tr.IDToken, nil
}

// Authenticate resolves a cookie token to its live session. It slides the
// expiry (at most once per minute) and reports whether it did, so the caller
// can re-issue the cookie. Expired sessions are deleted.
func (s *LoginService) Authenticate(ctx context.Context, token string) (ws WebSession, refreshed bool, err error) {
	if token == "" {
		return WebSession{}, false, ErrNoSession
	}
	ws, err = s.repos.WebSessions().GetByTokenHash(ctx, hashToken(token))
	if errors.Is(err, ErrNotFound) {
		return WebSession{}, false, ErrNoSession
	}
	if err != nil {
		return WebSession{}, false, err
	}
	now := s.clock.Now()
	if !now.Before(ws.ExpiresAt) {
		if err := s.repos.WebSessions().Delete(ctx, ws.UserID, ws.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return WebSession{}, false, err
		}
		return WebSession{}, false, ErrNoSession
	}
	if now.Sub(ws.LastSeenAt) >= touchInterval {
		exp := now.Add(s.cfg.SessionTTL)
		if err := s.repos.WebSessions().Touch(ctx, ws.UserID, ws.ID, now, exp); err != nil {
			return WebSession{}, false, err
		}
		ws.LastSeenAt, ws.ExpiresAt = now, exp
		refreshed = true
	}
	return ws, refreshed, nil
}

// Logout deletes the session.
func (s *LoginService) Logout(ctx context.Context, ws WebSession) error {
	err := s.repos.WebSessions().Delete(ctx, ws.UserID, ws.ID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// SafeReturnTo returns p when it is a local absolute path, else "/". It
// rejects scheme-relative ("//host"), backslash and control-character forms
// that browsers may treat as another origin.
func SafeReturnTo(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return "/"
	}
	return p
}

func firstClaim(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// randomToken returns 32 random bytes, base64url encoded.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken is the stored form of a bearer-like random value.
func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// pkceChallenge is the S256 code challenge for a verifier (RFC 7636).
func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
