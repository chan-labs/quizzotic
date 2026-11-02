// SPDX-License-Identifier: Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/cli"
)

// reviewServer fakes the review endpoints. Problem p1 is shown at v1; the
// first POST /attempts answers 409 stale-problem-version (it was edited to
// v2); an answer at v2 succeeds. With alwaysStale every answer is refused.
func reviewServer(t *testing.T, alwaysStale bool) (*httptest.Server, *[]int) {
	t.Helper()
	version := 1
	var submitted []int
	answered := false
	mux := http.NewServeMux()
	js := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) { js(w, 200, map[string]any{"kind": "human"}) })
	mux.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		js(w, 201, map[string]any{"id": "s1", "items": []any{map[string]any{"kind": "new"}}})
	})
	mux.HandleFunc("GET /api/v1/sessions/s1/next", func(w http.ResponseWriter, r *http.Request) {
		if answered {
			js(w, 200, map[string]any{"remaining": 0})
			return
		}
		js(w, 200, map[string]any{"remaining": 1, "item": map[string]any{"kind": "new", "problem": map[string]any{
			"id": "p1", "version": version, "type": "open", "prompt": "Why?"}}})
	})
	mux.HandleFunc("POST /api/v1/attempts", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Version int `json:"problem_version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		submitted = append(submitted, b.Version)
		if alwaysStale || b.Version != 2 {
			version = 2
			js(w, 409, map[string]any{"type": "urn:quizzotic:problem:stale-problem-version", "title": "Conflict", "detail": "stale"})
			return
		}
		answered = true
		js(w, 201, map[string]any{"attempt": map[string]any{"id": "a1"}, "grade": map[string]any{"correct": false}})
	})
	mux.HandleFunc("POST /api/v1/sessions/s1/complete", func(w http.ResponseWriter, r *http.Request) { js(w, 200, map[string]any{}) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &submitted
}

func reviewWith(srv *httptest.Server, stdin string) (int, string, string) {
	var o, e bytes.Buffer
	code := cli.Review(context.Background(), []string{"--server", srv.URL}, cli.Env{
		Stdin: strings.NewReader(stdin), Stdout: &o, Stderr: &e,
		Getenv: func(k string) string {
			if k == "QUIZZOTIC_TOKEN" {
				return "tok"
			}
			return ""
		},
	})
	return code, o.String(), e.String()
}

func TestReviewContinuesAfterStaleProblem(t *testing.T) {
	srv, submitted := reviewServer(t, false)
	code, out, errOut := reviewWith(srv, "my answer\nmy answer\n")
	if code != 0 {
		t.Fatalf("code %d, stderr %q, stdout %q", code, errOut, out)
	}
	if len(*submitted) != 2 || (*submitted)[0] != 1 || (*submitted)[1] != 2 {
		t.Errorf("submitted versions = %v, want [1 2]", *submitted)
	}
	if !strings.Contains(out, "edited since it was shown") {
		t.Errorf("no stale notice in %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "answered") {
		t.Errorf("run did not reach the summary: %q", out)
	}
}

func TestReviewGivesUpOnEndlessStale(t *testing.T) {
	srv, submitted := reviewServer(t, true)
	code, _, errOut := reviewWith(srv, strings.Repeat("x\n", 20))
	if code != 1 || !strings.Contains(errOut, "409") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if len(*submitted) != 4 {
		t.Errorf("submissions = %d, want 4 (1 + 3 retries)", len(*submitted))
	}
}

func TestLoginHelpMentionsConfigFile(t *testing.T) {
	var o, e bytes.Buffer
	cli.Login(context.Background(), []string{"-h"}, cli.Env{Stdin: strings.NewReader(""), Stdout: &o, Stderr: &e,
		Getenv: func(string) string { return "" }})
	help := e.String()
	for _, want := range []string{"issuer: in config.yaml", "login_client_id: in config.yaml"} {
		if !strings.Contains(help, want) {
			t.Errorf("login help lacks %q:\n%s", want, help)
		}
	}
}
