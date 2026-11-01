// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type tokenKey struct{}

// WithToken returns a context carrying the caller's bearer token. The HTTP
// handler built by NewHTTPHandler sends it on every tool call, so a call acts
// with the caller's own identity and scopes.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// ContextToken is the Options.Token for a server mounted over HTTP: it returns
// the token stored by WithToken.
func ContextToken(ctx context.Context) (string, error) {
	tok, _ := ctx.Value(tokenKey{}).(string)
	return tok, nil
}

// NewHTTPHandler returns a streamable HTTP handler serving the same tool set
// as the stdio server. Sessions are stateless: the caller authenticates every
// request (see WithToken) and nothing is shared between callers.
// Options.Token defaults to ContextToken.
func NewHTTPHandler(o Options) (http.Handler, error) {
	if o.Token == nil {
		o.Token = ContextToken
	}
	srv, err := New(o)
	if err != nil {
		return nil, err
	}
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}), nil
}
