// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type staticKeys map[string]*Key

func (s staticKeys) Key(_ context.Context, kid string) (*Key, error) {
	if k, ok := s[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKey
}

var jwtNow = time.Unix(1_800_000_000, 0)

const (
	jwtIss = "https://idp.example.com"
	jwtAud = "quizzotic"
)

type signer struct {
	alg  string
	priv crypto.Signer
}

func (s signer) sign(t *testing.T, data string) []byte {
	t.Helper()
	switch s.alg {
	case "RS256":
		h := sha256.Sum256([]byte(data))
		sig, err := rsa.SignPKCS1v15(rand.Reader, s.priv.(*rsa.PrivateKey), crypto.SHA256, h[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	case "ES256":
		h := sha256.Sum256([]byte(data))
		r, ss, err := ecdsa.Sign(rand.Reader, s.priv.(*ecdsa.PrivateKey), h[:])
		if err != nil {
			t.Fatal(err)
		}
		out := make([]byte, 64)
		r.FillBytes(out[:32])
		ss.FillBytes(out[32:])
		return out
	default: // EdDSA
		sig, err := s.priv.Sign(nil, []byte(data), crypto.Hash(0))
		if err != nil {
			t.Fatal(err)
		}
		return sig
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s signer) token(t *testing.T, hdr map[string]any, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(claims)
	data := b64(hb) + "." + b64(cb)
	return data + "." + b64(s.sign(t, data))
}

func goodClaims() map[string]any {
	return map[string]any{
		"iss": jwtIss, "sub": "u1", "aud": jwtAud,
		"exp": jwtNow.Add(time.Hour).Unix(), "nbf": jwtNow.Add(-time.Minute).Unix(),
		"nonce": "n-1",
	}
}

func newSigners(t *testing.T) (map[string]signer, staticKeys) {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signers := map[string]signer{
		"RS256": {"RS256", rk}, "ES256": {"ES256", ek}, "EdDSA": {"EdDSA", edk},
	}
	keys := staticKeys{
		"RS256": {KID: "RS256", Public: &rk.PublicKey},
		"ES256": {KID: "ES256", Public: &ek.PublicKey},
		"EdDSA": {KID: "EdDSA", Public: edk.Public()},
	}
	return signers, keys
}

func newTestVerifier(keys KeySource) *Verifier {
	return NewVerifier(jwtIss, keys, WithVerifierClock(func() time.Time { return jwtNow }))
}

func TestVerifyAlgorithms(t *testing.T) {
	signers, keys := newSigners(t)
	v := newTestVerifier(keys)
	for alg, s := range signers {
		t.Run(alg, func(t *testing.T) {
			tok := s.token(t, map[string]any{"alg": alg, "kid": alg}, goodClaims())
			c, err := v.Verify(context.Background(), tok, VerifyOptions{Audiences: []string{jwtAud}, Nonce: "n-1"})
			if err != nil {
				t.Fatal(err)
			}
			if c.Subject != "u1" || c.Nonce != "n-1" {
				t.Fatalf("claims = %+v", c)
			}
		})
	}
}

func TestVerifyClaimFailures(t *testing.T) {
	signers, keys := newSigners(t)
	v := newTestVerifier(keys)
	s := signers["ES256"]
	hdr := map[string]any{"alg": "ES256", "kid": "ES256"}
	mut := func(f func(m map[string]any)) map[string]any {
		m := goodClaims()
		f(m)
		return m
	}
	cases := []struct {
		name   string
		claims map[string]any
		opt    VerifyOptions
		ok     bool
	}{
		{"wrong iss", mut(func(m map[string]any) { m["iss"] = "https://evil.example.com" }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"missing iss", mut(func(m map[string]any) { delete(m, "iss") }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"wrong aud", mut(func(m map[string]any) { m["aud"] = "other" }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"missing aud", mut(func(m map[string]any) { delete(m, "aud") }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"aud array match", mut(func(m map[string]any) { m["aud"] = []string{"x", jwtAud} }), VerifyOptions{Audiences: []string{jwtAud}}, true},
		{"aud array miss", mut(func(m map[string]any) { m["aud"] = []string{"x", "y"} }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"any listed aud matches", mut(func(m map[string]any) { m["aud"] = "web" }), VerifyOptions{Audiences: []string{"cli", "web"}}, true},
		{"aud array any listed matches", mut(func(m map[string]any) { m["aud"] = []string{"x", "cli"} }), VerifyOptions{Audiences: []string{"cli", "web"}}, true},
		{"no listed aud matches", mut(func(m map[string]any) { m["aud"] = []string{"x", "y"} }), VerifyOptions{Audiences: []string{"cli", "web"}}, false},
		{"expired", mut(func(m map[string]any) { m["exp"] = jwtNow.Add(-2 * time.Minute).Unix() }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"expired within skew", mut(func(m map[string]any) { m["exp"] = jwtNow.Add(-30 * time.Second).Unix() }), VerifyOptions{Audiences: []string{jwtAud}}, true},
		{"missing exp", mut(func(m map[string]any) { delete(m, "exp") }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"nbf in future", mut(func(m map[string]any) { m["nbf"] = jwtNow.Add(2 * time.Minute).Unix() }), VerifyOptions{Audiences: []string{jwtAud}}, false},
		{"nbf within skew", mut(func(m map[string]any) { m["nbf"] = jwtNow.Add(30 * time.Second).Unix() }), VerifyOptions{Audiences: []string{jwtAud}}, true},
		{"nonce mismatch", goodClaims(), VerifyOptions{Audiences: []string{jwtAud}, Nonce: "other"}, false},
		{"nonce missing", mut(func(m map[string]any) { delete(m, "nonce") }), VerifyOptions{Audiences: []string{jwtAud}, Nonce: "n-1"}, false},
		{"nonce not checked", mut(func(m map[string]any) { delete(m, "nonce") }), VerifyOptions{Audiences: []string{jwtAud}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Verify(context.Background(), s.token(t, hdr, tc.claims), tc.opt)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyRejectsAlgNone(t *testing.T) {
	_, keys := newSigners(t)
	v := newTestVerifier(keys)
	for _, alg := range []string{"none", "None", "NONE", "", "HS256"} {
		hb, _ := json.Marshal(map[string]any{"alg": alg, "kid": "ES256"})
		cb, _ := json.Marshal(goodClaims())
		for _, sig := range []string{"", b64([]byte("x"))} {
			tok := b64(hb) + "." + b64(cb) + "." + sig
			if _, err := v.Verify(context.Background(), tok, VerifyOptions{Audiences: []string{jwtAud}}); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("alg %q sig %q: err = %v", alg, sig, err)
			}
		}
	}
}

func TestVerifyAlgPinnedToKeyType(t *testing.T) {
	signers, keys := newSigners(t)
	v := newTestVerifier(keys)
	for _, tc := range []struct{ signer, kid, alg string }{
		{"ES256", "ES256", "RS256"},
		{"ES256", "ES256", "EdDSA"},
		{"RS256", "ES256", "RS256"},
		{"RS256", "EdDSA", "RS256"},
		{"EdDSA", "RS256", "EdDSA"},
		{"RS256", "RS256", "ES256"},
	} {
		tok := signers[tc.signer].token(t, map[string]any{"alg": tc.alg, "kid": tc.kid}, goodClaims())
		if _, err := v.Verify(context.Background(), tok, VerifyOptions{Audiences: []string{jwtAud}}); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("%+v: err = %v", tc, err)
		}
	}
}

func TestVerifyJWKAlgMustMatch(t *testing.T) {
	signers, keys := newSigners(t)
	keys["ES256"].Alg = "RS256" // JWKS advertises a different alg than the key type
	v := newTestVerifier(keys)
	tok := signers["ES256"].token(t, map[string]any{"alg": "ES256", "kid": "ES256"}, goodClaims())
	if _, err := v.Verify(context.Background(), tok, VerifyOptions{Audiences: []string{jwtAud}}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyBadInputs(t *testing.T) {
	signers, keys := newSigners(t)
	v := newTestVerifier(keys)
	s := signers["RS256"]
	hdr := map[string]any{"alg": "RS256", "kid": "RS256"}
	good := s.token(t, hdr, goodClaims())
	parts := strings.Split(good, ".")

	cb, _ := json.Marshal(map[string]any{"iss": jwtIss, "aud": jwtAud, "exp": jwtNow.Add(time.Hour).Unix(), "sub": "admin"})
	tampered := parts[0] + "." + b64(cb) + "." + parts[2]

	for name, tok := range map[string]string{
		"empty":       "",
		"two parts":   "a.b",
		"bad header":  "!!!." + parts[1] + "." + parts[2],
		"tampered":    tampered,
		"unknown kid": s.token(t, map[string]any{"alg": "RS256", "kid": "nope"}, goodClaims()),
		"no kid":      s.token(t, map[string]any{"alg": "RS256"}, goodClaims()),
		"bad sig":     parts[0] + "." + parts[1] + ".AAAA",
	} {
		if _, err := v.Verify(context.Background(), tok, VerifyOptions{Audiences: []string{jwtAud}}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := v.Verify(context.Background(), good, VerifyOptions{Audiences: []string{}}); err == nil {
		t.Error("empty audience option must be refused")
	}
	if _, err := v.Verify(context.Background(), good, VerifyOptions{Audiences: []string{jwtAud, ""}}); err == nil {
		t.Error("an empty listed audience must be refused")
	}
}
