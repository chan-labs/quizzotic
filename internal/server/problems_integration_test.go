// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

func init() {
	// POST /problems creates a row for the caller and takes no ID; nothing
	// stored for another user can be reached through it.
	ownerScope.Exempt("POST /problems",
		"creates a new row owned by the caller; takes no ID and reads no stored data")
	ownerScope.Resource(ownerscope.Resource{
		Name: "problems",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			prompt := "Owner-scope secret prompt " + domain.NewIDGenerator(domain.SystemClock{}).New().String()
			r := c.Do("POST", "/problems", openBody(prompt))
			if r.Code != http.StatusCreated {
				t.Fatalf("create problem = %d (%s)", r.Code, r.Body)
			}
			var v problems.View
			if err := json.Unmarshal(r.Body, &v); err != nil {
				t.Fatal(err)
			}
			return ownerscope.Created{
				Params: ownerscope.Params{"id": v.ID.String()},
				Header: http.Header{"If-Match": {r.Header.Get("ETag")}},
				// The ID is not a secret here: problem details echo the
				// requested path as `instance`.
				Secrets: []string{prompt, "secret reference answer"},
			}
		},
		Ops: []ownerscope.Op{
			{Pattern: "GET /problems/{id}"},
			{Pattern: "GET /problems/{id}/versions"},
			{Pattern: "PATCH /problems/{id}", Body: func(ownerscope.Created) any {
				return map[string]any{"change_kind": "minor", "prompt": "Hijacked"}
			}},
			{Pattern: "GET /problems", List: true},
			// The query text is A's own prompt; B must still get nothing back.
			{Pattern: "POST /problems/similar", List: true, Body: func(c ownerscope.Created) any {
				return map[string]any{"prompt": c.Secrets[0], "answers": []string{c.Secrets[1]}}
			}},
		},
		Intact: func(t testing.TB, c *ownerscope.Client, created ownerscope.Created) {
			r := c.Do("GET", "/problems/"+created.Params["id"]+"/versions", nil)
			var page struct {
				Items []struct {
					Prompt string `json:"prompt"`
				} `json:"items"`
			}
			if err := json.Unmarshal(r.Body, &page); err != nil || len(page.Items) != 1 || strings.Contains(page.Items[0].Prompt, "Hijacked") {
				t.Errorf("problem changed by another user: %d %s", r.Code, r.Body)
			}
		},
	})
}

func openBody(prompt string) map[string]any {
	return map[string]any{
		"type": "open", "prompt": prompt,
		"payload": map[string]any{"reference_answer": "secret reference answer"},
	}
}

// probEnv is an itEnv with one author who can act as a human or an agent.
type probEnv struct {
	*itEnv
	human, agent, other string // bearer tokens
}

func newProbEnv(t *testing.T) *probEnv {
	t.Helper()
	e := newITEnv(t)
	return &probEnv{
		itEnv: e,
		human: e.idp.access("quizzotic-cli", "author-sub", with("name", "Author"), with("email", "author@example.com")),
		// An agent acts as the human named in sub.
		agent: e.idp.access("quizzotic-agent", "author-sub"),
		other: e.idp.access("quizzotic-cli", "other-sub", with("name", "Other"), with("email", "other@example.com")),
	}
}

func (e *probEnv) req(t *testing.T, method, path, tok string, body any, hdr map[string]string) reply {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.srv.URL+"/api/v1"+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, b}
}

func (e *probEnv) create(t *testing.T, tok string, body map[string]any) problems.View {
	t.Helper()
	r := e.req(t, "POST", "/problems", tok, body, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", r.code, r.body)
	}
	return decodeReply[problems.View](t, r)
}

func decodeReply[T any](t *testing.T, r reply) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return v
}

