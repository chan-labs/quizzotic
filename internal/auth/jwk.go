// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// Key is one verification key from the issuer's JWKS.
type Key struct {
	KID string
	// Alg is the JWK "alg" member; empty when the issuer did not set one.
	Alg string
	// Public is an *rsa.PublicKey, *ecdsa.PublicKey or ed25519.PublicKey.
	Public crypto.PublicKey
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	KTY string `json:"kty"`
	KID string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	CRV string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS decodes a JWKS document into keys by kid. Keys that are not for
// signature verification, lack a kid, or use an unsupported type are skipped;
// a set with no usable key is an error.
func parseJWKS(data []byte) (map[string]*Key, error) {
	var set jwkSet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	keys := make(map[string]*Key, len(set.Keys))
	for _, j := range set.Keys {
		if j.KID == "" || (j.Use != "" && j.Use != "sig") {
			continue
		}
		pub, err := j.public()
		if err != nil {
			continue
		}
		keys[j.KID] = &Key{KID: j.KID, Alg: j.Alg, Public: pub}
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks: no usable signing keys")
	}
	return keys, nil
}

func (j jwk) public() (crypto.PublicKey, error) {
	switch j.KTY {
	case "RSA":
		n, err := b64Int(j.N)
		if err != nil {
			return nil, err
		}
		e, err := b64Int(j.E)
		if err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 || n.BitLen() < 2048 {
			return nil, errors.New("jwks: weak or invalid RSA key")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch j.CRV {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("jwks: unsupported curve %q", j.CRV)
		}
		x, err := base64.RawURLEncoding.DecodeString(j.X)
		if err != nil {
			return nil, errors.New("jwks: bad EC x")
		}
		y, err := base64.RawURLEncoding.DecodeString(j.Y)
		if err != nil {
			return nil, errors.New("jwks: bad EC y")
		}
		size := (curve.Params().BitSize + 7) / 8
		if len(x) > size || len(y) > size {
			return nil, errors.New("jwks: EC coordinate too long")
		}
		// Uncompressed point: 0x04 || X || Y, each left-padded to the field size.
		pt := make([]byte, 1+2*size)
		pt[0] = 4
		copy(pt[1+size-len(x):], x)
		copy(pt[1+2*size-len(y):], y)
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, pt) // rejects points off the curve
		if err != nil {
			return nil, fmt.Errorf("jwks: invalid EC point: %w", err)
		}
		return pub, nil
	case "OKP":
		if j.CRV != "Ed25519" {
			return nil, fmt.Errorf("jwks: unsupported OKP curve %q", j.CRV)
		}
		x, err := base64.RawURLEncoding.DecodeString(j.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("jwks: invalid Ed25519 key")
		}
		return ed25519.PublicKey(x), nil
	}
	return nil, fmt.Errorf("jwks: unsupported key type %q", j.KTY)
}

func b64Int(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("jwks: bad base64url integer")
	}
	return new(big.Int).SetBytes(b), nil
}
