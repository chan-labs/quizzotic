// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func mcpHTTPSession(t *testing.T, e *probEnv, token string) *sdk.ClientSession {
	t.Helper()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(),
		&sdk.StreamableClientTransport{
			Endpoint:   e.srv.URL + "/mcp",
			HTTPClient: &http.Client{Transport: bearerTransport{token}},
		}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *sdk.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func postMCP(t *testing.T, e *probEnv, token string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("POST", e.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const wantChallenge = `Bearer resource_metadata="https://quizzotic.example.com/.well-known/oauth-protected-resource"`

// TestMCPHTTPAuth: /mcp is an OAuth-protected resource for agent clients only.
func TestMCPHTTPAuth(t *testing.T) {
	e := newProbEnv(t)

	for name, tok := range map[string]string{"no token": "", "garbage token": "not-a-jwt"} {
		resp, body := postMCP(t, e, tok)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d (%s)", name, resp.StatusCode, body)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got != wantChallenge {
			t.Errorf("%s: WWW-Authenticate = %q", name, got)
		}
	}

	resp, body := postMCP(t, e, e.human)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("human token: status = %d (%s)", resp.StatusCode, body)
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("403 carries a challenge: %q", resp.Header.Get("WWW-Authenticate"))
	}

	resp, body = postMCP(t, e, e.agent)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("agent token: status = %d (%s)", resp.StatusCode, body)
	}
}

// TestMCPHTTPTools: over HTTP an agent sees the stdio tool set and acts with
// its own token, under its owner's scope.
func TestMCPHTTPTools(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()
	cs := mcpHTTPSession(t, e, e.agent)

	stdio := toolNames(t, mcpSession(t, e, e.agent))
	httpNames := toolNames(t, cs)
	if len(stdio) == 0 || !slices.Equal(stdio, httpNames) {
		t.Fatalf("tool sets differ:\nstdio %v\nhttp  %v", stdio, httpNames)
	}

	res, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name: "problems_create",
		Arguments: map[string]any{
			"type":    "open",
			"prompt":  "What does FSRS model over HTTP?",
			"payload": map[string]any{"reference_answer": "memory"},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("problems_create: %v %s", err, resultText(t, res))
	}
	var created struct {
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &created); err != nil || created.ID == "" {
		t.Fatalf("create result = %s (%v)", resultText(t, res), err)
	}
	if created.Prompt != "What does FSRS model over HTTP?" {
		t.Errorf("prompt = %q", created.Prompt)
	}

	// The human the agent acts for sees it through the REST API.
	if r := e.req(t, "GET", "/problems/"+created.ID, e.human, nil, nil); r.code != http.StatusOK {
		t.Errorf("owner GET = %d", r.code)
	}

	// Another user's agent token is scoped to that user: the problem is invisible.
	otherAgent := e.idp.access("quizzotic-agent", "other-sub")
	res, err = mcpHTTPSession(t, e, otherAgent).CallTool(ctx, &sdk.CallToolParams{
		Name: "problems_get", Arguments: map[string]any{"id": created.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "404") {
		t.Errorf("another user's agent read the problem: IsError=%v %s", res.IsError, resultText(t, res))
	}

	// An agent's scopes still bind: this client cannot read attempts.
	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "attempts_list", Arguments: map[string]any{}})
	if err == nil && !res.IsError {
		t.Errorf("agent without attempts:read listed attempts: %s", resultText(t, res))
	}
}
