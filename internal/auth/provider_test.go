// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/platform"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWK(t *testing.T, kid string) map[string]string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": b64(k.N.Bytes()), "e": b64([]byte{1, 0, 1})}
}

// fakeIdP is an httptest OIDC issuer whose key set can change between calls.
type fakeIdP struct {
	srv *httptest.Server

	mu    sync.Mutex
	keys  []map[string]string
	down  bool
	iss   string // issuer reported in discovery; defaults to srv.URL
	nDisc atomic.Int32
	nJWKS atomic.Int32
}

func newFakeIdP(t *testing.T, keys ...map[string]string) *fakeIdP {
	t.Helper()
	f := &fakeIdP{keys: keys}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			f.nDisc.Add(1)
			iss := f.iss
			if iss == "" {
				iss = f.srv.URL
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": iss, "authorization_endpoint": f.srv.URL + "/authorize",
				"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
				"device_authorization_endpoint": f.srv.URL + "/device",
			})
		case "/jwks":
			f.nJWKS.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": f.keys})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) set(keys ...map[string]string) {
	f.mu.Lock()
	f.keys = keys
	f.mu.Unlock()
}

func (f *fakeIdP) setDown(d bool) {
	f.mu.Lock()
	f.down = d
	f.mu.Unlock()
}

// clock is a manually advanced time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newProvider(f *fakeIdP, c *clock) *auth.Provider {
	return auth.NewProvider(f.srv.URL,
		auth.WithClock(c.now), auth.WithJWKSTTL(time.Hour), auth.WithMinRefresh(time.Minute))
}

func TestDiscovery(t *testing.T) {
	f := newFakeIdP(t, rsaJWK(t, "a"))
	p := auth.NewProvider(f.srv.URL)
	d, err := p.Discovery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Issuer != f.srv.URL || d.TokenEndpoint != f.srv.URL+"/token" || d.JWKSURI != f.srv.URL+"/jwks" {
		t.Errorf("discovery = %+v", d)
	}
	if !d.SupportsDeviceFlow() {
		t.Error("device flow should be advertised")
	}
	if _, err := p.Discovery(context.Background()); err != nil || f.nDisc.Load() != 1 {
		t.Errorf("discovery should be cached (fetches=%d, err=%v)", f.nDisc.Load(), err)
	}
}

func TestDiscoveryIssuerMismatchRejected(t *testing.T) {
	f := newFakeIdP(t, rsaJWK(t, "a"))
	f.iss = "https://evil.example.com"
	p := auth.NewProvider(f.srv.URL)
	if _, err := p.Key(context.Background(), "a"); err == nil {
		t.Fatal("want error for issuer mismatch")
	}
	if err := p.Ready(context.Background()); err == nil {
		t.Fatal("must not be ready after issuer mismatch")
	}
	if f.nJWKS.Load() != 0 {
		t.Error("JWKS must not be fetched from an unverified discovery document")
	}
}

func TestKeyRotation(t *testing.T) {
	ctx := context.Background()
	old := rsaJWK(t, "old")
	f := newFakeIdP(t, old)
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	p := newProvider(f, c)

	if k, err := p.Key(ctx, "old"); err != nil || k.KID != "old" || k.Alg != "RS256" {
		t.Fatalf("old key: %v %+v", err, k)
	}
	if _, ok := mustKey(t, p, "old").Public.(*rsa.PublicKey); !ok {
		t.Error("want *rsa.PublicKey")
	}
	if f.nJWKS.Load() != 1 {
		t.Fatalf("JWKS fetches = %d, want 1 (cache hit on second lookup)", f.nJWKS.Load())
	}

	// The IdP rotates: publishes "new" alongside "old". A token with the new
	// kid arrives after the rate-limit window, so the cache refetches.
	f.set(old, rsaJWK(t, "new"))
	c.advance(2 * time.Minute)
	if _, err := p.Key(ctx, "new"); err != nil {
		t.Fatalf("new kid after rotation: %v", err)
	}
	if f.nJWKS.Load() != 2 {
		t.Errorf("JWKS fetches = %d, want 2", f.nJWKS.Load())
	}
	if f.nDisc.Load() != 1 {
		t.Errorf("discovery fetches = %d, want 1 (cached)", f.nDisc.Load())
	}

	// The old key is retired. It stays cached until the TTL refresh drops it.
	f.set(rsaJWK(t, "new2"))
	c.advance(2 * time.Minute)
	if _, err := p.Key(ctx, "old"); err != nil {
		t.Fatalf("old key still cached within TTL: %v", err)
	}
	c.advance(time.Hour)
	if _, err := p.Key(ctx, "old"); !errors.Is(err, auth.ErrUnknownKey) {
		t.Fatalf("retired key after TTL refresh: err=%v, want ErrUnknownKey", err)
	}
	if _, err := p.Key(ctx, "new2"); err != nil {
		t.Fatalf("new2: %v", err)
	}
}

func mustKey(t *testing.T, p *auth.Provider, kid string) *auth.Key {
	t.Helper()
	k, err := p.Key(context.Background(), kid)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestUnknownKidRefreshIsRateLimited(t *testing.T) {
	ctx := context.Background()
	f := newFakeIdP(t, rsaJWK(t, "a"))
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	p := newProvider(f, c)
	mustKey(t, p, "a")

	// Right after the load, forged kids do not trigger refetches.
	for range 20 {
		if _, err := p.Key(ctx, "forged"); !errors.Is(err, auth.ErrUnknownKey) {
			t.Fatalf("err = %v", err)
		}
	}
	if got := f.nJWKS.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1 inside the rate-limit window", got)
	}

	// After the window, exactly one refetch no matter how many lookups.
	c.advance(time.Minute)
	for range 20 {
		_, _ = p.Key(ctx, "forged")
	}
	if got := f.nJWKS.Load(); got != 2 {
		t.Fatalf("JWKS fetches = %d, want 2", got)
	}
}

