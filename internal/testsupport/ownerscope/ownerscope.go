// SPDX-License-Identifier: Apache-2.0

// Package ownerscope is the test helper that guards owner-scoped data
// (docs/spec.md, design principle 6). A feature test registers each of its
// endpoints once; the helper then drives the real server wiring as two users
// and asserts that user B can never see or touch user A's data.
//
// It is test support only: production code must not import it.
//
// Two kinds of endpoint exist:
//
//   - Resource endpoints address A's data by ID (GET /problems/{id}) or list a
//     collection. Register them with Registry.Resource. For every operation
//     the helper asserts: unauthenticated gets 401; user B gets 404 (never 403
//     or 2xx) with a problem body that leaks none of A's data; user A gets the
//     expected success status, so the check cannot pass vacuously.
//   - Self endpoints have no ID and answer for the caller (GET /me). Register
//     them with Registry.Self. The helper asserts each caller sees only their
//     own identity.
//
// Registry.VerifyCoverage fails closed: every non-public operation in the
// OpenAPI spec must be registered or exempted with a reason.
package ownerscope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/openapi"
)

// User selects which of the two test users (or nobody) makes a request.
type User int

const (
	Anonymous User = iota
	UserA
	UserB
)

func (u User) String() string { return [...]string{"anonymous", "user A", "user B"}[u] }

// Identity is what the server knows about a test user, read after the user's
// first request has provisioned them.
type Identity struct {
	ID          string
	Email       string
	DisplayName string
}

// Env is the running system under test. The caller builds it from the real
// server wiring, a fake IdP and SQLite, and must make UserA and UserB distinct
// users with distinct emails and names.
type Env struct {
	// BaseURL is the server root, without a trailing slash.
	BaseURL string
	// APIPrefix defaults to "/api/v1".
	APIPrefix string
	// Credentials returns a function that authenticates a request as u.
	Credentials func(t testing.TB, u User) func(*http.Request)
	// Identity returns u's identity. It is called after u has made a request.
	Identity func(t testing.TB, u User) Identity
}

// Response is a recorded HTTP reply.
type Response struct {
	Code   int
	Header http.Header
	Body   []byte
}

// Client issues requests to the API as one user.
type Client struct {
	T    testing.TB
	Env  *Env
	User User
}

