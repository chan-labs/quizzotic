// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		if op.MCPHint != "" && !strings.Contains(tl.Description, ToolRefs(Exposed(specOps(t)), op.MCPHint)) {
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
		if op.ID == "attempts.create" || op.ID == "attempts.rate" || strings.HasPrefix(op.ID, "sessions.") ||
			strings.HasPrefix(op.ID, "admin.") || strings.HasPrefix(op.Path, "/admin") {
			t.Errorf("%s must not be exposed", op.ID)
		}
	}
	// Attempt reads are allowed and exposed.
	for _, name := range []string{"attempts_list", "attempts_get"} {
		if !exposed[name] {
			t.Errorf("tool %s is not exposed", name)
		}
	}
	for _, name := range []string{"attempts_create", "attempts_rate"} {
		if exposed[name] {
			t.Errorf("write tool %s is exposed", name)
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

// TestAuthoringHintsInDescriptions pins the authoring guidance so it cannot
// silently disappear from the tools an agent uses to author content.
func TestAuthoringHintsInDescriptions(t *testing.T) {
	want := map[string][]string{
		"problems.create":       {"problems_similar", "draft", "tag", "tags_list", "alias"},
		"problems.createBatch":  {"problems_similar", "draft", "tag", "tags_list", "alias"},
		"problems.similar":      {"before problems_create", "duplicate"},
		"problems.setTags":      {"tags_list", "existing tags", "alias"},
		"tags.create":           {"tags_list", "existing tag", "alias"},
		"tagOperations.propose": {"never apply", "human"},
	}
	byName := map[string]*sdk.Tool{}
	for _, tl := range listTools(t) {
		byName[tl.Name] = tl
	}
	for op, subs := range want {
		tl := byName[strings.ReplaceAll(op, ".", "_")]
		if tl == nil {
			t.Errorf("tool for %s not exposed", op)
			continue
		}
		desc := strings.Join(strings.Fields(tl.Description), " ")
		for _, s := range subs {
			if !strings.Contains(desc, s) {
				t.Errorf("%s description missing %q:\n%s", op, s, desc)
			}
		}
	}
}

// TestDescriptionsOnlyReferenceExposedTools: an agent must be able to follow
// every instruction in a description, so no description may name a tool that
// is not exposed, in dotted or underscored form, and none may mention a header
// the server does not set.
func TestDescriptionsOnlyReferenceExposedTools(t *testing.T) {
	ops := specOps(t)
	exposed := map[string]bool{}
	for _, op := range Exposed(ops) {
		exposed[ToolName(op)] = true
	}
	// Every known operationId, dotted or underscored, mapped to its tool name.
	known := map[string]string{}
	for _, op := range ops {
		known[op.ID] = ToolName(op)
		known[ToolName(op)] = ToolName(op)
	}
	// Anything shaped like an operationId (group.verb or group_verb).
	ref := regexp.MustCompile(`\b[a-z][A-Za-z]*[._][a-z][A-Za-z]*\b`)
	for _, tl := range listTools(t) {
		for _, m := range ref.FindAllString(tl.Description, -1) {
			name, isOp := known[m]
			if !isOp {
				continue
			}
			if m != name {
				t.Errorf("%s: description uses dotted ID %q, not the tool name %q", tl.Name, m, name)
			}
			if !exposed[name] {
				t.Errorf("%s: description references %q, which is not an exposed tool", tl.Name, name)
			}
		}
		// The server sets no Idempotency-Key, so no description may ask for one.
		if strings.Contains(strings.ToLower(tl.Description), "idempotency-key") {
			t.Errorf("%s: description mentions Idempotency-Key, which tools cannot send", tl.Name)
		}
		// If-Match is only reachable as the if_match argument.
		if strings.Contains(tl.Description, "send its ETag as If-Match") {
			t.Errorf("%s: description tells the agent to send a header", tl.Name)
		}
	}
}

// TestIfMatchIsAToolArgument: operations requiring If-Match take it as
// if_match, because tools cannot set headers.
func TestIfMatchIsAToolArgument(t *testing.T) {
	for _, tl := range listTools(t) {
		if tl.Name != "problems_edit" && tl.Name != "quizzes_setItems" {
			continue
		}
		m, _ := tl.InputSchema.(map[string]any)
		props, _ := m["properties"].(map[string]any)
		if _, ok := props["if_match"]; !ok {
			t.Errorf("%s lacks the if_match argument", tl.Name)
		}
	}
}

// TestIfMatchSentAndETagReturned: if_match travels as the If-Match header, and
// a response ETag is returned as a second text item so it can be sent back.
func TestIfMatchSentAndETagReturned(t *testing.T) {
	var gotIfMatch, gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfMatch, gotKey = r.Header.Get("If-Match"), r.Header.Get("Idempotency-Key")
		w.Header().Set("ETag", `"3-99"`)
		_, _ = w.Write([]byte(`{"id":"p1"}`))
	}))
	defer ts.Close()
	srv, err := New(Options{BaseURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "problems_edit", Arguments: map[string]any{
		"id": "p1", "if_match": `"2-1"`, "change_kind": "minor", "prompt": "x",
	}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if gotIfMatch != `"2-1"` {
		t.Errorf("If-Match = %q", gotIfMatch)
	}
	if gotKey != "" {
		t.Errorf("unexpected Idempotency-Key %q", gotKey)
	}
	if len(res.Content) != 2 || res.Content[1].(*sdk.TextContent).Text != `ETag: "3-99"` {
		t.Errorf("content = %+v", res.Content)
	}
}
