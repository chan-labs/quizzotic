// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chan-labs/quizzotic/internal/mcp"
)

func mcpSession(t *testing.T, e *probEnv, token string) *sdk.ClientSession {
	t.Helper()
	srv, err := mcp.New(mcp.Options{
		BaseURL: e.srv.URL,
		Token:   func(context.Context) (string, error) { return token, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func resultText(t *testing.T, r *sdk.CallToolResult) string {
	t.Helper()
	// The first text item is the response body; a second one may carry the ETag.
	for _, c := range r.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// TestMCPAgainstRealServer drives the generated MCP server over an in-memory
// transport against the production handler: list tools, create a problem as
// an agent, read it back, and see API errors as tool errors.
func TestMCPAgainstRealServer(t *testing.T) {
	e := newProbEnv(t)
	cs := mcpSession(t, e, e.agent)
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, name := range []string{
		"problems_create", "problems_get", "problems_list", "tags_create",
		"tagOperations_propose", "tagOperations_list", "tagOperations_get",
	} {
		if !have[name] {
			t.Errorf("tools/list lacks %s", name)
		}
	}
	for _, name := range []string{"attempts_create", "attempts_rate", "tagOperations_apply"} {
		if have[name] {
			t.Errorf("tools/list exposes %s", name)
		}
	}

	res, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name: "problems_create",
		Arguments: map[string]any{
			"type":    "open",
			"prompt":  "What does FSRS model?",
			"payload": map[string]any{"reference_answer": "memory"},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("problems_create: %v %+v %s", err, res, resultText(t, res))
	}
	var created struct {
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &created); err != nil || created.ID == "" {
		t.Fatalf("create result = %s (%v)", resultText(t, res), err)
	}
	if created.Prompt != "What does FSRS model?" {
		t.Errorf("created prompt = %q", created.Prompt)
	}

	// A path parameter round-trips.
	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "problems_get", Arguments: map[string]any{"id": created.ID}})
	if err != nil || res.IsError || !strings.Contains(resultText(t, res), created.ID) {
		t.Fatalf("problems_get: %v %s", err, resultText(t, res))
	}

	// A query parameter filters the list.
	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "problems_list", Arguments: map[string]any{"limit": 1}})
	if err != nil || res.IsError || !strings.Contains(resultText(t, res), created.ID) {
		t.Fatalf("problems_list: %v %s", err, resultText(t, res))
	}

	// API errors become tool errors, not protocol errors.
	res, err = cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "problems_get",
		Arguments: map[string]any{"id": "00000000-0000-7000-8000-000000000000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "404") {
		t.Errorf("missing problem: IsError=%v %s", res.IsError, resultText(t, res))
	}

	// Another user's problem is invisible to this agent's owner scope.
	other := mcpSession(t, e, e.other)
	res, err = other.CallTool(ctx, &sdk.CallToolParams{Name: "problems_get", Arguments: map[string]any{"id": created.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("another user read the problem: %s", resultText(t, res))
	}

	// Without a token the API answers 401 as a tool error.
	anon := mcpSession(t, e, "")
	res, err = anon.CallTool(ctx, &sdk.CallToolParams{Name: "problems_list", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "401") {
		t.Errorf("anonymous: IsError=%v %s", res.IsError, resultText(t, res))
	}
}
