// SPDX-License-Identifier: Apache-2.0

package openapi_test

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
)

var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

type document struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

// specOps returns "METHOD /path" for API operations and for root operations
// (path items whose servers override is "/"), plus checks every operation
// carries operationId and x-quizzotic-scopes.
func specOps(t *testing.T) (api, root []string) {
	t.Helper()
	var doc document
	if err := yaml.Unmarshal(openapi.Spec(), &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	for path, item := range doc.Paths {
		isRoot := false
		if n, ok := item["servers"]; ok {
			var servers []struct {
				URL string `yaml:"url"`
			}
			if err := n.Decode(&servers); err != nil {
				t.Fatalf("%s servers: %v", path, err)
			}
			isRoot = len(servers) == 1 && servers[0].URL == "/"
		}
		for key, n := range item {
			if !httpMethods[key] {
				continue
			}
			var op map[string]yaml.Node
			if err := n.Decode(&op); err != nil {
				t.Fatalf("%s %s: %v", key, path, err)
			}
			for _, req := range []string{"operationId", "x-quizzotic-scopes"} {
				if _, ok := op[req]; !ok {
					t.Errorf("%s %s: missing %s", strings.ToUpper(key), path, req)
				}
			}
			pat := strings.ToUpper(key) + " " + path
			if isRoot {
				root = append(root, pat)
			} else {
				api = append(api, pat)
			}
		}
	}
	sort.Strings(api)
	sort.Strings(root)
	return api, root
}

// appRouter builds the router with every feature registered. Each feature
// task adds its Register call here, and its operations to api/openapi.yaml.
func appRouter() *platform.Router {
	rt := platform.NewRouter()
	openapi.Register(rt)
	return rt
}

func diff(t *testing.T, what string, have, want []string) {
	t.Helper()
	in := map[string]bool{}
	for _, w := range want {
		in[w] = true
	}
	for _, h := range have {
		if !in[h] {
			t.Errorf("%s: %q is not in the other side", what, h)
		}
	}
}

func TestRoutesMatchSpec(t *testing.T) {
	apiOps, rootOps := specOps(t)
	routes := appRouter().Patterns()

	diff(t, "registered route without spec operation", routes, apiOps)
	diff(t, "spec operation without registered route", apiOps, routes)
	rootRoutes := append(append([]string(nil), platform.RootRoutes...), auth.WebRoutes...)
	diff(t, "root route without spec operation", rootRoutes, rootOps)
	diff(t, "spec root operation without route", rootOps, rootRoutes)
}

func TestServesSpec(t *testing.T) {
	mux := http.NewServeMux()
	appRouter().Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("content type = %q", ct)
	}
	if rec.Body.String() != string(openapi.Spec()) {
		t.Error("body differs from embedded spec")
	}
}

func TestRootRoutesServed(t *testing.T) {
	mux := http.NewServeMux()
	platform.NewHealth(nil).Register(mux)
	platform.NewMetrics().Register(mux)
	for _, p := range platform.RootRoutes {
		method, path, _ := strings.Cut(p, " ")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s not served by Health.Register (status %d)", p, rec.Code)
		}
	}
}