// Do sends method to the API-relative path (e.g. "/problems/abc") with an
// optional JSON body, never following redirects.
func (c *Client) Do(method, path string, body any) Response {
	c.T.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.T.Errorf("ownerscope: marshal body: %v", err)
			return Response{}
		}
		rd = bytes.NewReader(b)
	}
	prefix := c.Env.APIPrefix
	if prefix == "" {
		prefix = "/api/v1"
	}
	req, err := http.NewRequest(method, c.Env.BaseURL+prefix+path, rd)
	if err != nil {
		c.T.Errorf("ownerscope: new request: %v", err)
		return Response{}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.User != Anonymous {
		c.Env.Credentials(c.T, c.User)(req)
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		c.T.Errorf("ownerscope: %s %s: %v", method, path, err)
		return Response{}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return Response{resp.StatusCode, resp.Header, b}
}

// Params are path parameter values, keyed by name without braces.
type Params map[string]string

// Created describes a resource just created as user A.
type Created struct {
	// Params fill the {placeholders} of every Op pattern (e.g. {"id": "..."}).
	Params Params
	// Secrets are strings drawn from A's data (a prompt, a title, an ID) that
	// must never appear in any response user B receives.
	Secrets []string
}

// Op is one operation on a resource.
type Op struct {
	// Pattern is "METHOD /path" exactly as in the OpenAPI spec, e.g.
	// "PATCH /problems/{id}". It is also the registration key.
	Pattern string
	// Body builds the JSON request body, or nil for none.
	Body func(Created) any
	// List marks a collection endpoint that has no ID to guess: user B gets a
	// successful reply (WantOther, default 200) that must not contain A's
	// data, instead of 404.
	List bool
	// WantOwner is the success status for user A (default 200).
	WantOwner int
	// WantOther is the status for user B on a List op (default 200).
	WantOther int
}

// Resource declares owner-scoped endpoints that share a creator.
type Resource struct {
	Name string
	// Create makes a fresh resource as user A (c.User == UserA). It is called
	// once per Op, so destructive ops do not disturb each other.
	Create func(t testing.TB, c *Client) Created
	Ops    []Op
	// Intact, if set, asserts as user A that the resource is unchanged after
	// user B's attempts; it runs before A's own request. Use it for update and
	// delete ops.
	Intact func(t testing.TB, c *Client, created Created)
}

// Self declares an endpoint that answers for the caller and takes no ID.
type Self struct {
	// Pattern is "METHOD /path" as in the spec, e.g. "GET /me".
	Pattern string
	// UserID extracts, from a 200 body, the ID of the user it describes.
	UserID func(body []byte) string
}

// Registry collects registrations. Declare a package-level Registry in the
// server tests and register from init functions, so each feature adds its own.
type Registry struct {
	resources []Resource
	selfs     []Self
	exempt    map[string]string
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{exempt: map[string]string{}} }

// Resource registers owner-scoped, by-ID or collection endpoints.
func (r *Registry) Resource(res Resource) { r.resources = append(r.resources, res) }

// Self registers a caller-relative endpoint.
func (r *Registry) Self(s Self) { r.selfs = append(r.selfs, s) }

// Exempt records that an authenticated spec operation needs no owner-scoping
// test, and why. The reason must be non-empty and explain why no user-owned
// data can be reached (for example "admin only: not owner-scoped, see spec").
func (r *Registry) Exempt(pattern, reason string) {
	if strings.TrimSpace(reason) == "" {
		panic("ownerscope: exemption for " + pattern + " needs a reason")
	}
	r.exempt[pattern] = reason
}

func (r *Registry) registered() map[string]string {
	m := map[string]string{}
	for _, res := range r.resources {
		for _, op := range res.Ops {
			m[op.Pattern] = "resource " + res.Name
		}
	}
	for _, s := range r.selfs {
		m[s.Pattern] = "self"
	}
	return m
}

// Coverage lists the problems with the registry against the spec: a
// non-public operation that is neither registered nor exempted, a pattern
// registered twice or both ways, and a registration or exemption that names no
// operation. A public operation (no security scheme and no scopes) needs
// nothing. It returns nil when the registry is complete.
func (r *Registry) Coverage(ops []openapi.Operation) []string {
	spec := map[string]bool{}
	reg := r.registered()
	var problems []string
	for _, o := range ops {
		p := o.Method + " " + o.Path
		spec[p] = true
		if !o.Authenticated && len(o.Scopes) == 0 {
			continue
		}
		_, isReg := reg[p]
		_, isEx := r.exempt[p]
		switch {
		case !isReg && !isEx:
			problems = append(problems, fmt.Sprintf("%s: not registered with ownerscope; add Registry.Resource/Self or Registry.Exempt with a reason", p))
		case isReg && isEx:
			problems = append(problems, fmt.Sprintf("%s: both registered and exempted", p))
		}
	}
	seen := map[string]bool{}
	for _, res := range r.resources {
		for _, op := range res.Ops {
			if seen[op.Pattern] {
				problems = append(problems, op.Pattern+": registered more than once")
			}
			seen[op.Pattern] = true
		}
	}
	for _, s := range r.selfs {
		if seen[s.Pattern] {
			problems = append(problems, s.Pattern+": registered more than once")
		}
		seen[s.Pattern] = true
	}
	for p := range reg {
		if !spec[p] {
			problems = append(problems, p+": registered but not a spec operation")
		}
	}
	for p := range r.exempt {
		if !spec[p] {
			problems = append(problems, p+": exempted but not a spec operation")
		}
	}
	sort.Strings(problems)
	return problems
}

// VerifyCoverage fails t for every problem Coverage reports. Run it in a test
// that uses openapi.SpecOperations(); it makes forgetting to register a new
// endpoint a test failure.
func (r *Registry) VerifyCoverage(t testing.TB, ops []openapi.Operation) {
	t.Helper()
	for _, p := range r.Coverage(ops) {
		t.Errorf("ownerscope coverage: %s", p)
	}
}

// Run executes every registration against env as subtests.
func (r *Registry) Run(t *testing.T, env *Env) {
	t.Helper()
	for _, res := range r.resources {
		for _, op := range res.Ops {
			t.Run(res.Name+"/"+op.Pattern, func(t *testing.T) { checkOp(t, env, res, op) })
		}
	}
	for _, s := range r.selfs {
		t.Run("self/"+s.Pattern, func(t *testing.T) { checkSelf(t, env, s) })
	}
}

// RunWith runs every registration against env on t without subtests. The
// helper's own tests use it with a failure recorder; feature tests use Run.
func (r *Registry) RunWith(t testing.TB, env *Env) {
	t.Helper()
	for _, res := range r.resources {
		for _, op := range res.Ops {
			checkOp(t, env, res, op)
		}
	}
	for _, s := range r.selfs {
		checkSelf(t, env, s)
	}
}

func split(pattern string) (method, path string) {
	m, p, ok := strings.Cut(pattern, " ")
	if !ok || !strings.HasPrefix(p, "/") {
		panic("ownerscope: pattern must be \"METHOD /path\": " + pattern)
	}
	return m, p
}

func fill(path string, p Params) string {
	for k, v := range p {
		path = strings.ReplaceAll(path, "{"+k+"}", v)
	}
	if strings.Contains(path, "{") {
		panic("ownerscope: unfilled path parameter in " + path)
	}
	return path
}

func checkOp(t testing.TB, env *Env, res Resource, op Op) {
	t.Helper()
	a := &Client{t, env, UserA}
	b := &Client{t, env, UserB}
	anon := &Client{t, env, Anonymous}

	created := res.Create(t, a)
	if len(created.Secrets) == 0 {
		t.Errorf("ownerscope: %s: Create returned no Secrets; the leak check would be vacuous", res.Name)
	}
	method, tmpl := split(op.Pattern)
	path := fill(tmpl, created.Params)
	var body any
	if op.Body != nil {
		body = op.Body(created)
	}

	// Unauthenticated: 401, no leak.
	if r := anon.Do(method, path, body); r.Code != http.StatusUnauthorized {
		t.Errorf("%s unauthenticated = %d, want 401 (%s)", op.Pattern, r.Code, r.Body)
	} else {
		noLeak(t, op.Pattern+" unauthenticated", r, created.Secrets)
	}

	// User B: 404 for A's resource, or a clean success for a collection.
	rb := b.Do(method, path, body)
	if op.List {
		want := orDefault(op.WantOther, http.StatusOK)
		if rb.Code != want {
			t.Errorf("%s as user B = %d, want %d (%s)", op.Pattern, rb.Code, want, rb.Body)
		}
	} else {
		if rb.Code != http.StatusNotFound {
			t.Errorf("%s as user B = %d, want 404 (not 403, not success) (%s)", op.Pattern, rb.Code, rb.Body)
		}
		if ct := rb.Header.Get("Content-Type"); rb.Code == http.StatusNotFound && !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s as user B: 404 content-type = %q, want problem+json", op.Pattern, ct)
		}
	}
	noLeak(t, op.Pattern+" as user B", rb, created.Secrets)

	if res.Intact != nil && !op.List && method != http.MethodGet {
		res.Intact(t, a, created)
	}

	// Sanity: the owner succeeds, so the checks above cannot pass vacuously.
	want := orDefault(op.WantOwner, http.StatusOK)
	if ra := a.Do(method, path, body); ra.Code != want {
		t.Errorf("%s as user A = %d, want %d (%s)", op.Pattern, ra.Code, want, ra.Body)
	}
}

