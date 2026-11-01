// SPDX-License-Identifier: Apache-2.0

package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/mcp"
	"github.com/chan-labs/quizzotic/internal/platform"
)

const metadataPath = "/.well-known/oauth-protected-resource"

// WithMCP mounts the MCP server over streamable HTTP at /mcp, protected as an
// OAuth resource: callers present an IdP access token of an agent client.
// publicURL is the externally visible origin, used to point unauthenticated
// callers at the protected-resource metadata.
func WithMCP(publicURL string) HandlerOption {
	return func(o *handlerOptions) { o.mcpURL, o.mcp = strings.TrimRight(publicURL, "/"), true }
}

// mcpLoopback runs requests through the already-built server handler without
// a network hop, so tool calls go through the same authentication, scope
// guard, owner scoping and idempotency as any API request.
type mcpLoopback struct{ h http.Handler }

func (l mcpLoopback) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	l.h.ServeHTTP(rec, r)
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

// mcpHandler returns the /mcp handler. api is the full handler that tool calls
// are sent to; it is read lazily because /mcp is mounted on it.
func mcpHandler(authn auth.Authenticator, publicURL string, api func() http.Handler) (http.Handler, error) {
	inner, err := mcp.NewHTTPHandler(mcp.Options{
		BaseURL: "http://quizzotic.invalid",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return mcpLoopback{api()}.RoundTrip(r)
		})},
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	challenge := `Bearer resource_metadata="` + publicURL + metadataPath + `"`
	deny := func(w http.ResponseWriter, r *http.Request, p *platform.Problem) {
		if p.Status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", challenge)
		}
		platform.WriteError(w, r, p)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
			deny(w, r, platform.Unauthorized("a bearer access token is required"))
			return
		}
		p, err := authn.Authenticate(w, r)
		switch {
		case errors.Is(err, auth.ErrNoCredentials):
			deny(w, r, platform.Unauthorized("a bearer access token is required"))
			return
		case err != nil:
			deny(w, r, platform.Unauthorized("invalid access token"))
			return
		case p.Kind != auth.KindAgent:
			deny(w, r, platform.Forbidden("MCP is for agent clients; this token belongs to a human client"))
			return
		}
		inner.ServeHTTP(w, r.WithContext(mcp.WithToken(r.Context(), fields[1])))
	}), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
