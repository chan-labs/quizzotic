// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The cloze match mode through the real server: stored and returned as
// `match`, legacy `case_sensitive` accepted on input and never returned, and
// graded per mode.
func TestClozeMatchModeEndToEnd(t *testing.T) {
	e := newProbEnv(t)

	mk := func(blank map[string]any) (id string, raw string) {
		t.Helper()
		body := map[string]any{"type": "cloze", "status": "live", "prompt": "Capital: {{w}}",
			"payload": map[string]any{"blanks": []map[string]any{blank}}}
		v := e.create(t, e.human, body)
		r := e.req(t, "GET", "/problems/"+v.ID.String(), e.human, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("get = %d (%s)", r.code, r.body)
		}
		return v.ID.String(), string(r.body)
	}
	correct := func(id, ans string) bool {
		t.Helper()
		r := e.req(t, "POST", "/attempts", e.human, map[string]any{
			"problem_id": id, "problem_version": 1, "latency_ms": 100,
			"response": map[string]any{"answers": map[string]string{"w": ans}}}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("answer %q = %d (%s)", ans, r.code, r.body)
		}
		var out struct {
			Grade struct {
				Correct *bool `json:"correct"`
			} `json:"grade"`
		}
		if err := json.Unmarshal(r.body, &out); err != nil || out.Grade.Correct == nil {
			t.Fatalf("answer %q: %s (%v)", ans, r.body, err)
		}
		return *out.Grade.Correct
	}

	t.Run("default is relaxed", func(t *testing.T) {
		id, body := mk(map[string]any{"id": "w", "accepted": []string{"Paris"}})
		if !strings.Contains(body, `"match":"relaxed"`) || strings.Contains(body, "case_sensitive") {
			t.Errorf("body = %s", body)
		}
		if !correct(id, "  paris. ") {
			t.Error("relaxed should ignore case, spacing and trailing punctuation")
		}
	})

	t.Run("case-insensitive ignores case only", func(t *testing.T) {
		id, body := mk(map[string]any{"id": "w", "accepted": []string{"New York"}, "match": "case-insensitive"})
		if !strings.Contains(body, `"match":"case-insensitive"`) {
			t.Errorf("body = %s", body)
		}
		if !correct(id, "NEW YORK") {
			t.Error("case must not matter")
		}
		for _, ans := range []string{"new york ", " new york", "new  york", "new york."} {
			if correct(id, ans) {
				t.Errorf("%q accepted: whitespace and punctuation must match exactly", ans)
			}
		}
	})

	t.Run("legacy case_sensitive is accepted and stored as relaxed", func(t *testing.T) {
		for _, legacy := range []bool{true, false} {
			id, body := mk(map[string]any{"id": "w", "accepted": []string{"Paris"}, "case_sensitive": legacy})
			if !strings.Contains(body, `"match":"relaxed"`) || strings.Contains(body, "case_sensitive") {
				t.Errorf("case_sensitive=%v: body = %s", legacy, body)
			}
			if !correct(id, "PARIS") {
				t.Errorf("case_sensitive=%v: case must not matter any more", legacy)
			}
		}
	})

	t.Run("match wins over legacy", func(t *testing.T) {
		_, body := mk(map[string]any{"id": "w", "accepted": []string{"x"}, "case_sensitive": true, "match": "case-insensitive"})
		if !strings.Contains(body, `"match":"case-insensitive"`) || strings.Contains(body, "case_sensitive") {
			t.Errorf("body = %s", body)
		}
	})

	t.Run("unknown match is a 422", func(t *testing.T) {
		r := e.req(t, "POST", "/problems", e.human, map[string]any{"type": "cloze", "prompt": "{{w}}",
			"payload": map[string]any{"blanks": []map[string]any{{"id": "w", "accepted": []string{"x"}, "match": "exact"}}}}, nil)
		if r.code != http.StatusUnprocessableEntity || !strings.Contains(string(r.body), "payload.blanks[0].match") {
			t.Errorf("status = %d (%s)", r.code, r.body)
		}
	})

	t.Run("editing with the legacy key stores the new form", func(t *testing.T) {
		id, _ := mk(map[string]any{"id": "w", "accepted": []string{"a"}, "match": "case-insensitive"})
		etag := e.req(t, "GET", "/problems/"+id, e.human, nil, nil).header.Get("ETag")
		r := e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"change_kind": "minor",
			"payload": map[string]any{"blanks": []map[string]any{{"id": "w", "accepted": []string{"b"}, "case_sensitive": true}}}},
			map[string]string{"If-Match": etag})
		if r.code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", r.code, r.body)
		}
		if !strings.Contains(string(r.body), `"match":"relaxed"`) || strings.Contains(string(r.body), "case_sensitive") {
			t.Errorf("body = %s", r.body)
		}
	})
}