func checkSelf(t testing.TB, env *Env, s Self) {
	t.Helper()
	method, path := split(s.Pattern)
	if r := (&Client{t, env, Anonymous}).Do(method, path, nil); r.Code != http.StatusUnauthorized {
		t.Errorf("%s unauthenticated = %d, want 401", s.Pattern, r.Code)
	}
	ra := (&Client{t, env, UserA}).Do(method, path, nil)
	rb := (&Client{t, env, UserB}).Do(method, path, nil)
	if ra.Code != http.StatusOK || rb.Code != http.StatusOK {
		t.Errorf("%s = %d for user A, %d for user B, want 200 for both", s.Pattern, ra.Code, rb.Code)
		return
	}
	ia, ib := env.Identity(t, UserA), env.Identity(t, UserB)
	if ia.ID == "" || ib.ID == "" || ia.ID == ib.ID || ia.Email == ib.Email || ia.DisplayName == ib.DisplayName {
		t.Errorf("ownerscope: users A and B must have distinct IDs, emails and names: %+v %+v", ia, ib)
		return
	}
	if got := s.UserID(ra.Body); got != ia.ID {
		t.Errorf("%s as user A describes %q, want %q", s.Pattern, got, ia.ID)
	}
	if got := s.UserID(rb.Body); got != ib.ID {
		t.Errorf("%s as user B describes %q, want %q", s.Pattern, got, ib.ID)
	}
	noLeak(t, s.Pattern+" as user B", rb, []string{ia.ID, ia.Email, ia.DisplayName})
	noLeak(t, s.Pattern+" as user A", ra, []string{ib.ID, ib.Email, ib.DisplayName})
}

func noLeak(t testing.TB, what string, r Response, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && bytes.Contains(r.Body, []byte(s)) {
			t.Errorf("%s leaks %q: %s", what, s, r.Body)
		}
	}
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}
