// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chan-labs/quizzotic/internal/openapi"
)

func specOps(t *testing.T) []openapi.Operation {
	t.Helper()
	ops, err := openapi.SpecOperations()
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func listTools(t *testing.T) []*sdk.Tool {
	t.Helper()
	srv, err := New(Options{BaseURL: "http://127.0.0.1:1"})
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
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Tools
}

func TestToolsMatchFlaggedOperations(t *testing.T) {
	tools := listTools(t)
	names := map[string]*sdk.Tool{}
	for _, tl := range tools {
		names[tl.Name] = tl
	}
	var want int
	for _, op := range specOps(t) {
		if !op.MCP {
			if _, ok := names[ToolName(op)]; ok {
				t.Errorf("unflagged operation %s is exposed", op.ID)
			}
			continue
		}
		want++
		tl, ok := names[strings.ReplaceAll(op.ID, ".", "_")]
		if !ok {
			t.Errorf("flagged operation %s has no tool", op.ID)
			continue
		}
		if op.Summary != "" && !strings.Contains(tl.Description, op.Summary) {
			t.Errorf("%s: description lacks the summary", tl.Name)
		}
		if op.MCPHint != "" && !strings.Contains(tl.Description, op.MCPHint) {
			t.Errorf("%s: description lacks the hint", tl.Name)
		}
	}
	if len(tools) != want {
		t.Errorf("got %d tools, want %d", len(tools), want)
	}
}

// Tag operations marked for MCP (M8) must be exposed.
func TestTagOperationToolsExposed(t *testing.T) {
	have := map[string]bool{}
	for _, tl := range listTools(t) {
		have[tl.Name] = true
	}
	for _, name := range []string{"tagOperations_propose", "tagOperations_list", "tagOperations_get"} {
		if !have[name] {
			t.Errorf("tool %s is not exposed", name)
		}
	}
	// Applying, rejecting and reverting need tags:apply and stay human-only.
	for _, name := range []string{"tagOperations_apply", "tagOperations_reject", "tagOperations_revert"} {
		if have[name] {
			t.Errorf("tool %s must not be exposed", name)
		}
	}
}

// No tool may write attempts or sessions, or administer the instance.
func TestNoForbiddenOperationExposed(t *testing.T) {
	ops := specOps(t)
	exposed := map[string]bool{}
	for _, tl := range listTools(t) {
		exposed[tl.Name] = true
	}
	for _, op := range ops {
		if !exposed[ToolName(op)] {
			continue
		}
		if slices.ContainsFunc(op.Scopes, func(s string) bool { return slices.Contains(forbiddenScopes, s) }) {
			t.Errorf("%s needs a forbidden scope %v", op.ID, op.Scopes)
		}
		if strings.HasPrefix(op.ID, "attempts.") || strings.HasPrefix(op.ID, "sessions.") ||
			strings.HasPrefix(op.ID, "admin.") || strings.HasPrefix(op.Path, "/admin") {
			t.Errorf("%s must not be exposed", op.ID)
		}
	}
}

// Exposed drops a flagged operation that needs a forbidden scope.
func TestExposedFiltersForbiddenScopes(t *testing.T) {
	ops := []openapi.Operation{
		{ID: "attempts.create", MCP: true, Scopes: []string{"attempts:write"}},
		{ID: "sessions.start", MCP: true, Scopes: []string{"sessions:write"}},
		{ID: "x.admin", MCP: true, Scopes: []string{"admin"}},
		{ID: "problems.list", MCP: true, Scopes: []string{"problems:read"}},
	}
	got := Exposed(ops)
	if len(got) != 1 || got[0].ID != "problems.list" {
		t.Errorf("Exposed = %v", got)
	}
}

func TestInputSchemaMergesPathQueryAndBody(t *testing.T) {
	byName := map[string]*sdk.Tool{}
	for _, tl := range listTools(t) {
		byName[tl.Name] = tl
	}
	schema := func(name string) map[string]any {
		t.Helper()
		tl := byName[name]
		m, ok := tl.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s schema is %T", name, tl.InputSchema)
		}
		return m
	}
	create := schema("problems_create")["properties"].(map[string]any)
	for _, k := range []string{"type", "prompt", "payload"} {
		if _, ok := create[k]; !ok {
			t.Errorf("problems_create schema lacks body property %q", k)
		}
	}
	get := schema("problems_get")["properties"].(map[string]any)
	if _, ok := get["id"]; !ok {
		t.Errorf("problems_get schema lacks path parameter id")
	}
	list := schema("problems_list")["properties"].(map[string]any)
	if _, ok := list["limit"]; !ok {
		t.Errorf("problems_list schema lacks query parameter limit")
	}
}
