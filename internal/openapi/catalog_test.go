// SPDX-License-Identifier: Apache-2.0

package openapi_test

import (
	"slices"
	"testing"

	"github.com/chan-labs/quizzotic/internal/openapi"
)

func TestScopeCatalogMatchesSpecScopes(t *testing.T) {
	got, err := openapi.SpecScopes()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"admin", "attempts:read", "attempts:write", "metrics:read",
		"problems:read", "problems:write", "quizzes:read", "quizzes:write",
		"sessions:write", "tags:apply", "tags:read", "tags:write",
	}
	if !slices.Equal(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
}

func TestScopeCatalogRejectsUndeclaredScope(t *testing.T) {
	spec := `
x-quizzotic-scope-catalog:
  a:read: x
paths:
  /x:
    get:
      operationId: x.get
      x-quizzotic-scopes: [b:read]
`
	if _, err := openapi.ScopeCatalog([]byte(spec)); err == nil {
		t.Fatal("expected an error for a scope outside the catalog")
	}
}

func TestOperationAuthenticatedFollowsSecurity(t *testing.T) {
	spec := `
paths:
  /pub:
    get: {operationId: p, security: [], x-quizzotic-scopes: []}
  /none:
    get: {operationId: n, x-quizzotic-scopes: []}
  /me:
    get: {operationId: m, security: [{bearer: []}], x-quizzotic-scopes: []}
`
	ops, err := openapi.Operations([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, o := range ops {
		got[o.Path] = o.Authenticated
	}
	if got["/pub"] || got["/none"] || !got["/me"] {
		t.Errorf("authenticated = %v", got)
	}
}
