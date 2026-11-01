// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// harness mounts the guard in front of the REAL platform.Router and ServeMux,
// with a handler per guardSpec operation that counts how often it is reached.
type harness struct {
	mux     *http.ServeMux
	reached atomic.Int64
}

func newHarness(t *testing.T, a Authenticator) *harness {
	t.Helper()
	ops, err := openapi.Operations([]byte(guardSpec))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{}
	rt := platform.NewRouter()
	rt.SetAuthorizer(NewScopeGuard(a, ops).Authorize)
	count := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	for _, p := range []string{
		"GET /open", "GET /problems", "POST /problems", "POST /problems/similar",
		"GET /problems/{id}", "POST /attempts",
	} {
		rt.Handle(p, count)
	}
	h.mux = http.NewServeMux()
	rt.Mount(h.mux)
	return h
}

func (h *harness) do(method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestScopeGuardDecisions(t *testing.T) {
	agent := Principal{Kind: KindAgent, Scopes: map[string]bool{"problems:read": true}}
	other := Principal{Kind: KindAgent, Scopes: map[string]bool{"tags:read": true}}
	human := Principal{Kind: KindHuman}
	none := fakeAuth{err: ErrNoCredentials}
	cases := []struct {
		name         string
		a            Authenticator
		method, path string
		want         int
		reaches      bool
	}{
		{"scope-free op needs no credentials", none, "GET", "/open", 204, true},
		{"scope-free op on same path, wrong method: router 405", none, "POST", "/open", 405, false},
		{"no credentials", none, "GET", "/problems", 401, false},
		{"bad token", fakeAuth{err: invalid("x")}, "GET", "/problems", 401, false},
		{"unknown client", fakeAuth{err: ErrUnknownClient}, "GET", "/problems", 401, false},
		{"csrf failure", fakeAuth{err: ErrCSRF}, "POST", "/problems", 403, false},
		{"infra failure is opaque 500", fakeAuth{err: errors.New("db down")}, "GET", "/problems", 500, false},
		{"agent has scope", fakeAuth{p: agent}, "GET", "/problems", 204, true},
		{"agent lacks scope", fakeAuth{p: agent}, "POST", "/problems", 403, false},
		{"literal pattern beats wildcard", fakeAuth{p: agent}, "POST", "/problems/similar", 204, true},
		{"any one of the scopes", fakeAuth{p: agent}, "GET", "/problems/abc", 204, true},
		{"agent never attempts:write", fakeAuth{p: agent}, "POST", "/attempts", 403, false},
		{"human holds all", fakeAuth{p: human}, "POST", "/attempts", 204, true},
		// HEAD resolves to the GET pattern the mux matched, so it is guarded the same.
		{"HEAD unauthenticated", none, "HEAD", "/problems", 401, false},
		{"HEAD missing scope", fakeAuth{p: other}, "HEAD", "/problems", 403, false},
		{"HEAD with scope", fakeAuth{p: agent}, "HEAD", "/problems", 204, true},
		// OPTIONS and wrong methods match no pattern: rejected by the router, even for a human.
		{"OPTIONS", fakeAuth{p: human}, "OPTIONS", "/problems", 405, false},
		{"wrong method", fakeAuth{p: human}, "DELETE", "/problems/abc", 405, false},
		{"unknown path", fakeAuth{p: human}, "GET", "/nope", 404, false},
		{"unknown path under known prefix", fakeAuth{p: human}, "GET", "/problems/a/b", 404, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.a)
			rec := h.do(tc.method, "/api/v1"+tc.path)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if got := h.reached.Load() == 1; got != tc.reaches {
				t.Fatalf("handler reached = %v, want %v", got, tc.reaches)
			}
			if tc.want == 401 && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
		})
	}
}

func TestScopeGuardPutsPrincipalInContext(t *testing.T) {
	ops, _ := openapi.Operations([]byte(guardSpec))
	rt := platform.NewRouter()
	want := Principal{Kind: KindAgent, ClientID: "c", Scopes: map[string]bool{"problems:read": true}}
	rt.SetAuthorizer(NewScopeGuard(fakeAuth{p: want}, ops).Authorize)
	var got Principal
	rt.HandleFunc("GET /problems", func(w http.ResponseWriter, r *http.Request) { got, _ = PrincipalFrom(r.Context()) })
	mux := http.NewServeMux()
	rt.Mount(mux)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/problems", nil))
	if got.ClientID != "c" {
		t.Errorf("principal = %+v", got)
	}
}

