// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorProblem(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/problems/x", nil)
	r = r.WithContext(WithRequestID(r.Context(), "req-1"))
	w := httptest.NewRecorder()
	WriteError(w, r, fmt.Errorf("wrap: %w", NotFound("problem x not found")))

	if w.Code != 404 {
		t.Fatalf("code %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type %q", ct)
	}
	var p Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != TypeNotFound || p.Status != 404 || p.Title != "Not Found" ||
		p.Detail != "problem x not found" || p.Instance != "/api/v1/problems/x" || p.RequestID != "req-1" {
		t.Fatalf("unexpected %+v", p)
	}
}

func TestWriteErrorHidesInternals(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	WriteError(w, r, errors.New("pq: password authentication failed"))
	if w.Code != 500 || strings.Contains(w.Body.String(), "password") {
		t.Fatalf("leaked or wrong code: %d %s", w.Code, w.Body)
	}
	var p Problem
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Type != TypeInternal {
		t.Fatalf("type %q", p.Type)
	}
}

func TestWriteErrorValidationFieldsAndNoMutation(t *testing.T) {
	shared := Validation("bad", FieldError{Field: "prompt", Detail: "required"})
	for _, path := range []string{"/a", "/b"} {
		w := httptest.NewRecorder()
		WriteError(w, httptest.NewRequest(http.MethodPost, path, nil), shared)
		var p Problem
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		if w.Code != 422 || p.Instance != path || len(p.Errors) != 1 || p.Errors[0].Field != "prompt" {
			t.Fatalf("%s: %d %+v", path, w.Code, p)
		}
	}
	if shared.Instance != "" {
		t.Fatal("shared problem mutated")
	}
}

func TestProblemTypesStableAndDistinct(t *testing.T) {
	types := []string{TypeBadRequest, TypeValidation, TypeUnauthorized, TypeForbidden, TypeNotFound,
		TypeMethodNotAllowed, TypeConflict, TypePreconditionFail, TypePreconditionReq, TypeTooLarge, TypeInternal}
	seen := map[string]bool{}
	for _, ty := range types {
		if !strings.HasPrefix(ty, "urn:quizzotic:problem:") || seen[ty] {
			t.Fatalf("bad or duplicate type %q", ty)
		}
		seen[ty] = true
	}
}

func TestParsePageBounds(t *testing.T) {
	cases := []struct {
		query   string
		limit   int
		cursor  string
		wantErr bool
	}{
		{"", 50, "", false},
		{"limit=1", 1, "", false},
		{"limit=200&cursor=abc", 200, "abc", false},
		{"limit=0", 0, "", true},
		{"limit=-5", 0, "", true},
		{"limit=201", 0, "", true},
		{"limit=abc", 0, "", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/x?"+c.query, nil)
		p, err := ParsePage(r)
		if c.wantErr {
			var pr *Problem
			if !errors.As(err, &pr) || pr.Status != 422 || pr.Errors[0].Field != "limit" {
				t.Errorf("%q: want validation error, got %v", c.query, err)
			}
			continue
		}
		if err != nil || p.Limit != c.limit || p.Cursor != c.cursor {
			t.Errorf("%q: got %+v, %v", c.query, p, err)
		}
	}
}

func TestPageResultJSON(t *testing.T) {
	b, _ := json.Marshal(NewPageResult[int](nil, ""))
	if string(b) != `{"items":[],"next_cursor":null}` {
		t.Fatalf("%s", b)
	}
	b, _ = json.Marshal(NewPageResult([]int{1, 2}, "c2"))
	if string(b) != `{"items":[1,2],"next_cursor":"c2"}` {
		t.Fatalf("%s", b)
	}
}

type body struct {
	Name string `json:"name"`
}

func decode(t *testing.T, s string) (body, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(s))
	var b body
	err := DecodeJSON(httptest.NewRecorder(), r, &b)
	return b, err
}

func TestDecodeJSONStrict(t *testing.T) {
	if b, err := decode(t, `{"name":"a"}`); err != nil || b.Name != "a" {
		t.Fatalf("valid: %v %+v", err, b)
	}
	bad := map[string]string{
		"unknown field": `{"name":"a","extra":1}`,
		"empty":         ``,
		"malformed":     `{"name":`,
		"wrong type":    `{"name":5}`,
		"trailing":      `{"name":"a"} {"name":"b"}`,
	}
	for name, in := range bad {
		_, err := decode(t, in)
		var p *Problem
		if !errors.As(err, &p) || p.Status != 400 || p.Type != TypeBadRequest {
			t.Errorf("%s: got %v", name, err)
		}
	}
	_, err := decode(t, `{"name":"`+strings.Repeat("a", MaxBodyBytes)+`"}`)
	var p *Problem
	if !errors.As(err, &p) || p.Status != 413 {
		t.Errorf("oversize: got %v", err)
	}
}

func TestRouterMountsUnderPrefix(t *testing.T) {
	rt := NewRouter()
	rt.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("thing " + r.PathValue("id")))
	})
	mux := http.NewServeMux()
	rt.Mount(mux)

	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	if w := do("GET", "/api/v1/things/42"); w.Code != 200 || w.Body.String() != "thing 42" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w := do("GET", "/api/v1/nope")
	if w.Code != 404 || w.Header().Get("Content-Type") != ProblemContentType {
		t.Fatalf("404: %d %s", w.Code, w.Header())
	}
	w = do("DELETE", "/api/v1/things/42")
	var p Problem
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != 405 || p.Type != TypeMethodNotAllowed || !strings.Contains(w.Header().Get("Allow"), "GET") {
		t.Fatalf("405: %d %+v %v", w.Code, p, w.Header())
	}
	if w := do("GET", "/things/42"); w.Code != 404 {
		t.Fatalf("unprefixed should not route: %d", w.Code)
	}
}