func TestTTLRefreshAndStaleServing(t *testing.T) {
	ctx := context.Background()
	f := newFakeIdP(t, rsaJWK(t, "a"))
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	p := newProvider(f, c)
	mustKey(t, p, "a")

	f.setDown(true)
	c.advance(2 * time.Hour) // past the TTL, IdP is down
	if _, err := p.Key(ctx, "a"); err != nil {
		t.Fatalf("stale key must keep working while the IdP is down: %v", err)
	}
	if err := p.Ready(ctx); err != nil {
		t.Fatalf("already-loaded JWKS stays ready: %v", err)
	}

	f.setDown(false)
	c.advance(2 * time.Minute) // attempt gap elapsed
	before := f.nJWKS.Load()
	mustKey(t, p, "a")
	if f.nJWKS.Load() != before+1 {
		t.Errorf("expected a TTL refresh once the IdP is back")
	}
	mustKey(t, p, "a")
	if f.nJWKS.Load() != before+1 {
		t.Errorf("fresh cache must not refetch")
	}
}

func TestConcurrentLookupsFetchOnce(t *testing.T) {
	f := newFakeIdP(t, rsaJWK(t, "a"))
	p := auth.NewProvider(f.srv.URL)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Key(context.Background(), "a"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.nJWKS.Load() != 1 || f.nDisc.Load() != 1 {
		t.Errorf("fetches: jwks=%d disc=%d, want 1 each", f.nJWKS.Load(), f.nDisc.Load())
	}
}

func TestReadinessCheck(t *testing.T) {
	f := newFakeIdP(t, rsaJWK(t, "a"))
	f.setDown(true)
	p := auth.NewProvider(f.srv.URL)

	health := platform.NewHealth(slog.New(slog.NewTextHandler(io.Discard, nil)))
	health.AddCheck("jwks", p.Ready)
	mux := http.NewServeMux()
	health.Register(mux)

	get := func() (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code, rec.Body.String()
	}

	if code, body := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("IdP down: /readyz = %d %s, want 503", code, body)
	}
	f.setDown(false)
	code, body := get()
	if code != http.StatusOK {
		t.Fatalf("IdP up: /readyz = %d %s, want 200", code, body)
	}
	var out struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Checks["jwks"] != "ok" {
		t.Errorf("body = %s", body)
	}
	// Loaded keys keep readiness green through a later outage.
	f.setDown(true)
	if code, _ := get(); code != http.StatusOK {
		t.Errorf("after load, outage /readyz = %d, want 200", code)
	}
}

func TestKeyTypesAndSkips(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	f := newFakeIdP(t,
		map[string]string{"kty": "EC", "kid": "ec", "crv": "P-256", "x": b64(ec.X.Bytes()), "y": b64(ec.Y.Bytes())},
		map[string]string{"kty": "OKP", "kid": "ed", "crv": "Ed25519", "x": b64(edPub)},
		map[string]string{"kty": "RSA", "kid": "enc", "use": "enc", "n": "AQAB", "e": "AQAB"},
		map[string]string{"kty": "EC", "kid": "bad", "crv": "P-256", "x": "AQ", "y": "AQ"}, // not on curve
		map[string]string{"kty": "oct", "kid": "sym", "k": "c2VjcmV0"},
		map[string]string{"kty": "OKP", "crv": "Ed25519", "x": b64(edPub)}, // no kid
	)
	p := auth.NewProvider(f.srv.URL)
	ctx := context.Background()
	if k, err := p.Key(ctx, "ec"); err != nil {
		t.Fatal(err)
	} else if _, ok := k.Public.(*ecdsa.PublicKey); !ok {
		t.Errorf("ec key is %T", k.Public)
	}
	if k, err := p.Key(ctx, "ed"); err != nil {
		t.Fatal(err)
	} else if _, ok := k.Public.(ed25519.PublicKey); !ok {
		t.Errorf("ed key is %T", k.Public)
	}
	for _, kid := range []string{"enc", "bad", "sym"} {
		if _, err := p.Key(ctx, kid); !errors.Is(err, auth.ErrUnknownKey) {
			t.Errorf("%s: err = %v, want ErrUnknownKey", kid, err)
		}
	}
}

func TestEmptyJWKSNotReady(t *testing.T) {
	f := newFakeIdP(t)
	if err := auth.NewProvider(f.srv.URL).Ready(context.Background()); err == nil {
		t.Fatal("a JWKS without usable keys must not count as loaded")
	}
}

func TestIdPCallsAreChildSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	f := newFakeIdP(t, rsaJWK(t, "a"))
	ctx, parent := tp.Tracer("test").Start(context.Background(), "request")
	if _, err := auth.NewProvider(f.srv.URL).Key(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	parent.End()

	got := map[string]bool{}
	for _, s := range rec.Ended() {
		if s.Name() == "request" {
			continue
		}
		if s.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("span %q is not a child of the request span", s.Name())
		}
		got[s.Name()] = true
	}
	if !got["oidc.discovery"] || !got["oidc.jwks"] {
		t.Errorf("spans = %v, want oidc.discovery and oidc.jwks", got)
	}
}