// TestScopeGuardDeniesUnspecifiedRoutes: a route registered on the router with
// no spec operation is never served.
func TestScopeGuardDeniesUnspecifiedRoutes(t *testing.T) {
	ops, _ := openapi.Operations([]byte(guardSpec))
	rt := platform.NewRouter()
	rt.SetAuthorizer(NewScopeGuard(fakeAuth{p: Principal{Kind: KindHuman}}, ops).Authorize)
	reached := false
	rt.HandleFunc("GET /secret", func(http.ResponseWriter, *http.Request) { reached = true })
	mux := http.NewServeMux()
	rt.Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/secret", nil))
	if reached || rec.Code != 404 {
		t.Errorf("reached=%v status=%d", reached, rec.Code)
	}
}

// TestScopeGuardAdversarialRequests sends raw requests through a real HTTP
// server: no variation of path encoding, prefix spelling, method or request
// form may reach a scoped handler without credentials.
func TestScopeGuardAdversarialRequests(t *testing.T) {
	h := newHarness(t, fakeAuth{err: ErrNoCredentials})
	srv := httptest.NewServer(h.mux)
	defer srv.Close()

	scoped := []string{"/problems", "/problems/abc", "/problems/similar", "/attempts"}
	var targets []string
	for _, s := range scoped {
		targets = append(targets,
			"/api/v1"+s,
			"/api/v1"+s+"/",
			"/%61pi/v1"+s,
			"/api/%76%31"+s,
			"/%41pi/v1"+s, // wrong case: no such prefix
			"/api/v1/."+s,
			"/api/v1/.."+s,
			"/api/v1/%2e%2e"+s,
			"/api/v1/%2E%2e"+s,
			"/api/v1/x/../"+s[1:],
			"/api/v1/./"+s[1:],
			"/api/v1/"+s, // "//"
			"//api/v1"+s, // leading "//"
			"/api//v1"+s,
			"/api/v1"+s+";x=1",
			"/api/v1"+s+"%3Bx",
			"/api/v1"+s+"%2f",
			"/api/v1"+s+"%2F",
			"/api/v1"+s+"%5c",
			"/api/v1"+strings.Replace(s, "/", "%2f", -1),
			"/api/v1"+strings.Replace(s, "/", "%2F", -1),
			"/api/v1"+strings.Replace(s, "/", "%5C", -1),
			"/api/v1"+s+"?x=%2f",
			"http://example.com/api/v1"+s, // absolute-form
			"http://example.com/%61pi/v1"+s,
		)
	}
	targets = append(targets,
		"/api/v1/problems/a%2Fb", "/api/v1/problems/a%2fb", "/api/v1/problems/a%5cb",
		"/api/v1/problems%2Fabc", "/api/v1/problems/%2e%2e/attempts", "/api/v1/problems/..%2fattempts",
		"/api/v1/problems/%00", "/api/v1/problems/%ff",
	)

	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "CONNECT"} {
		for _, target := range targets {
			status := rawRequest(t, srv.Listener.Addr().String(), method, target)
			if n := h.reached.Load(); n != 0 {
				t.Fatalf("%s %s reached a scoped handler without credentials (status %d)", method, target, status)
			}
		}
	}
	// CONNECT uses authority-form.
	rawRequest(t, srv.Listener.Addr().String(), "CONNECT", "example.com:443")
	if h.reached.Load() != 0 {
		t.Fatal("CONNECT reached a handler")
	}
}

// TestScopeGuardHarnessCanReachHandlers is the control for the adversarial
// test: with credentials the same server does reach the handlers.
func TestScopeGuardHarnessCanReachHandlers(t *testing.T) {
	h := newHarness(t, fakeAuth{p: Principal{Kind: KindHuman}})
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	if st := rawRequest(t, srv.Listener.Addr().String(), "GET", "/api/v1/problems"); st != 204 || h.reached.Load() != 1 {
		t.Fatalf("status=%d reached=%d", st, h.reached.Load())
	}
}

// rawRequest writes the request line verbatim (net/http clients would
// normalize it) and returns the status, or 0 when the server closed the
// connection without a response.
func rawRequest(t *testing.T, addr, method, target string) int {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	host := "example.com"
	fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", method, target, host)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: method})
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
