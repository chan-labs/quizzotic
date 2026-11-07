// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// bodyKey names the tool argument that carries the request body when the body
// cannot be merged into the top-level arguments.
const bodyKey = "body"

// forbiddenScopes are never reachable from a tool: an agent can answer
// nothing and administer nothing.
var forbiddenScopes = []string{"attempts:write", "sessions:write", "admin"}

// Options configures a server.
type Options struct {
	// BaseURL is the Quizzotic server root, without /api/v1.
	BaseURL string
	// HTTP defaults to a client that traces outbound requests.
	HTTP *http.Client
	// Ops defaults to the embedded spec's operations.
	Ops []openapi.Operation
	// Token returns the bearer token for a call; it may return "".
	Token func(ctx context.Context) (string, error)
	// Version is reported as the server version.
	Version string
}

// ToolName is the MCP tool name for an operation: its operationId with dots
// replaced by underscores.
func ToolName(op openapi.Operation) string { return strings.ReplaceAll(op.ID, ".", "_") }

// argName is the tool argument that carries a parameter. Path and query
// parameters keep their names. The only header an agent can set is If-Match,
// exposed as "if_match"; every other header is invisible to a tool.
func argName(p openapi.Parameter) (string, bool) {
	switch {
	case p.In == "path" || p.In == "query":
		return p.Name, true
	case p.In == "header" && strings.EqualFold(p.Name, "If-Match"):
		return "if_match", true
	}
	return "", false
}

// Exposed returns the operations that become tools: those marked
// x-quizzotic-mcp, minus any needing a scope an agent can never hold.
func Exposed(ops []openapi.Operation) []openapi.Operation {
	var out []openapi.Operation
	for _, op := range ops {
		if !op.MCP || op.ID == "" {
			continue
		}
		if slices.ContainsFunc(op.Scopes, func(s string) bool { return slices.Contains(forbiddenScopes, s) }) {
			continue
		}
		out = append(out, op)
	}
	return out
}

// New builds an MCP server with one tool per exposed operation.
func New(o Options) (*sdk.Server, error) {
	ops := o.Ops
	if ops == nil {
		var err error
		if ops, err = openapi.SpecOperations(); err != nil {
			return nil, err
		}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Transport: platform.TraceTransport(nil)}
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.Version == "" {
		o.Version = "dev"
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "quizzotic", Version: o.Version}, nil)
	exposed := Exposed(ops)
	refs := idRewriter(exposed)
	for _, op := range exposed {
		t := &tool{op: op, opts: o}
		t.layout()
		srv.AddTool(&sdk.Tool{
			Name:        ToolName(op),
			Description: description(op, refs),
			InputSchema: t.schema(),
		}, t.call)
	}
	return srv, nil
}

// idRewriter returns a function that rewrites exposed operationIds written in
// dotted form (as hints in openapi.yaml are) to the tool names an agent sees.
func idRewriter(ops []openapi.Operation) func(string) string {
	var ids []string
	for _, op := range ops {
		ids = append(ids, regexp.QuoteMeta(op.ID))
	}
	if len(ids) == 0 {
		return func(s string) string { return s }
	}
	// Longest first so problems.createBatch is not read as problems.create.
	sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })
	re := regexp.MustCompile(`\b(?:` + strings.Join(ids, "|") + `)\b`)
	return func(s string) string {
		return re.ReplaceAllStringFunc(s, func(m string) string { return strings.ReplaceAll(m, ".", "_") })
	}
}

// ToolRefs rewrites dotted operationIds in text to the tool names of ops.
func ToolRefs(ops []openapi.Operation, text string) string { return idRewriter(ops)(text) }