type problemPage struct {
	Items      []problems.View `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

func (e *probEnv) list(t *testing.T, tok string, q url.Values) problemPage {
	t.Helper()
	r := e.req(t, "GET", "/problems?"+q.Encode(), tok, nil, nil)
	if r.code != 200 {
		t.Fatalf("list %v = %d (%s)", q, r.code, r.body)
	}
	return decodeReply[problemPage](t, r)
}

func prompts(items []problems.View) []string {
	out := make([]string, 0, len(items))
	for _, v := range items {
		out = append(out, v.Prompt)
	}
	return out
}

func mcqBody(prompt string, shuffle bool) map[string]any {
	return map[string]any{
		"type": "mcq", "prompt": prompt, "explanation": "Because of the explanation.",
		"payload": map[string]any{
			"choices": []map[string]string{
				{"id": "a", "text": "Alpha"}, {"id": "b", "text": "Bravo"}, {"id": "c", "text": "Charlie"},
				{"id": "d", "text": "Delta"}, {"id": "e", "text": "Echo"},
			},
			"correct": []string{"c"}, "shuffle": shuffle,
		},
	}
}

func clozeBody(prompt string) map[string]any {
	return map[string]any{
		"type": "cloze", "prompt": prompt,
		"payload": map[string]any{"blanks": []map[string]any{
			{"id": "w", "accepted": []string{"hippocampus"}, "max_edits": 1},
		}},
	}
}

func TestCreateProblem(t *testing.T) {
	e := newProbEnv(t)

	t.Run("defaults to draft and records the actor", func(t *testing.T) {
		r := e.req(t, "POST", "/problems", e.agent, mcqBody("Which letter is third?", false), nil)
		if r.code != http.StatusCreated {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		v := decodeReply[problems.View](t, r)
		uid, _ := e.userFor(t, "author-sub")
		if v.Status != problems.StatusDraft || v.Version != 1 || v.Type != "mcq" ||
			v.CreatedBy.Kind != domain.ActorAgent || v.CreatedBy.UserID != uid || v.CreatedBy.ClientID != "quizzotic-agent" {
			t.Errorf("view = %+v", v)
		}
		if got := r.header.Get("Location"); got != "/api/v1/problems/"+v.ID.String() {
			t.Errorf("Location = %q", got)
		}
		if r.header.Get("ETag") == "" || r.header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %v", r.header)
		}
		if g := e.req(t, "GET", "/problems/"+v.ID.String(), e.human, nil, nil); g.header.Get("ETag") != r.header.Get("ETag") {
			t.Errorf("GET ETag %q != create ETag %q", g.header.Get("ETag"), r.header.Get("ETag"))
		}
	})

	t.Run("live", func(t *testing.T) {
		body := openBody("Live one?")
		body["status"] = "live"
		v := e.create(t, e.human, body)
		if v.Status != problems.StatusLive || v.CreatedBy.Kind != domain.ActorHuman {
			t.Errorf("view = %+v", v)
		}
	})

	t.Run("validation failures list every field", func(t *testing.T) {
		body := map[string]any{
			"type": "mcq", "prompt": "  ", "status": "archived",
			"payload": map[string]any{"choices": []map[string]string{{"id": "a", "text": "Only"}}, "correct": []string{"zz"}},
		}
		p := e.req(t, "POST", "/problems", e.human, body, nil).wantProblem(t, 422, "validation")
		var fields []string
		for _, fe := range p.Errors {
			fields = append(fields, fe.Field)
		}
		for _, want := range []string{"status", "prompt", "payload.choices", "payload.correct[0]"} {
			if !slices.Contains(fields, want) {
				t.Errorf("errors %v lack %q", fields, want)
			}
		}
		if n := len(e.list(t, e.human, nil).Items); n != 2 {
			t.Errorf("invalid create must store nothing; list has %d", n)
		}
	})

	t.Run("cloze markers must match blanks", func(t *testing.T) {
		body := clozeBody("The {{nope}} stores memories.")
		e.req(t, "POST", "/problems", e.human, body, nil).wantProblem(t, 422, "validation")
	})

	t.Run("unknown type", func(t *testing.T) {
		e.req(t, "POST", "/problems", e.human, map[string]any{"type": "essay", "prompt": "x", "payload": map[string]any{}}, nil).
			wantProblem(t, 422, "validation")
	})

	t.Run("bad bodies", func(t *testing.T) {
		e.req(t, "POST", "/problems", e.human, "{not json", nil).wantProblem(t, 400, "bad-request")
		e.req(t, "POST", "/problems", e.human, `{"type":"open","prompt":"x","payload":{"reference_answer":"y"},"bogus":1}`, nil).
			wantProblem(t, 400, "bad-request")
	})

	t.Run("write scope required", func(t *testing.T) {
		ro := e.idp.access("quizzotic-agent", "author-sub", with("scope", "problems:read"))
		e.req(t, "POST", "/problems", ro, openBody("nope"), nil).wantProblem(t, 403, "forbidden")
	})
}

func TestGetProblemViews(t *testing.T) {
	e := newProbEnv(t)
	mcq := e.create(t, e.human, mcqBody("Pick the third letter.", false))
	shuf := e.create(t, e.human, mcqBody("Pick the third letter, shuffled.", true))
	cloze := e.create(t, e.human, clozeBody("The {{w}} stores memories."))
	open := e.create(t, e.human, map[string]any{
		"type": "open", "prompt": "Explain FSRS.", "explanation": "Because.",
		"payload": map[string]any{"reference_answer": "secret reference answer", "rubric": []string{"secret rubric point"}},
	})

	type promptView struct {
		ID      domain.ID       `json:"id"`
		Status  string          `json:"status"`
		Version int             `json:"version"`
		Type    string          `json:"type"`
		Prompt  string          `json:"prompt"`
		Payload json.RawMessage `json:"payload"`
	}
	prompt := func(id domain.ID) (promptView, []byte) {
		r := e.req(t, "GET", "/problems/"+id.String()+"?view=prompt", e.human, nil, nil)
		if r.code != 200 {
			t.Fatalf("prompt view = %d (%s)", r.code, r.body)
		}
		return decodeReply[promptView](t, r), r.body
	}

	t.Run("full view has the answers", func(t *testing.T) {
		for _, q := range []string{"", "?view=full"} {
			r := e.req(t, "GET", "/problems/"+open.ID.String()+q, e.human, nil, nil)
			if r.code != 200 {
				t.Fatalf("status = %d (%s)", r.code, r.body)
			}
			v := decodeReply[problems.View](t, r)
			if v.ID != open.ID || v.Explanation != "Because." || !strings.Contains(string(v.Payload), "secret reference answer") ||
				!strings.Contains(string(v.Payload), "secret rubric point") {
				t.Errorf("view%s = %s", q, r.body)
			}
		}
		v := decodeReply[problems.View](t, e.req(t, "GET", "/problems/"+mcq.ID.String(), e.human, nil, nil))
		if !strings.Contains(string(v.Payload), `"correct":["c"]`) {
			t.Errorf("mcq full view lacks correct: %s", v.Payload)
		}
	})

	t.Run("prompt view hides open answers", func(t *testing.T) {
		pv, raw := prompt(open.ID)
		if pv.Type != "open" || pv.Prompt != "Explain FSRS." || pv.Version != 1 || pv.Status != "draft" || string(pv.Payload) != "{}" {
			t.Errorf("view = %s", raw)
		}
		for _, leak := range []string{"secret reference answer", "secret rubric point", "Because.", "explanation", "reference_answer", "rubric"} {
			if bytes.Contains(raw, []byte(leak)) {
				t.Errorf("prompt view leaks %q: %s", leak, raw)
			}
		}
	})

	t.Run("prompt view hides cloze answers", func(t *testing.T) {
		pv, raw := prompt(cloze.ID)
		if string(pv.Payload) != `{"blanks":[{"id":"w"}]}` {
			t.Errorf("payload = %s", pv.Payload)
		}
		for _, leak := range []string{"hippocampus", "accepted", "max_edits", "case_sensitive"} {
			if bytes.Contains(raw, []byte(leak)) {
				t.Errorf("prompt view leaks %q: %s", leak, raw)
			}
		}
	})

	type mcqPrompt struct {
		Choices  []struct{ ID, Text string } `json:"choices"`
		Multiple bool                        `json:"multiple"`
		Shuffle  bool                        `json:"shuffle"`
	}
	order := func(id domain.ID) (string, []byte) {
		pv, raw := prompt(id)
		m := decodeReply[mcqPrompt](t, reply{body: pv.Payload})
		var ids []string
		for _, c := range m.Choices {
			ids = append(ids, c.ID)
		}
		return strings.Join(ids, ""), raw
	}

	t.Run("mcq without shuffle keeps order and hides correct", func(t *testing.T) {
		for range 10 {
			got, raw := order(mcq.ID)
			if got != "abcde" {
				t.Fatalf("order = %s, want abcde", got)
			}
			if bytes.Contains(raw, []byte("correct")) || bytes.Contains(raw, []byte("explanation")) {
				t.Fatalf("prompt view leaks: %s", raw)
			}
		}
	})

	t.Run("mcq with shuffle reorders but keeps the same choices", func(t *testing.T) {
		seen := map[string]bool{}
		for range 40 {
			got, raw := order(shuf.ID)
			ids := strings.Split(got, "")
			slices.Sort(ids)
			if strings.Join(ids, "") != "abcde" {
				t.Fatalf("choices = %s", got)
			}
			if bytes.Contains(raw, []byte("correct")) {
				t.Fatalf("prompt view leaks: %s", raw)
			}
			seen[got] = true
		}
		if len(seen) < 2 {
			t.Errorf("40 requests produced one order: %v", seen)
		}
		// The stored problem is untouched by shuffling.
		v := decodeReply[problems.View](t, e.req(t, "GET", "/problems/"+shuf.ID.String(), e.human, nil, nil))
		if !strings.Contains(string(v.Payload), `"id":"a","text":"Alpha"},{"id":"b"`) {
			t.Errorf("stored order changed: %s", v.Payload)
		}
	})

	t.Run("errors", func(t *testing.T) {
		e.req(t, "GET", "/problems/"+open.ID.String()+"?view=raw", e.human, nil, nil).wantProblem(t, 422, "validation")
		e.req(t, "GET", "/problems/"+domain.NewIDGenerator(domain.SystemClock{}).New().String(), e.human, nil, nil).
			wantProblem(t, 404, "not-found")
		e.req(t, "GET", "/problems/not-an-id", e.human, nil, nil).wantProblem(t, 404, "not-found")
		e.req(t, "GET", "/problems/"+open.ID.String(), e.other, nil, nil).wantProblem(t, 404, "not-found")
		e.req(t, "GET", "/problems/"+open.ID.String()+"?view=prompt", e.other, nil, nil).wantProblem(t, 404, "not-found")
	})
}

func TestListProblems(t *testing.T) {
	e := newProbEnv(t)
	live := func(b map[string]any) map[string]any { b["status"] = "live"; return b }

	byHuman := e.create(t, e.human, mcqBody("Human MCQ about Tokyo", false))
	liveHuman := e.create(t, e.human, live(openBody("Human open about 100% effort")))
	byAgent := e.create(t, e.agent, clozeBody("Agent cloze: the {{w}} stores memories."))
	liveAgent := e.create(t, e.agent, live(mcqBody("Agent MCQ about tokyo and 100_ things", true)))
	archived := e.create(t, e.agent, openBody("Agent open, to be archived"))
	deleted := e.create(t, e.human, openBody("Deleted open"))
	// Someone else's problem must never show up.
	e.create(t, e.other, openBody("Other users Tokyo problem"))

	ctx := context.Background()
	uid, _ := e.userFor(t, "author-sub")
	owner, _ := domain.ParseID(uid)
	if err := e.st.Problems().SetStatus(ctx, owner, archived.ID, problems.StatusArchived, archived.UpdatedAt.Add(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Problems().SoftDelete(ctx, owner, deleted.ID, deleted.UpdatedAt.Add(1)); err != nil {
		t.Fatal(err)
	}

	all := []problems.View{byHuman, liveHuman, byAgent, liveAgent, archived}
	ids := func(items []problems.View) []domain.ID {
		out := []domain.ID{}
		for _, v := range items {
			out = append(out, v.ID)
		}
		return out
	}
	want := func(vs ...problems.View) []domain.ID {
		out := []domain.ID{}
		for _, v := range vs {
			out = append(out, v.ID)
		}
		return out
	}

	cases := []struct {
		name string
		q    url.Values
		want []domain.ID
	}{
		{"no filter: oldest first, deleted and foreign hidden", url.Values{}, want(all...)},
		{"status draft", url.Values{"status": {"draft"}}, want(byHuman, byAgent)},
		{"status live", url.Values{"status": {"live"}}, want(liveHuman, liveAgent)},
		{"status archived", url.Values{"status": {"archived"}}, want(archived)},
		{"type mcq", url.Values{"type": {"mcq"}}, want(byHuman, liveAgent)},
		{"type cloze", url.Values{"type": {"cloze"}}, want(byAgent)},
		{"type open", url.Values{"type": {"open"}}, want(liveHuman, archived)},
		{"created_by_kind human", url.Values{"created_by_kind": {"human"}}, want(byHuman, liveHuman)},
		{"created_by_kind agent", url.Values{"created_by_kind": {"agent"}}, want(byAgent, liveAgent, archived)},
		{"created_by_kind system", url.Values{"created_by_kind": {"system"}}, want()},
		{"q is case-insensitive", url.Values{"q": {"TOKYO"}}, want(byHuman, liveAgent)},
		{"q is a substring", url.Values{"q": {"stores mem"}}, want(byAgent)},
		{"q percent is literal", url.Values{"q": {"100%"}}, want(liveHuman)},
		{"q underscore is literal", url.Values{"q": {"100_"}}, want(liveAgent)},
		{"q no match", url.Values{"q": {"zebra"}}, want()},
		{"filters combine with AND", url.Values{"type": {"mcq"}, "created_by_kind": {"agent"}, "status": {"live"}, "q": {"tokyo"}}, want(liveAgent)},
		{"combination with no match", url.Values{"type": {"open"}, "status": {"draft"}}, want()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page := e.list(t, e.human, c.q)
			if got := ids(page.Items); !slices.Equal(got, c.want) {
				t.Errorf("got %v\nwant %d items", prompts(page.Items), len(c.want))
			}
			if page.NextCursor != nil {
				t.Errorf("next_cursor = %q on a single page", *page.NextCursor)
			}
		})
	}

	t.Run("items carry the full view", func(t *testing.T) {
		page := e.list(t, e.human, url.Values{"type": {"open"}, "status": {"live"}})
		if len(page.Items) != 1 || !strings.Contains(string(page.Items[0].Payload), "reference_answer") {
			t.Fatalf("items = %+v", page.Items)
		}
	})

	t.Run("empty page is [] not null", func(t *testing.T) {
		r := e.req(t, "GET", "/problems?q=zebra", e.human, nil, nil)
		if !strings.Contains(string(r.body), `"items":[]`) || !strings.Contains(string(r.body), `"next_cursor":null`) {
			t.Errorf("body = %s", r.body)
		}
	})

	t.Run("tag filter", func(t *testing.T) {
		mkTag := func(tok, slug string) string {
			r := e.req(t, "POST", "/tags", tok, map[string]string{"slug": slug, "name": slug}, nil)
			if r.code != http.StatusCreated {
				t.Fatalf("create tag = %d (%s)", r.code, r.body)
			}
			return decodeReply[struct {
				ID string `json:"id"`
			}](t, r).ID
		}
		tagProblems := func(tok string, p problems.View, refs ...string) {
			r := e.req(t, "PUT", "/problems/"+p.ID.String()+"/tags", tok, map[string]any{"tags": refs}, nil)
			if r.code != http.StatusOK {
				t.Fatalf("tag problem = %d (%s)", r.code, r.body)
			}
		}
		bioID := mkTag(e.human, "biology")
		mkTag(e.human, "chemistry")
		tagProblems(e.human, byHuman, "biology")
		tagProblems(e.human, liveAgent, "biology", "chemistry")
		tagProblems(e.human, archived, "chemistry")
		// Another user's tag with the same slug, on their own problem.
		otherBio := mkTag(e.other, "biology")
		otherPage := e.list(t, e.other, nil)
		tagProblems(e.other, otherPage.Items[0], "biology")

		check := func(name, tag string, tok string, want ...problems.View) {
			t.Run(name, func(t *testing.T) {
				page := e.list(t, tok, url.Values{"tag": {tag}})
				if got := ids(page.Items); !slices.Equal(got, ids(want)) {
					t.Errorf("tag %q: got %v, want %v", tag, prompts(page.Items), prompts(want))
				}
			})
		}
		check("by slug", "biology", e.human, byHuman, liveAgent)
		check("by slug is case-insensitive", "BIOLOGY", e.human, byHuman, liveAgent)
		check("by ID", bioID, e.human, byHuman, liveAgent)
		check("other slug", "chemistry", e.human, liveAgent, archived)
		check("owner isolation: same slug, other user", "biology", e.other, otherPage.Items[0])

		t.Run("another user's tag ID is unknown", func(t *testing.T) {
			e.req(t, "GET", "/problems?tag="+otherBio, e.human, nil, nil).wantProblem(t, 422, "validation")
		})

		// Renaming leaves the old slug as an alias that still filters.
		g := e.req(t, "GET", "/tags/biology", e.human, nil, nil)
		r := e.req(t, "PATCH", "/tags/biology", e.human, map[string]string{"slug": "bio"}, map[string]string{"If-Match": g.header.Get("ETag")})
		if r.code != http.StatusOK {
			t.Fatalf("rename = %d (%s)", r.code, r.body)
		}
		check("by new slug", "bio", e.human, byHuman, liveAgent)
		check("by alias", "biology", e.human, byHuman, liveAgent)

		t.Run("combines with other filters and paging", func(t *testing.T) {
			page := e.list(t, e.human, url.Values{"tag": {"chemistry"}, "type": {"mcq"}})
			if !slices.Equal(ids(page.Items), ids([]problems.View{liveAgent})) {
				t.Errorf("got %v", prompts(page.Items))
			}
			p1 := e.list(t, e.human, url.Values{"tag": {"chemistry"}, "limit": {"1"}})
			if len(p1.Items) != 1 || p1.NextCursor == nil {
				t.Fatalf("page 1 = %v next %v", prompts(p1.Items), p1.NextCursor)
			}
			p2 := e.list(t, e.human, url.Values{"tag": {"chemistry"}, "limit": {"1"}, "cursor": {*p1.NextCursor}})
			if !slices.Equal(append(ids(p1.Items), ids(p2.Items)...), ids([]problems.View{liveAgent, archived})) || p2.NextCursor != nil {
				t.Errorf("page 2 = %v", prompts(p2.Items))
			}
		})

		t.Run("unknown tag is a validation error", func(t *testing.T) {
			p := e.req(t, "GET", "/problems?tag=nonexistent", e.human, nil, nil).wantProblem(t, 422, "validation")
			if len(p.Errors) != 1 || p.Errors[0].Field != "tag" {
				t.Errorf("errors = %+v", p.Errors)
			}
		})
	})

	t.Run("invalid filters are rejected together", func(t *testing.T) {
		p := e.req(t, "GET", "/problems?status=bogus&type=essay&created_by_kind=robot", e.human, nil, nil).wantProblem(t, 422, "validation")
		var fields []string
		for _, fe := range p.Errors {
			fields = append(fields, fe.Field)
		}
		slices.Sort(fields)
		if !slices.Equal(fields, []string{"created_by_kind", "status", "type"}) {
			t.Errorf("fields = %v", fields)
		}
	})

	t.Run("other user sees only their own", func(t *testing.T) {
		page := e.list(t, e.other, url.Values{"q": {"tokyo"}})
		if len(page.Items) != 1 || page.Items[0].Prompt != "Other users Tokyo problem" {
			t.Errorf("items = %v", prompts(page.Items))
		}
	})

	t.Run("pagination walks every page exactly once", func(t *testing.T) {
		var got []domain.ID
		cursor, pages := "", 0
		for {
			q := url.Values{"limit": {"2"}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			page := e.list(t, e.human, q)
			pages++
			if pages > 10 {
				t.Fatal("pagination does not terminate")
			}
			got = append(got, ids(page.Items)...)
			if page.NextCursor == nil {
				if len(page.Items) == 0 {
					t.Error("last page is empty")
				}
				break
			}
			if len(page.Items) != 2 {
				t.Errorf("non-final page has %d items", len(page.Items))
			}
			cursor = *page.NextCursor
		}
		if !slices.Equal(got, want(all...)) || pages != 3 {
			t.Errorf("pages = %d, ids = %v", pages, prompts(all))
		}
	})

	t.Run("exact fit has no next page", func(t *testing.T) {
		page := e.list(t, e.human, url.Values{"limit": {"5"}})
		if len(page.Items) != 5 || page.NextCursor != nil {
			t.Errorf("items = %d, next = %v", len(page.Items), page.NextCursor)
		}
	})

	t.Run("pagination keeps filters", func(t *testing.T) {
		q := url.Values{"created_by_kind": {"agent"}, "limit": {"2"}}
		p1 := e.list(t, e.human, q)
		if len(p1.Items) != 2 || p1.NextCursor == nil {
			t.Fatalf("page 1 = %v next %v", prompts(p1.Items), p1.NextCursor)
		}
		q.Set("cursor", *p1.NextCursor)
		p2 := e.list(t, e.human, q)
		if !slices.Equal(append(ids(p1.Items), ids(p2.Items)...), want(byAgent, liveAgent, archived)) || p2.NextCursor != nil {
			t.Errorf("page 2 = %v next %v", prompts(p2.Items), p2.NextCursor)
		}
	})

	t.Run("default limit applies", func(t *testing.T) {
		page := e.list(t, e.human, nil)
		if len(page.Items) != 5 {
			t.Errorf("items = %d", len(page.Items))
		}
	})

	t.Run("bad pagination parameters", func(t *testing.T) {
		for _, q := range []string{"limit=0", "limit=201", "limit=abc", "cursor=%25%25", "cursor=" + "bm90LWEtaWQ"} {
			p := e.req(t, "GET", "/problems?"+q, e.human, nil, nil).wantProblem(t, 422, "validation")
			if len(p.Errors) == 0 {
				t.Errorf("%s: no field errors", q)
			}
		}
		if e.req(t, "GET", "/problems?limit=200", e.human, nil, nil).code != 200 {
			t.Error("limit=200 must be accepted")
		}
	})

	t.Run("read scope required", func(t *testing.T) {
		none := e.idp.access("quizzotic-agent", "author-sub", with("scope", "attempts:write"))
		e.req(t, "GET", "/problems", none, nil, nil).wantProblem(t, 403, "forbidden")
	})
}

func TestCreateProblemIdempotencyReplay(t *testing.T) {
	e := newProbEnv(t)
	body := openBody("Created once, however often it is sent")
	key := map[string]string{"Idempotency-Key": "retry-key-1"}

	first := e.req(t, "POST", "/problems", e.agent, body, key)
	if first.code != http.StatusCreated {
		t.Fatalf("first = %d (%s)", first.code, first.body)
	}
	if first.header.Get("Idempotency-Replayed") != "" {
		t.Error("first response must not be marked replayed")
	}

	replay := e.req(t, "POST", "/problems", e.agent, body, key)
	if replay.code != http.StatusCreated || !bytes.Equal(replay.body, first.body) {
		t.Fatalf("replay = %d %s\nwant   = 201 %s", replay.code, replay.body, first.body)
	}
	if got := replay.header.Get("Idempotency-Replayed"); got != "true" {
		t.Errorf("Idempotency-Replayed = %q, want true", got)
	}
	for _, h := range []string{"Location", "ETag", "Content-Type"} {
		if replay.header.Get(h) != first.header.Get(h) || first.header.Get(h) == "" {
			t.Errorf("%s: replay %q, first %q", h, replay.header.Get(h), first.header.Get(h))
		}
	}

	// Exactly one row exists.
	uid, _ := e.userFor(t, "author-sub")
	owner, _ := domain.ParseID(uid)
	rows, err := e.st.Problems().List(context.Background(), owner, problems.ListFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, err %v; want exactly 1", len(rows), err)
	}
	if want := decodeReply[problems.View](t, first).ID; rows[0].ID != want {
		t.Errorf("stored id %s, response id %s", rows[0].ID, want)
	}

	t.Run("same key, different body is rejected and creates nothing", func(t *testing.T) {
		r := e.req(t, "POST", "/problems", e.agent, openBody("A different problem"), key)
		r.wantProblem(t, 422, "idempotency-key-reuse")
		if page := e.list(t, e.human, nil); len(page.Items) != 1 {
			t.Errorf("rows = %d", len(page.Items))
		}
	})

	t.Run("a new key creates a new row", func(t *testing.T) {
		r := e.req(t, "POST", "/problems", e.agent, body, map[string]string{"Idempotency-Key": "retry-key-2"})
		if r.code != http.StatusCreated || r.header.Get("Idempotency-Replayed") != "" {
			t.Fatalf("status = %d replayed = %q", r.code, r.header.Get("Idempotency-Replayed"))
		}
		if page := e.list(t, e.human, nil); len(page.Items) != 2 {
			t.Errorf("rows = %d, want 2", len(page.Items))
		}
	})

	t.Run("keys are per user", func(t *testing.T) {
		r := e.req(t, "POST", "/problems", e.other, body, key)
		if r.code != http.StatusCreated || r.header.Get("Idempotency-Replayed") != "" {
			t.Fatalf("other user with the same key = %d replayed %q", r.code, r.header.Get("Idempotency-Replayed"))
		}
		if id := decodeReply[problems.View](t, r).ID; id == decodeReply[problems.View](t, first).ID {
			t.Error("another user's request replayed the first user's response")
		}
	})

	t.Run("requests without a key are never replayed", func(t *testing.T) {
		a := e.req(t, "POST", "/problems", e.agent, body, nil)
		b := e.req(t, "POST", "/problems", e.agent, body, nil)
		if a.code != 201 || b.code != 201 || bytes.Equal(a.body, b.body) {
			t.Errorf("a = %d, b = %d", a.code, b.code)
		}
	})

	t.Run("a failed validation is stored too", func(t *testing.T) {
		bad := map[string]any{"type": "open", "prompt": "x", "payload": map[string]any{}}
		k := map[string]string{"Idempotency-Key": fmt.Sprint("bad-", 1)}
		first := e.req(t, "POST", "/problems", e.agent, bad, k)
		again := e.req(t, "POST", "/problems", e.agent, bad, k)
		if first.code != 422 || again.code != 422 || again.header.Get("Idempotency-Replayed") != "true" {
			t.Errorf("first %d, again %d replayed %q", first.code, again.code, again.header.Get("Idempotency-Replayed"))
		}
	})
}

func TestEditProblem(t *testing.T) {
	e := newProbEnv(t)
	v := e.create(t, e.human, openBody("Original prompt?"))
	id := v.ID.String()
	etag := e.req(t, "GET", "/problems/"+id, e.human, nil, nil).header.Get("ETag")

	t.Run("requires If-Match", func(t *testing.T) {
		e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"change_kind": "minor", "prompt": "x"}, nil).
			wantProblem(t, 428, "precondition-required")
	})
	t.Run("stale If-Match", func(t *testing.T) {
		e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"change_kind": "minor", "prompt": "x"},
			map[string]string{"If-Match": `"stale"`}).wantProblem(t, 412, "precondition-failed")
	})
	t.Run("change_kind is required and checked", func(t *testing.T) {
		h := map[string]string{"If-Match": etag}
		e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"prompt": "x"}, h).wantProblem(t, 422, "validation")
		e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"change_kind": "huge", "prompt": "x"}, h).wantProblem(t, 422, "validation")
	})
	t.Run("merged payload is validated", func(t *testing.T) {
		h := map[string]string{"If-Match": etag}
		e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"change_kind": "minor", "type": "cloze"}, h).
			wantProblem(t, 422, "validation")
		if got := e.req(t, "GET", "/problems/"+id, e.human, nil, nil); decodeReply[problems.View](t, got).Version != 1 {
			t.Error("invalid edit must not create a version")
		}
	})
	t.Run("minor edit makes version 2 and a fresh ETag", func(t *testing.T) {
		r := e.req(t, "PATCH", "/problems/"+id, e.agent, map[string]any{"change_kind": "minor", "prompt": "Fixed prompt?"},
			map[string]string{"If-Match": etag})
		if r.code != 200 {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		nv := decodeReply[problems.View](t, r)
		if nv.Version != 2 || nv.Prompt != "Fixed prompt?" || string(nv.Payload) != string(v.Payload) {
			t.Errorf("view = %+v", nv)
		}
		if r.header.Get("ETag") == "" || r.header.Get("ETag") == etag {
			t.Errorf("ETag %q must change from %q", r.header.Get("ETag"), etag)
		}
		// The old ETag is now stale.
		e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"change_kind": "minor", "prompt": "again"},
			map[string]string{"If-Match": etag}).wantProblem(t, 412, "precondition-failed")
	})
	t.Run("old versions stay readable", func(t *testing.T) {
		cur := e.req(t, "GET", "/problems/"+id, e.human, nil, nil).header.Get("ETag")
		e.req(t, "PATCH", "/problems/"+id, e.human, map[string]any{"change_kind": "major",
			"payload": map[string]any{"reference_answer": "new answer"}}, map[string]string{"If-Match": cur})
		r := e.req(t, "GET", "/problems/"+id+"/versions", e.human, nil, nil)
		if r.code != 200 {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		var page struct {
			Items []struct {
				Version    int    `json:"version"`
				Prompt     string `json:"prompt"`
				ChangeKind string `json:"change_kind"`
			} `json:"items"`
		}
		if err := json.Unmarshal(r.body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 3 {
			t.Fatalf("items = %+v", page.Items)
		}
		want := []struct{ prompt, kind string }{{"Original prompt?", "major"}, {"Fixed prompt?", "minor"}, {"Fixed prompt?", "major"}}
		for i, w := range want {
			if it := page.Items[i]; it.Version != i+1 || it.Prompt != w.prompt || it.ChangeKind != w.kind {
				t.Errorf("item %d = %+v, want %+v", i, it, w)
			}
		}
	})
	t.Run("another user's problem is 404", func(t *testing.T) {
		h := map[string]string{"If-Match": "*"}
		e.req(t, "PATCH", "/problems/"+id, e.other, map[string]any{"change_kind": "minor", "prompt": "x"}, h).wantProblem(t, 404, "not-found")
		e.req(t, "GET", "/problems/"+id+"/versions", e.other, nil, nil).wantProblem(t, 404, "not-found")
	})
}
