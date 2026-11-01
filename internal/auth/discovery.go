// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/chan-labs/quizzotic/internal/platform"
)

// maxIdPBody bounds how much of an IdP response is read.
const maxIdPBody = 1 << 20

// Discovery is the subset of the OIDC discovery document (OpenID Connect
// Discovery 1.0, section 3) that Quizzotic uses.
type Discovery struct {
	Issuer                      string   `json:"issuer"`
	AuthorizationEndpoint       string   `json:"authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	JWKSURI                     string   `json:"jwks_uri"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint,omitempty"`
	EndSessionEndpoint          string   `json:"end_session_endpoint,omitempty"`
	GrantTypesSupported         []string `json:"grant_types_supported,omitempty"`
	IDTokenSigningAlgs          []string `json:"id_token_signing_alg_values_supported,omitempty"`
}

// SupportsDeviceFlow reports whether the issuer advertises the device
// authorization grant, which the CLI and local agents use when available.
func (d *Discovery) SupportsDeviceFlow() bool {
	return d.DeviceAuthorizationEndpoint != ""
}

// DiscoveryURL returns the well-known discovery document URL for issuer.
func DiscoveryURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
}

// fetchDiscovery downloads and validates the discovery document. The issuer in
// the document must equal the configured issuer exactly (OIDC Discovery
// section 4.3), which stops a mix-up where another issuer's metadata is served.
func (p *Provider) fetchDiscovery(ctx context.Context) (*Discovery, error) {
	u := DiscoveryURL(p.issuer)
	ctx, span := platform.StartSpan(ctx, "oidc.discovery", attribute.String("url.full", u))
	defer span.End()

	var d Discovery
	if err := p.getJSON(ctx, u, &d); err != nil {
		platform.RecordError(span, err)
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if d.Issuer != p.issuer {
		err := fmt.Errorf("oidc discovery: issuer mismatch (document says %q, configured %q)", d.Issuer, p.issuer)
		platform.RecordError(span, err)
		return nil, err
	}
	if !isHTTPURL(d.JWKSURI) {
		err := errors.New("oidc discovery: jwks_uri missing or not an absolute http(s) URL")
		platform.RecordError(span, err)
		return nil, err
	}
	return &d, nil
}

// getJSON GETs u and decodes a 200 JSON body into v.
func (p *Provider) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxIdPBody))
		return fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxIdPBody)).Decode(v); err != nil {
		return fmt.Errorf("GET %s: decode: %w", u, err)
	}
	return nil
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