func description(op openapi.Operation, rewrite func(string) string) string {
	var parts []string
	for _, s := range []string{op.Summary, op.Description, op.MCPHint} {
		if s = strings.TrimSpace(s); s != "" {
			s = rewrite(s)
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

type tool struct {
	op   openapi.Operation
	opts Options
	// mergeBody: the object body's properties are top-level arguments.
	mergeBody bool
	bodyProps map[string]any
}

// layout decides whether the body merges into the top-level arguments (an
// object body whose property names do not collide with parameters) or travels
// under "body".
func (t *tool) layout() {
	if t.op.Body == nil {
		return
	}
	s := t.op.Body.Schema
	props, _ := s["properties"].(map[string]any)
	if s["type"] != "object" || len(props) == 0 {
		return
	}
	for name := range props {
		if name == bodyKey || slices.ContainsFunc(t.op.Params, func(p openapi.Parameter) bool { n, _ := argName(p); return n == name }) {
			return
		}
	}
	t.mergeBody, t.bodyProps = true, props
}

func (t *tool) schema() map[string]any {
	props := map[string]any{}
	var required []any
	for _, p := range t.op.Params {
		name, ok := argName(p)
		if !ok {
			continue
		}
		ps := map[string]any{}
		for k, v := range p.Schema {
			ps[k] = v
		}
		if p.Description != "" {
			ps["description"] = strings.Join(strings.Fields(p.Description), " ")
		}
		props[name] = ps
		if p.Required {
			required = append(required, name)
		}
	}
	if b := t.op.Body; b != nil {
		if t.mergeBody {
			for k, v := range t.bodyProps {
				props[k] = v
			}
			if req, ok := b.Schema["required"].([]any); ok {
				required = append(required, req...)
			}
		} else {
			props[bodyKey] = b.Schema
			if b.Required {
				required = append(required, bodyKey)
			}
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func toolError(format string, a ...any) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf(format, a...)}},
	}
}

func (t *tool) call(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	args := map[string]any{}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return toolError("invalid arguments: %v", err), nil
		}
	}
	op := t.op
	path := op.Path
	q := url.Values{}
	used := map[string]bool{}
	hdr := http.Header{}
	for _, p := range op.Params {
		name, ok := argName(p)
		if !ok {
			continue
		}
		v, ok := args[name]
		if !ok || v == nil {
			if p.Required {
				return toolError("missing required argument %q", name), nil
			}
			continue
		}
		used[name] = true
		if p.In == "header" {
			hdr.Set(p.Name, scalar(v))
			continue
		}
		if p.In == "path" {
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(scalar(v)))
			continue
		}
		if arr, ok := v.([]any); ok {
			for _, e := range arr {
				q.Add(p.Name, scalar(e))
			}
		} else {
			q.Add(p.Name, scalar(v))
		}
	}

	var body []byte
	if op.Body != nil {
		var doc any
		empty := true
		if t.mergeBody {
			m := map[string]any{}
			for k, v := range args {
				if !used[k] {
					m[k] = v
				}
			}
			doc, empty = m, len(m) == 0
		} else if v, ok := args[bodyKey]; ok && v != nil {
			doc, empty = v, false
		}
		if empty && op.Body.Required {
			return toolError("a request body is required"), nil
		}
		if !empty {
			var err error
			if body, err = json.Marshal(doc); err != nil {
				return toolError("encoding body: %v", err), nil
			}
		}
	}

	target := t.opts.BaseURL
	if !op.Root {
		target += "/api/v1"
	}
	target += path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	hr, err := http.NewRequestWithContext(ctx, op.Method, target, rd)
	if err != nil {
		return toolError("%v", err), nil
	}
	hr.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		hr.Header[k] = v
	}
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if t.opts.Token != nil {
		tok, err := t.opts.Token(ctx)
		if err != nil {
			return toolError("authentication: %v", err), nil
		}
		if tok != "" {
			hr.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := t.opts.HTTP.Do(hr)
	if err != nil {
		return toolError("request failed: %v", err), nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return toolError("reading response: %v", err), nil
	}
	text := strings.TrimSpace(string(raw))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return toolError("%s\n%s", resp.Status, text), nil
	}
	if text == "" {
		text = fmt.Sprintf(`{"status":%d}`, resp.StatusCode)
	}
	content := []sdk.Content{&sdk.TextContent{Text: text}}
	if etag := resp.Header.Get("ETag"); etag != "" {
		// Tools see no headers; surface the ETag so it can be sent back as if_match.
		content = append(content, &sdk.TextContent{Text: "ETag: " + etag})
	}
	return &sdk.CallToolResult{Content: content}, nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return json.Number(fmt.Sprint(x)).String()
	case bool:
		return fmt.Sprint(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
