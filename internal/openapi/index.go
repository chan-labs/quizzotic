// SPDX-License-Identifier: Apache-2.0

package openapi

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Operation is one API operation read from the embedded spec.
type Operation struct {
	ID     string
	Method string // upper case
	Path   string // as written in the spec, e.g. "/problems/{id}"
	// Scopes: any one authorizes the operation; empty means no scope needed.
	Scopes []string
	// Authenticated: the operation lists a security scheme, so it needs an
	// authenticated principal even when it needs no particular scope.
	Authenticated bool
	// Root operations live at the host root (servers: "/"), not under /api/v1.
	Root bool

	// Tag is the operation's first OpenAPI tag (the CLI resource); empty when
	// the operation has none.
	Tag         string
	Summary     string
	Description string
	// Params are the path-level and operation-level parameters, with $refs
	// resolved. An operation-level parameter overrides a path-level one with
	// the same name and location.
	Params []Parameter
	// Body is the JSON request body, or nil when the operation has none or
	// only a non-JSON one.
	Body *Body
	// CLIColumns is x-quizzotic-cli-columns.
	CLIColumns []string
	// MCP is x-quizzotic-mcp: the operation is exposed as an MCP tool.
	MCP bool
	// MCPHint is x-quizzotic-mcp-hint.
	MCPHint string
}

// Parameter is a resolved path, query, header or cookie parameter.
type Parameter struct {
	Name        string
	In          string // "path", "query", "header" or "cookie"
	Required    bool
	Description string
	// Schema is the parameter schema with every $ref inlined.
	Schema map[string]any
}

// Body is a resolved application/json request body.
type Body struct {
	Required bool
	// Schema is the body schema with every $ref inlined. A schema that refers
	// back to itself keeps the inner $ref (e.g. "#/components/schemas/Node").
	Schema map[string]any
}

// PathParams returns the operation's path parameters in declaration order.
func (o Operation) PathParams() []Parameter { return o.paramsIn("path") }

// QueryParams returns the operation's query parameters in declaration order.
func (o Operation) QueryParams() []Parameter { return o.paramsIn("query") }

func (o Operation) paramsIn(in string) []Parameter {
	var out []Parameter
	for _, p := range o.Params {
		if p.In == in {
			out = append(out, p)
		}
	}
	return out
}

var methods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// Operations parses an OpenAPI document and returns its operations sorted by
// path then method. An operation lacking x-quizzotic-scopes is an error.
func Operations(spec []byte) ([]Operation, error) {
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("openapi: parse: %w", err)
	}
	var tree any
	if err := yaml.Unmarshal(spec, &tree); err != nil {
		return nil, fmt.Errorf("openapi: parse: %w", err)
	}
	r := &resolver{root: tree}
	var out []Operation
	for path, item := range doc.Paths {
		root := false
		if n, ok := item["servers"]; ok {
			var servers []struct {
				URL string `yaml:"url"`
			}
			if err := n.Decode(&servers); err != nil {
				return nil, fmt.Errorf("openapi: %s servers: %w", path, err)
			}
			root = len(servers) == 1 && servers[0].URL == "/"
		}
		var pathParams []any
		if n, ok := item["parameters"]; ok {
			if err := n.Decode(&pathParams); err != nil {
				return nil, fmt.Errorf("openapi: %s parameters: %w", path, err)
			}
		}
		for key, n := range item {
			if !methods[key] {
				continue
			}
			var op struct {
				ID          string           `yaml:"operationId"`
				Tags        []string         `yaml:"tags"`
				Summary     string           `yaml:"summary"`
				Description string           `yaml:"description"`
				Scopes      *[]string        `yaml:"x-quizzotic-scopes"`
				Security    []map[string]any `yaml:"security"`
				Parameters  []any            `yaml:"parameters"`
				RequestBody any              `yaml:"requestBody"`
				CLIColumns  []string         `yaml:"x-quizzotic-cli-columns"`
				MCP         bool             `yaml:"x-quizzotic-mcp"`
				MCPHint     string           `yaml:"x-quizzotic-mcp-hint"`
			}
			if err := n.Decode(&op); err != nil {
				return nil, fmt.Errorf("openapi: %s %s: %w", key, path, err)
			}
			if op.Scopes == nil {
				return nil, fmt.Errorf("openapi: %s %s: missing x-quizzotic-scopes", strings.ToUpper(key), path)
			}
			where := strings.ToUpper(key) + " " + path
			params, err := r.parameters(pathParams, op.Parameters)
			if err != nil {
				return nil, fmt.Errorf("openapi: %s: %w", where, err)
			}
			body, err := r.body(op.RequestBody)
			if err != nil {
				return nil, fmt.Errorf("openapi: %s: %w", where, err)
			}
			o := Operation{
				ID: op.ID, Method: strings.ToUpper(key), Path: path, Scopes: *op.Scopes,
				Authenticated: len(op.Security) > 0, Root: root,
				Summary: strings.TrimSpace(op.Summary), Description: strings.TrimSpace(op.Description),
				Params: params, Body: body, CLIColumns: op.CLIColumns, MCP: op.MCP,
				MCPHint: strings.TrimSpace(op.MCPHint),
			}
			if len(op.Tags) > 0 {
				o.Tag = op.Tags[0]
			}
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

// SpecOperations returns the operations of the embedded spec.
func SpecOperations() ([]Operation, error) { return Operations(Spec()) }

// ScopeCatalog returns the scopes declared in the spec's
// x-quizzotic-scope-catalog, sorted. It errors if an operation uses a scope
// missing from the catalog.
func ScopeCatalog(spec []byte) ([]string, error) {
	var doc struct {
		Catalog map[string]string `yaml:"x-quizzotic-scope-catalog"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("openapi: parse: %w", err)
	}
	ops, err := Operations(spec)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		for _, s := range op.Scopes {
			if _, ok := doc.Catalog[s]; !ok {
				return nil, fmt.Errorf("openapi: %s %s: scope %q is not in x-quizzotic-scope-catalog", op.Method, op.Path, s)
			}
		}
	}
	out := make([]string, 0, len(doc.Catalog))
	for s := range doc.Catalog {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// SpecScopes returns the scope catalog of the embedded spec.
func SpecScopes() ([]string, error) { return ScopeCatalog(Spec()) }
