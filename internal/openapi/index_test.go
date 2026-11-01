// SPDX-License-Identifier: Apache-2.0

package openapi_test

import (
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/openapi"
)

// hasRef reports whether a $ref survives anywhere in v.
func hasRef(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["$ref"]; ok {
			return true
		}
		for _, e := range t {
			if hasRef(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasRef(e) {
				return true
			}
		}
	}
	return false
}

func pathVars(p string) []string {
	var out []string
	for {
		i := strings.Index(p, "{")
		if i < 0 {
			return out
		}
		j := strings.Index(p[i:], "}")
		out = append(out, p[i+1:i+j])
		p = p[i+j+1:]
	}
}

// Every operation in the embedded spec is indexed, and every schema reachable
// from it is resolved.
func TestSpecOperationsIndexedWithResolvedSchemas(t *testing.T) {
	ops, err := openapi.SpecOperations()
	if err != nil {
		t.Fatal(err)
	}
	// Independent count: operations declare an operationId at this indent.
	want := strings.Count(string(openapi.Spec()), "\n      operationId: ")
	if want == 0 || len(ops) != want {
		t.Fatalf("indexed %d operations, spec declares %d", len(ops), want)
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if op.ID == "" || seen[op.ID] {
			t.Errorf("%s %s: empty or duplicate operationId %q", op.Method, op.Path, op.ID)
		}
		seen[op.ID] = true
		for _, p := range op.Params {
			if p.Schema == nil || hasRef(p.Schema) {
				t.Errorf("%s: param %s has unresolved schema", op.ID, p.Name)
			}
		}
		for _, name := range pathVars(op.Path) {
			found := false
			for _, p := range op.PathParams() {
				found = found || p.Name == name
			}
			if !found {
				t.Errorf("%s: path variable {%s} has no parameter", op.ID, name)
			}
		}
		if op.Body != nil && hasRef(op.Body.Schema) {
			t.Errorf("%s: body schema has unresolved $ref", op.ID)
		}
		if op.MCP && op.Summary == "" {
			t.Errorf("%s: MCP operation has no summary", op.ID)
		}
	}
}

func TestSpecOperationsExtensions(t *testing.T) {
	ops, err := openapi.SpecOperations()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]openapi.Operation{}
	for _, op := range ops {
		by[op.ID] = op
	}
	create := by["problems.create"]
	if create.Tag != "problems" || create.Body == nil || !create.Body.Required || !create.MCP || create.MCPHint == "" {
		t.Errorf("problems.create = %+v", create)
	}
	if create.Body != nil && create.Body.Schema["properties"] == nil {
		t.Errorf("problems.create body schema not resolved: %v", create.Body.Schema)
	}
	list := by["problems.list"]
	if len(list.CLIColumns) == 0 || len(list.QueryParams()) < 3 {
		t.Errorf("problems.list = columns %v, query %v", list.CLIColumns, list.QueryParams())
	}
	var sawLimit bool
	for _, p := range list.QueryParams() {
		if p.Name == "limit" {
			sawLimit = p.Schema["type"] == "integer" // via components/parameters/Limit
		}
	}
	if !sawLimit {
		t.Error("problems.list: limit param ($ref) not resolved")
	}
}

func TestOperationsResolvesRefsAndCycles(t *testing.T) {
	spec := `
paths:
  /things/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: string}}
    post:
      operationId: things.update
      tags: [things]
      x-quizzotic-scopes: []
      requestBody:
        content:
          application/json:
            schema: {$ref: "#/components/schemas/Node"}
components:
  schemas:
    Node:
      type: object
      properties:
        name: {$ref: "#/components/schemas/Name"}
        child: {$ref: "#/components/schemas/Node"}
    Name: {type: string}
`
	ops, err := openapi.Operations([]byte(spec))
	if err != nil || len(ops) != 1 {
		t.Fatalf("ops=%v err=%v", ops, err)
	}
	op := ops[0]
	if len(op.PathParams()) != 1 || op.PathParams()[0].Name != "id" {
		t.Errorf("path params = %v", op.PathParams())
	}
	props := op.Body.Schema["properties"].(map[string]any)
	if props["name"].(map[string]any)["type"] != "string" {
		t.Errorf("name not inlined: %v", props["name"])
	}
	if props["child"].(map[string]any)["$ref"] != "#/components/schemas/Node" {
		t.Errorf("cycle not cut: %v", props["child"])
	}
}

func TestOperationsRejectsDanglingRef(t *testing.T) {
	spec := `
paths:
  /x:
    get:
      operationId: x.get
      x-quizzotic-scopes: []
      parameters:
        - $ref: "#/components/parameters/Nope"
`
	if _, err := openapi.Operations([]byte(spec)); err == nil {
		t.Fatal("expected error for dangling $ref")
	}
}
