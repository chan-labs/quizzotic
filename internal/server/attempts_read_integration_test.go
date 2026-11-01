// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// listAttempts GETs /attempts, requires status want, and returns the attempt
// IDs in order plus the next cursor.
func (e *probEnv) listAttempts(t *testing.T, tok string, q url.Values, want int) ([]string, *string) {
	t.Helper()
	path := "/attempts"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	r := e.req(t, "GET", path, tok, nil, nil)
	if r.code != want {
		t.Fatalf("GET %s = %d, want %d (%s)", path, r.code, want, r.body)
	}
	if want != http.StatusOK {
		return nil, nil
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(r.body, &page); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(page.Items))
	for i, it := range page.Items {
		ids[i] = it.ID
	}
	return ids, page.NextCursor
}

// TestAttemptsAnswerEveryTypeThenListAndGet is the M3 exit check: one attempt
// of each type is answered and scored through the API, then listed and
// fetched back, with filters, pagination and the read-only agent scope.
func TestAttemptsAnswerEveryTypeThenListAndGet(t *testing.T) {
	e := newProbEnv(t)
	live := func(body map[string]any) problems.View {
		body["status"] = "live"
		return e.create(t, e.human, body)
	}
	mcq := live(mcqBody("Which letter is third?", false))
	cloze := live(clozeBody("The seat of memory is the {{w}}."))
	open := live(openBody("Explain spacing."))

	post := func(p problems.View, response any, extra map[string]any) attemptResult {
		t.Helper()
		r := e.answer(t, e.human, attemptBody(p, response, extra))
		if r.code != http.StatusCreated {
			t.Fatalf("answer = %d (%s)", r.code, r.body)
		}
		res := decodeReply[attemptResult](t, r)
		if loc := r.header.Get("Location"); loc != "/api/v1/attempts/"+res.Attempt.ID {
			t.Errorf("Location = %q, want /api/v1/attempts/%s", loc, res.Attempt.ID)
		}
		return res
	}
	a1 := post(mcq, map[string]any{"selected": []string{"c"}}, nil)
	a2 := post(cloze, map[string]any{"answers": map[string]string{"w": "hippocampus"}}, nil)
	a3 := post(open, map[string]any{"text": "Reviews spread out."}, map[string]any{"rating": 3})
	a4 := post(mcq, map[string]any{"selected": []string{"a"}}, nil)
	all := []string{a1.Attempt.ID, a2.Attempt.ID, a3.Attempt.ID, a4.Attempt.ID}

	if !*a1.Grade.Correct || !*a2.Grade.Correct || a3.Grade.Correct != nil || *a4.Grade.Correct {
		t.Fatalf("grades: %+v %+v %+v %+v", a1.Grade, a2.Grade, a3.Grade, a4.Grade)
	}
	if *a1.Grade.Score != 1 || *a2.Grade.Score != 1 || *a3.Grade.Score != 0.67 || *a4.Grade.Score != 0 {
		t.Errorf("scores: %v %v %v %v", *a1.Grade.Score, *a2.Grade.Score, *a3.Grade.Score, *a4.Grade.Score)
	}

	t.Run("get returns each stored attempt", func(t *testing.T) {
		for _, want := range []attemptResult{a1, a2, a3, a4} {
			r := e.req(t, "GET", "/attempts/"+want.Attempt.ID, e.human, nil, nil)
			if r.code != http.StatusOK {
				t.Fatalf("get = %d (%s)", r.code, r.body)
			}
			var got struct {
				ID          string          `json:"id"`
				ProblemID   string          `json:"problem_id"`
				Response    json.RawMessage `json:"response"`
				Score       *float64        `json:"score"`
				Rating      *int            `json:"rating"`
				AutoCorrect *bool           `json:"auto_correct"`
			}
			if err := json.Unmarshal(r.body, &got); err != nil {
				t.Fatal(err)
			}
			w := want.Attempt
			if got.ID != w.ID || got.ProblemID != w.ProblemID || string(got.Response) != string(w.Response) ||
				(got.Score == nil) != (w.Score == nil) || (got.Rating == nil) != (w.Rating == nil) ||
				(got.AutoCorrect == nil) != (w.AutoCorrect == nil) {
				t.Errorf("get %s = %s, want %+v", w.ID, r.body, w)
			}
		}
		unknown := domain.NewIDGenerator(domain.SystemClock{}).New().String()
		for name, path := range map[string]string{"unknown": unknown, "malformed": "not-an-id"} {
			if r := e.req(t, "GET", "/attempts/"+path, e.human, nil, nil); r.code != http.StatusNotFound {
				t.Errorf("%s = %d", name, r.code)
			}
		}
		if r := e.req(t, "GET", "/attempts/"+a1.Attempt.ID, e.other, nil, nil); r.code != http.StatusNotFound {
			t.Errorf("other user = %d", r.code)
		}
	})

	t.Run("list returns all, oldest first", func(t *testing.T) {
		ids, next := e.listAttempts(t, e.human, nil, http.StatusOK)
		if !slices.Equal(ids, all) || next != nil {
			t.Errorf("ids = %v next = %v, want %v", ids, next, all)
		}
		if other, _ := e.listAttempts(t, e.other, nil, http.StatusOK); len(other) != 0 {
			t.Errorf("other user sees %v", other)
		}
	})

	t.Run("filter by problem", func(t *testing.T) {
		ids, _ := e.listAttempts(t, e.human, url.Values{"problem": {mcq.ID.String()}}, http.StatusOK)
		if !slices.Equal(ids, []string{a1.Attempt.ID, a4.Attempt.ID}) {
			t.Errorf("ids = %v", ids)
		}
		e.listAttempts(t, e.human, url.Values{"problem": {"nope"}}, http.StatusUnprocessableEntity)
	})

	t.Run("filter by tag", func(t *testing.T) {
		if r := e.req(t, "POST", "/tags", e.human, map[string]string{"slug": "memory", "name": "memory"}, nil); r.code != http.StatusCreated {
			t.Fatalf("create tag = %d (%s)", r.code, r.body)
		}
		if r := e.req(t, "PUT", "/problems/"+cloze.ID.String()+"/tags", e.human, map[string]any{"tags": []string{"memory"}}, nil); r.code != http.StatusOK {
			t.Fatalf("tag problem = %d (%s)", r.code, r.body)
		}
		ids, _ := e.listAttempts(t, e.human, url.Values{"tag": {"memory"}}, http.StatusOK)
		if !slices.Equal(ids, []string{a2.Attempt.ID}) {
			t.Errorf("ids = %v", ids)
		}
		e.listAttempts(t, e.human, url.Values{"tag": {"unknown"}}, http.StatusUnprocessableEntity)
	})

	t.Run("filter by time", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		if ids, _ := e.listAttempts(t, e.human, url.Values{"since": {past}, "until": {future}}, http.StatusOK); len(ids) != 4 {
			t.Errorf("in window = %v", ids)
		}
		if ids, _ := e.listAttempts(t, e.human, url.Values{"since": {future}}, http.StatusOK); len(ids) != 0 {
			t.Errorf("since future = %v", ids)
		}
		if ids, _ := e.listAttempts(t, e.human, url.Values{"until": {past}}, http.StatusOK); len(ids) != 0 {
			t.Errorf("until past = %v", ids)
		}
		e.listAttempts(t, e.human, url.Values{"since": {"yesterday"}}, http.StatusUnprocessableEntity)
		e.listAttempts(t, e.human, url.Values{"since": {future}, "until": {past}}, http.StatusUnprocessableEntity)
	})

	t.Run("pagination", func(t *testing.T) {
		var got []string
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			q := url.Values{"limit": {"3"}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			ids, next := e.listAttempts(t, e.human, q, http.StatusOK)
			got = append(got, ids...)
			if next == nil {
				break
			}
			cursor = *next
		}
		if !slices.Equal(got, all) {
			t.Errorf("paged ids = %v, want %v", got, all)
		}
		e.listAttempts(t, e.human, url.Values{"cursor": {"!!"}}, http.StatusUnprocessableEntity)
	})

	t.Run("agents may read with attempts:read only", func(t *testing.T) {
		reader := e.idp.access(itAttemptReader, "author-sub")
		if ids, _ := e.listAttempts(t, reader, nil, http.StatusOK); len(ids) != 4 {
			t.Errorf("agent list = %v", ids)
		}
		if r := e.req(t, "GET", "/attempts/"+a1.Attempt.ID, reader, nil, nil); r.code != http.StatusOK {
			t.Errorf("agent get = %d", r.code)
		}
		// quizzotic-agent holds no attempts:read.
		e.listAttempts(t, e.agent, nil, http.StatusForbidden)
		if r := e.req(t, "GET", "/attempts/"+a1.Attempt.ID, e.agent, nil, nil); r.code != http.StatusForbidden {
			t.Errorf("agent without scope get = %d", r.code)
		}
		if r := e.answer(t, reader, attemptBody(mcq, map[string]any{"selected": []string{"c"}}, nil)); r.code != http.StatusForbidden {
			t.Errorf("reader answering = %d", r.code)
		}
	})
}
