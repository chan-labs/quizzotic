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
		for key, n := range item {
			if !methods[key] {
				continue
			}
			var op struct {
				ID       string           `yaml:"operationId"`
				Scopes   *[]string        `yaml:"x-quizzotic-scopes"`
				Security []map[string]any `yaml:"security"`
			}
			if err := n.Decode(&op); err != nil {
				return nil, fmt.Errorf("openapi: %s %s: %w", key, path, err)
			}
			if op.Scopes == nil {
				return nil, fmt.Errorf("openapi: %s %s: missing x-quizzotic-scopes", strings.ToUpper(key), path)
			}
			out = append(out, Operation{ID: op.ID, Method: strings.ToUpper(key), Path: path, Scopes: *op.Scopes, Authenticated: len(op.Security) > 0, Root: root})
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
