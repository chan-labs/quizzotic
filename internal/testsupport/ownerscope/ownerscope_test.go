// SPDX-License-Identifier: Apache-2.0

package ownerscope_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

// recorder captures failures instead of failing the real test, so the tests
// can prove the helper rejects broken servers.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(f string, a ...any) {
	r.errs = append(r.errs, fmt.Sprintf(f, a...))
}

type mode int

const (
	correct    mode = iota
	forbids         // 403 instead of 404
	leaks           // B reads A's item
	noAuth          // anonymous is let in
	alwaysDeny      // even the owner is refused (a vacuous pass)
)

// fakeAPI holds one item; the caller is the bearer token ("A" or "B").
func fakeAPI(m mode) http.Handler {
	owner := ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		prob := func(code int) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"title":"` + http.StatusText(code) + `"}`))
		}
		if user == "" && m != noAuth {
			prob(401)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch {
		case r.Method == http.MethodPost && path == "/things":
			owner = user
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "t1", "title": "secret-title"})
		case r.Method == http.MethodGet && path == "/things/t1":
			switch {
			case m == alwaysDeny:
				prob(404)
			case owner == user || m == leaks:
				_, _ = w.Write([]byte(`{"title":"secret-title"}`))
			case m == forbids:
				prob(403)
			default:
				prob(404)
			}
		default:
			prob(404)
		}
	})
}

func run(t *testing.T, m mode) []string {
	srv := httptest.NewServer(fakeAPI(m))
	t.Cleanup(srv.Close)
	tokens := map[ownerscope.User]string{ownerscope.UserA: "A", ownerscope.UserB: "B"}
	env := &ownerscope.Env{
		BaseURL: srv.URL,
		Credentials: func(_ testing.TB, u ownerscope.User) func(*http.Request) {
			return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tokens[u]) }
		},
	}
	reg := ownerscope.NewRegistry()
	reg.Resource(ownerscope.Resource{
		Name: "things",
		Create: func(_ testing.TB, c *ownerscope.Client) ownerscope.Created {
			r := c.Do("POST", "/things", nil)
			var o struct{ ID, Title string }
			_ = json.Unmarshal(r.Body, &o)
			return ownerscope.Created{Params: ownerscope.Params{"id": o.ID}, Secrets: []string{o.Title}}
		},
		Ops: []ownerscope.Op{{Pattern: "GET /things/{id}"}},
	})
	rec := &recorder{TB: t}
	reg.RunWith(rec, env)
	return rec.errs
}

func TestHelperAcceptsCorrectServer(t *testing.T) {
	if errs := run(t, correct); len(errs) != 0 {
		t.Fatalf("unexpected failures: %v", errs)
	}
}

func TestHelperRejectsBrokenServers(t *testing.T) {
	for name, m := range map[string]mode{"403": forbids, "leak": leaks, "anonymous allowed": noAuth, "owner denied": alwaysDeny} {
		t.Run(name, func(t *testing.T) {
			if errs := run(t, m); len(errs) == 0 {
				t.Fatal("helper passed a broken server")
			}
		})
	}
}

func TestCoverageFailsClosed(t *testing.T) {
	ops := []openapi.Operation{
		{Method: "GET", Path: "/public"},
		{Method: "GET", Path: "/things/{id}", Scopes: []string{"problems:read"}},
		{Method: "GET", Path: "/me", Authenticated: true},
		{Method: "GET", Path: "/admin/x", Scopes: []string{"admin"}},
	}
	reg := ownerscope.NewRegistry()
	if got := reg.Coverage(ops); len(got) != 3 {
		t.Fatalf("empty registry should flag the 3 non-public ops: %v", got)
	}
	reg.Resource(ownerscope.Resource{Ops: []ownerscope.Op{{Pattern: "GET /things/{id}"}}})
	reg.Self(ownerscope.Self{Pattern: "GET /me"})
	reg.Exempt("GET /admin/x", "admin only; reads no owner data")
	if got := reg.Coverage(ops); got != nil {
		t.Fatalf("complete registry flagged: %v", got)
	}
	reg.Exempt("GET /ghost", "stale")
	if got := reg.Coverage(ops); len(got) != 1 {
		t.Fatalf("stale exemption should be flagged: %v", got)
	}
}

func TestExemptNeedsReason(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("empty reason accepted")
		}
	}()
	ownerscope.NewRegistry().Exempt("GET /x", " ")
}
