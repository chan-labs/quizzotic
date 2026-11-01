// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

type quizJSON struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	CreatedBy   struct {
		Kind     string `json:"kind"`
		ClientID string `json:"client_id"`
	} `json:"created_by"`
	Items []struct {
		ProblemID string `json:"problem_id"`
		Position  int    `json:"position"`
	} `json:"items"`
}

func (q quizJSON) problemIDs() []string {
	out := []string{}
	for i, it := range q.Items {
		if it.Position != i {
			panic("item position does not match its index")
		}
		out = append(out, it.ProblemID)
	}
	return out
}

func init() {
	// createProblem makes an open problem as the client and returns its ID.
	createProblem := func(t testing.TB, c *ownerscope.Client, prompt string) string {
		r := c.Do("POST", "/problems", openBody(prompt))
		if r.Code != http.StatusCreated {
			t.Fatalf("create problem = %d (%s)", r.Code, r.Body)
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(r.Body, &v); err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	ownerScope.Resource(ownerscope.Resource{
		Name: "quizzes",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			pid := createProblem(t, c, "Quiz owner-scope prompt")
			r := c.Do("POST", "/quizzes", map[string]any{
				"title": "Secret quiz title", "description": "Secret quiz description", "problem_ids": []string{pid}})
			if r.Code != http.StatusCreated {
				t.Fatalf("create quiz = %d (%s)", r.Code, r.Body)
			}
			var q quizJSON
			if err := json.Unmarshal(r.Body, &q); err != nil {
				t.Fatal(err)
			}
			return ownerscope.Created{
				Params:  ownerscope.Params{"id": q.ID, "problem": pid},
				Secrets: []string{"Secret quiz title", "Secret quiz description", "Quiz owner-scope prompt", pid},
				Header:  http.Header{"If-Match": {r.Header.Get("ETag")}},
			}
		},
		Ops: []ownerscope.Op{
			{Pattern: "GET /quizzes/{id}"},
			{Pattern: "PATCH /quizzes/{id}", Body: func(ownerscope.Created) any { return map[string]string{"title": "Changed"} }},
			{Pattern: "PUT /quizzes/{id}/items", Body: func(c ownerscope.Created) any {
				return map[string]any{"problem_ids": []string{c.Params["problem"]}}
			}},
			{Pattern: "DELETE /quizzes/{id}", WantOwner: http.StatusNoContent},
			{Pattern: "GET /quizzes", List: true},
			{Pattern: "POST /quizzes", List: true, WantOwner: http.StatusCreated, WantOther: http.StatusCreated,
				Body: func(ownerscope.Created) any { return map[string]string{"title": "Posted"} }},
		},
		Intact: func(t testing.TB, c *ownerscope.Client, created ownerscope.Created) {
			r := c.Do("GET", "/quizzes/"+created.Params["id"], nil)
			var q quizJSON
			if err := json.Unmarshal(r.Body, &q); err != nil || q.Title != "Secret quiz title" ||
				len(q.Items) != 1 || q.Items[0].ProblemID != created.Params["problem"] {
				t.Errorf("quiz changed by another user: %d %s", r.Code, r.Body)
			}
		},
	})
}

func (te tagsEnv) problem(t *testing.T, c *ownerscope.Client, prompt string) string {
	t.Helper()
	r := c.Do("POST", "/problems", openBody(prompt))
	return decode[struct {
		ID string `json:"id"`
	}](t, r, http.StatusCreated).ID
}

func (te tagsEnv) quiz(t *testing.T, c *ownerscope.Client, body map[string]any) (quizJSON, string) {
	t.Helper()
	r := c.Do("POST", "/quizzes", body)
	return decode[quizJSON](t, r, http.StatusCreated), r.Header.Get("ETag")
}

func (te tagsEnv) putItems(c *ownerscope.Client, qid, etag string, pids []string) ownerscope.Response {
	return c.DoWith("PUT", "/quizzes/"+qid+"/items", map[string]any{"problem_ids": pids}, http.Header{"If-Match": {etag}})
}

func TestQuizItemsKeepTheirOrder(t *testing.T) {
	te := newTagsEnv(t)
	p1, p2, p3 := te.problem(t, te.a, "one"), te.problem(t, te.a, "two"), te.problem(t, te.a, "three")

	created, etag := te.quiz(t, te.a, map[string]any{"title": "Order", "problem_ids": []string{p3, p1}})
	if created.Status != "draft" || created.CreatedBy.Kind != "human" || !slices.Equal(created.problemIDs(), []string{p3, p1}) {
		t.Fatalf("created = %+v", created)
	}

	// PUT replaces the list; the response and later reads show the new order.
	put := te.putItems(te.a, created.ID, etag, []string{p2, p3, p1})
	got := decode[quizJSON](t, put, http.StatusOK)
	if !slices.Equal(got.problemIDs(), []string{p2, p3, p1}) {
		t.Fatalf("put items = %v", got.problemIDs())
	}
	if put.Header.Get("ETag") == "" || put.Header.Get("ETag") == etag {
		t.Errorf("ETag after PUT = %q, want a new value (was %q)", put.Header.Get("ETag"), etag)
	}
	read := decode[quizJSON](t, te.a.Do("GET", "/quizzes/"+created.ID, nil), http.StatusOK)
	if !slices.Equal(read.problemIDs(), []string{p2, p3, p1}) {
		t.Errorf("get items = %v", read.problemIDs())
	}

	// A pure reorder, then a subset, then empty.
	etag = put.Header.Get("ETag")
	r := te.putItems(te.a, created.ID, etag, []string{p1, p2, p3})
	if got := decode[quizJSON](t, r, http.StatusOK); !slices.Equal(got.problemIDs(), []string{p1, p2, p3}) {
		t.Errorf("reorder = %v", got.problemIDs())
	}
	r = te.putItems(te.a, created.ID, r.Header.Get("ETag"), []string{p2})
	if got := decode[quizJSON](t, r, http.StatusOK); !slices.Equal(got.problemIDs(), []string{p2}) {
		t.Errorf("subset = %v", got.problemIDs())
	}
	r = te.putItems(te.a, created.ID, r.Header.Get("ETag"), []string{})
	if got := decode[quizJSON](t, r, http.StatusOK); len(got.Items) != 0 {
		t.Errorf("empty = %v", got.problemIDs())
	}

	// The list carries items in order too.
	te.putItems(te.a, created.ID, r.Header.Get("ETag"), []string{p3, p2})
	page := decode[struct {
		Items []quizJSON `json:"items"`
	}](t, te.a.Do("GET", "/quizzes", nil), http.StatusOK)
	if len(page.Items) != 1 || !slices.Equal(page.Items[0].problemIDs(), []string{p3, p2}) {
		t.Errorf("list = %+v", page.Items)
	}
}

func TestQuizRejectsAnotherUsersProblems(t *testing.T) {
	te := newTagsEnv(t)
	mine, theirs := te.problem(t, te.a, "mine"), te.problem(t, te.b, "theirs")
	created, etag := te.quiz(t, te.a, map[string]any{"title": "Guarded", "problem_ids": []string{mine}})

	field := func(r ownerscope.Response) []string {
		wantProblem(t, r, http.StatusUnprocessableEntity, "validation")
		var p struct {
			Errors []struct {
				Field string `json:"field"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(r.Body, &p); err != nil {
			t.Fatal(err)
		}
		var fs []string
		for _, e := range p.Errors {
			fs = append(fs, e.Field)
		}
		return fs
	}

	// Another user's problem names its index; the body never says it exists.
	r := te.putItems(te.a, created.ID, etag, []string{mine, theirs})
	if fs := field(r); !slices.Equal(fs, []string{"problem_ids[1]"}) {
		t.Errorf("fields = %v", fs)
	}
	if strings.Contains(string(r.Body), theirs) {
		t.Errorf("body echoes the other user's problem ID: %s", r.Body)
	}
	// Unknown, malformed and duplicate entries are rejected the same way.
	for name, pids := range map[string][]string{
		"unknown":   {"0190a000-0000-7000-8000-000000000000"},
		"malformed": {"not-an-id"},
		"duplicate": {mine, mine},
	} {
		if fs := field(te.putItems(te.a, created.ID, etag, pids)); len(fs) == 0 {
			t.Errorf("%s: no field errors", name)
		}
	}
	// Creating with another user's problem is rejected, and creates nothing.
	field(te.a.Do("POST", "/quizzes", map[string]any{"title": "Bad", "problem_ids": []string{theirs}}))
	page := decode[struct {
		Items []quizJSON `json:"items"`
	}](t, te.a.Do("GET", "/quizzes", nil), http.StatusOK)
	if len(page.Items) != 1 {
		t.Errorf("quizzes after rejected create = %d, want 1", len(page.Items))
	}

	// Nothing changed, and the ETag from before still works.
	read := decode[quizJSON](t, te.a.Do("GET", "/quizzes/"+created.ID, nil), http.StatusOK)
	if !slices.Equal(read.problemIDs(), []string{mine}) {
		t.Errorf("items after rejections = %v", read.problemIDs())
	}
	decode[quizJSON](t, te.putItems(te.a, created.ID, etag, []string{mine}), http.StatusOK)

	// B cannot touch A's quiz even with B's own problem: 404, not 422.
	if r := te.putItems(te.b, created.ID, etag, []string{theirs}); r.Code != http.StatusNotFound {
		t.Errorf("b puts a's quiz = %d, want 404", r.Code)
	}
}

func TestQuizPreconditionsAndLifecycle(t *testing.T) {
	te := newTagsEnv(t)
	created, etag := te.quiz(t, te.a, map[string]any{"title": "  Padded  ", "description": "d", "status": "live"})
	if created.Title != "Padded" || created.Status != "live" {
		t.Fatalf("created = %+v", created)
	}
	path := "/quizzes/" + created.ID
	patch := map[string]any{"title": "Renamed"}

	// 428 without If-Match, 412 with a stale one, for every write.
	for _, do := range map[string]func(h http.Header) ownerscope.Response{
		"PATCH": func(h http.Header) ownerscope.Response { return te.a.DoWith("PATCH", path, patch, h) },
		"PUT": func(h http.Header) ownerscope.Response {
			return te.a.DoWith("PUT", path+"/items", map[string]any{"problem_ids": []string{}}, h)
		},
		"DELETE": func(h http.Header) ownerscope.Response { return te.a.DoWith("DELETE", path, nil, h) },
	} {
		wantProblem(t, do(nil), http.StatusPreconditionRequired, "precondition-required")
		wantProblem(t, do(http.Header{"If-Match": {`"stale"`}}), http.StatusPreconditionFailed, "precondition-failed")
	}

	r := te.a.DoWith("PATCH", path, patch, http.Header{"If-Match": {etag}})
	renamed := decode[quizJSON](t, r, http.StatusOK)
	if renamed.Title != "Renamed" || renamed.Status != "live" || renamed.Description != "d" {
		t.Errorf("patched = %+v", renamed)
	}
	// The old ETag is stale after the change.
	wantProblem(t, te.a.DoWith("PATCH", path, patch, http.Header{"If-Match": {etag}}), http.StatusPreconditionFailed, "precondition-failed")

	// Validation.
	etag = r.Header.Get("ETag")
	for name, body := range map[string]map[string]any{
		"blank title": {"title": " "},
		"bad status":  {"status": "bogus"},
		"empty patch": {},
	} {
		if r := te.a.DoWith("PATCH", path, body, http.Header{"If-Match": {etag}}); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d (%s)", name, r.Code, r.Body)
		}
	}
	if r := te.a.Do("POST", "/quizzes", map[string]any{"title": ""}); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("blank title create = %d", r.Code)
	}

	// Status filter.
	te.quiz(t, te.a, map[string]any{"title": "Other"})
	page := decode[struct {
		Items []quizJSON `json:"items"`
	}](t, te.a.Do("GET", "/quizzes?status=live", nil), http.StatusOK)
	if len(page.Items) != 1 || page.Items[0].ID != created.ID {
		t.Errorf("live quizzes = %+v", page.Items)
	}

	// Delete hides the quiz.
	if r := te.a.DoWith("DELETE", path, nil, http.Header{"If-Match": {etag}}); r.Code != http.StatusNoContent {
		t.Fatalf("delete = %d (%s)", r.Code, r.Body)
	}
	if r := te.a.Do("GET", path, nil); r.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", r.Code)
	}
	if r := te.a.DoWith("DELETE", path, nil, http.Header{"If-Match": {etag}}); r.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", r.Code)
	}
}
