// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/chan-labs/quizzotic/internal/platform"
)

// Defaults for the JWKS cache.
const (
	DefaultJWKSTTL        = time.Hour
	DefaultMinRefresh     = time.Minute
	defaultRequestTimeout = 10 * time.Second
)

// ErrUnknownKey means no key with the requested kid is published by the issuer.
var ErrUnknownKey = errors.New("auth: unknown signing key id")

// Provider fetches the issuer's discovery document and keeps a cache of its
// signing keys. The cache is refreshed when its TTL lapses and when a token
// names a kid it does not hold; unknown-kid refreshes are rate-limited so a
// stream of forged kids cannot turn Quizzotic into a load generator for the IdP.
// While the IdP is unreachable, previously loaded keys keep being served.
type Provider struct {
	issuer     string
	client     *http.Client
	ttl        time.Duration
	minRefresh time.Duration
	now        func() time.Time

	fetchMu sync.Mutex // serializes IdP fetches

	mu          sync.RWMutex
	disc        *Discovery
	keys        map[string]*Key
	loadedAt    time.Time // last successful JWKS load
	lastAttempt time.Time // last fetch attempt, successful or not
}

// Option configures a Provider.
type Option func(*Provider)

// WithHTTPClient sets the client used for IdP calls.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.client = c } }

// WithJWKSTTL sets how long loaded keys are trusted before a refresh.
func WithJWKSTTL(d time.Duration) Option { return func(p *Provider) { p.ttl = d } }

// WithMinRefresh sets the minimum gap between refreshes triggered by an
// unknown kid or by TTL expiry once keys are loaded.
func WithMinRefresh(d time.Duration) Option { return func(p *Provider) { p.minRefresh = d } }

// WithClock replaces time.Now (for tests).
func WithClock(now func() time.Time) Option { return func(p *Provider) { p.now = now } }

// NewProvider returns a Provider for issuer. It does no I/O; keys load on the
// first Key, Discovery or Ready call.
func NewProvider(issuer string, opts ...Option) *Provider {
	p := &Provider{
		issuer:     issuer,
		client:     &http.Client{Timeout: defaultRequestTimeout},
		ttl:        DefaultJWKSTTL,
		minRefresh: DefaultMinRefresh,
		now:        time.Now,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Issuer returns the configured issuer URL.
func (p *Provider) Issuer() string { return p.issuer }

// Discovery returns the issuer's discovery document, fetching it if needed.
func (p *Provider) Discovery(ctx context.Context) (*Discovery, error) {
	p.mu.RLock()
	d := p.disc
	p.mu.RUnlock()
	if d != nil {
		return d, nil
	}
	if err := p.refresh(ctx, refreshLoad); err != nil {
		return nil, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.disc, nil
}

// Key returns the verification key for kid, refreshing the cache when the TTL
// has lapsed or the kid is unknown (rate-limited). It returns ErrUnknownKey
// when the issuer does not publish the kid.
func (p *Provider) Key(ctx context.Context, kid string) (*Key, error) {
	keys, loadedAt := p.snapshot()
	switch {
	case keys == nil:
		if err := p.refresh(ctx, refreshLoad); err != nil {
			return nil, err
		}
	case p.now().Sub(loadedAt) >= p.ttl:
		// A failed TTL refresh keeps serving the stale set: an IdP outage
		// should not log everyone out while tokens remain valid.
		_ = p.refresh(ctx, refreshTTL)
	default:
		if k, ok := keys[kid]; ok {
			return k, nil
		}
		_ = p.refresh(ctx, refreshUnknownKid)
	}
	keys, _ = p.snapshot()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKey
}

// Ready is the /readyz check: it succeeds once the JWKS has been loaded,
// attempting a load if none has happened yet.
func (p *Provider) Ready(ctx context.Context) error {
	if keys, _ := p.snapshot(); keys != nil {
		return nil
	}
	return p.refresh(ctx, refreshLoad)
}

func (p *Provider) snapshot() (map[string]*Key, time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.keys, p.loadedAt
}

type refreshReason int

const (
	refreshLoad       refreshReason = iota // nothing loaded yet: always try
	refreshTTL                             // cache expired
	refreshUnknownKid                      // token named an unseen kid
)

// refresh fetches discovery (when not cached) and the JWKS. Concurrent callers
// queue on fetchMu; those that find the cache already renewed return at once.
func (p *Provider) refresh(ctx context.Context, reason refreshReason) error {
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()

	now := p.now()
	p.mu.RLock()
	loaded := p.keys != nil
	fresh := loaded && now.Sub(p.loadedAt) < p.ttl
	sinceAttempt := now.Sub(p.lastAttempt)
	p.mu.RUnlock()

	switch reason {
	case refreshLoad:
		if loaded {
			return nil
		}
	case refreshTTL:
		if fresh || sinceAttempt < p.minRefresh {
			return nil
		}
	case refreshUnknownKid:
		if sinceAttempt < p.minRefresh {
			return nil
		}
	}

	p.mu.Lock()
	p.lastAttempt = now
	p.mu.Unlock()

	p.mu.RLock()
	disc := p.disc
	p.mu.RUnlock()
	if disc == nil {
		d, err := p.fetchDiscovery(ctx)
		if err != nil {
			return err
		}
		disc = d
	}
	keys, err := p.fetchJWKS(ctx, disc.JWKSURI)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.disc, p.keys, p.loadedAt = disc, keys, p.now()
	p.mu.Unlock()
	return nil
}

func (p *Provider) fetchJWKS(ctx context.Context, uri string) (map[string]*Key, error) {
	ctx, span := platform.StartSpan(ctx, "oidc.jwks", attribute.String("url.full", uri))
	defer span.End()

	var raw rawJSON
	if err := p.getJSON(ctx, uri, &raw); err != nil {
		platform.RecordError(span, err)
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	keys, err := parseJWKS(raw)
	if err != nil {
		platform.RecordError(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("oidc.jwks.keys", len(keys)))
	return keys, nil
}

// rawJSON keeps the undecoded JSON so parseJWKS owns the schema.
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}
