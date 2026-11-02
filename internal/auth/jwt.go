// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

// ClockSkew is the leeway applied to exp and nbf.
const ClockSkew = 60 * time.Second

// ErrInvalidToken wraps every token verification failure.
var ErrInvalidToken = errors.New("auth: invalid token")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidToken, fmt.Sprintf(format, a...))
}

// KeySource resolves a signing key by kid. *Provider implements it.
type KeySource interface {
	Key(ctx context.Context, kid string) (*Key, error)
}

// Verifier verifies JWTs from one issuer using only the standard library.
type Verifier struct {
	issuer string
	keys   KeySource
	now    func() time.Time
}

// VerifierOption configures a Verifier.
type VerifierOption func(*Verifier)

// WithVerifierClock overrides the clock (tests).
func WithVerifierClock(now func() time.Time) VerifierOption {
	return func(v *Verifier) { v.now = now }
}

// NewVerifier returns a Verifier that requires iss to equal issuer.
func NewVerifier(issuer string, keys KeySource, opts ...VerifierOption) *Verifier {
	v := &Verifier{issuer: issuer, keys: keys, now: time.Now}
	for _, o := range opts {
		o(v)
	}
	return v
}

// VerifyOptions are the per-call expectations.
type VerifyOptions struct {
	// Audiences lists the acceptable audiences; the token's aud claim (a
	// string or an array) must contain at least one of them. Required, and
	// no entry may be empty.
	Audiences []string
	// Nonce, when non-empty, must equal the token's nonce claim (ID tokens).
	Nonce string
}

// Claims are the verified token claims.
type Claims struct {
	Issuer    string
	Subject   string
	Audience  []string
	Expiry    time.Time
	NotBefore time.Time // zero when absent
	Nonce     string
	// Raw holds every claim, for callers that need client_id, scope, etc.
	Raw map[string]any
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// Verify checks the signature and the iss, aud, exp, nbf and (optionally)
// nonce claims of a compact JWS.
func (v *Verifier) Verify(ctx context.Context, token string, opt VerifyOptions) (*Claims, error) {
	if len(opt.Audiences) == 0 {
		return nil, errors.New("auth: VerifyOptions.Audiences is required")
	}
	for _, a := range opt.Audiences {
		if a == "" {
			return nil, errors.New("auth: VerifyOptions.Audiences must not contain an empty value")
		}
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, invalid("malformed")
	}
	var hdr jwtHeader
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return nil, invalid("bad header")
	}
	switch hdr.Alg {
	case "RS256", "ES256", "EdDSA":
	default: // includes "none", HS*, empty
		return nil, invalid("unsupported alg %q", hdr.Alg)
	}
	if hdr.Kid == "" {
		return nil, invalid("missing kid")
	}
	key, err := v.keys.Key(ctx, hdr.Kid)
	if err != nil {
		if errors.Is(err, ErrUnknownKey) {
			return nil, invalid("unknown kid")
		}
		return nil, err
	}
	// The algorithm is dictated by the key type, never by the token alone.
	want, err := algForKey(key.Public)
	if err != nil {
		return nil, invalid("%v", err)
	}
	if hdr.Alg != want || (key.Alg != "" && key.Alg != want) {
		return nil, invalid("alg %q does not match key", hdr.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, invalid("bad signature encoding")
	}
	if err := verifySig(key.Public, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, invalid("bad signature")
	}

	var raw map[string]any
	if err := decodeSegment(parts[1], &raw); err != nil {
		return nil, invalid("bad payload")
	}
	return v.checkClaims(raw, opt)
}

func algForKey(pub crypto.PublicKey) (string, error) {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return "RS256", nil
	case *ecdsa.PublicKey:
		if k.Curve == elliptic.P256() {
			return "ES256", nil
		}
		return "", errors.New("unsupported EC curve")
	case ed25519.PublicKey:
		return "EdDSA", nil
	}
	return "", errors.New("unsupported key type")
}

func verifySig(pub crypto.PublicKey, signed, sig []byte) error {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		h := sha256.Sum256(signed)
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, h[:], sig)
	case *ecdsa.PublicKey:
		if len(sig) != 64 { // JWS ES256 is raw r||s
			return errors.New("bad ES256 signature length")
		}
		h := sha256.Sum256(signed)
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(k, h[:], r, s) {
			return errors.New("ecdsa verify failed")
		}
		return nil
	case ed25519.PublicKey:
		if !ed25519.Verify(k, signed, sig) {
			return errors.New("ed25519 verify failed")
		}
		return nil
	}
	return errors.New("unsupported key type")
}

func (v *Verifier) checkClaims(raw map[string]any, opt VerifyOptions) (*Claims, error) {
	c := &Claims{Raw: raw}
	c.Issuer, _ = raw["iss"].(string)
	c.Subject, _ = raw["sub"].(string)
	c.Nonce, _ = raw["nonce"].(string)
	if c.Issuer == "" || c.Issuer != v.issuer {
		return nil, invalid("issuer mismatch")
	}
	switch a := raw["aud"].(type) {
	case string:
		c.Audience = []string{a}
	case []any:
		for _, e := range a {
			if s, ok := e.(string); ok {
				c.Audience = append(c.Audience, s)
			}
		}
	}
	found := false
	for _, a := range c.Audience {
		if slices.Contains(opt.Audiences, a) {
			found = true
		}
	}
	if !found {
		return nil, invalid("audience mismatch")
	}
	now := v.now()
	exp, ok := numericDate(raw["exp"])
	if !ok {
		return nil, invalid("missing exp")
	}
	c.Expiry = exp
	if !now.Before(exp.Add(ClockSkew)) {
		return nil, invalid("expired")
	}
	if rawNBF, present := raw["nbf"]; present {
		nbf, ok := numericDate(rawNBF)
		if !ok {
			return nil, invalid("bad nbf")
		}
		c.NotBefore = nbf
		if now.Add(ClockSkew).Before(nbf) {
			return nil, invalid("not yet valid")
		}
	}
	if opt.Nonce != "" && c.Nonce != opt.Nonce {
		return nil, invalid("nonce mismatch")
	}
	return c, nil
}

func numericDate(v any) (time.Time, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || f > 1<<40 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

func decodeSegment(seg string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
