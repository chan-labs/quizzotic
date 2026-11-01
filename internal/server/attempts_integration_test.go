// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

func init() {
	// POST /attempts names a problem in its body. Another user's live problem
	// must answer 404 and leak nothing, exactly like a by-ID path.
	ownerScope.Resource(ownerscope.Resource{
		Name: "attempts",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			prompt := "Attempt owner-scope prompt " + domain.NewIDGenerator(domain.SystemClock{}).New().String()
			body := openBody(prompt)
			body["status"] = "live"
			r := c.Do("POST", "/problems", body)
			if r.Code != http.StatusCreated {
				t.Fatalf("create problem = %d (%s)", r.Code, r.Body)
			}
			var v problems.View
			if err := json.Unmarshal(r.Body, &v); err != nil {
				t.Fatal(err)
			}
			return ownerscope.Created{
				Params:  ownerscope.Params{"id": v.ID.String()},
				Secrets: []string{prompt, "secret reference answer"},
			}
		},
		Ops: []ownerscope.Op{{
			Pattern: "POST /attempts", WantOwner: http.StatusCreated,
			Body: func(c ownerscope.Created) any {
				return map[string]any{"problem_id": c.Params["id"], "problem_version": 1, "latency_ms": 10,
					"rating": 3, "response": map[string]any{"text": "mine"}}
			},
		}},
	})
}

// attemptResult mirrors the response of POST /attempts.
type attemptResult struct {
	Attempt struct {
		ID             string          `json:"id"`
		ProblemID      string          `json:"problem_id"`
		ProblemVersion int             `json:"problem_version"`
		CardEpoch      int             `json:"card_epoch"`
		SessionID      *string         `json:"session_id"`
		Response       json.RawMessage `json:"response"`
		AutoCorrect    *bool           `json:"auto_correct"`
		Score          *float64        `json:"score"`
		Rating         *int            `json:"rating"`
		LatencyMS      int             `json:"latency_ms"`
		Client         struct {
			Kind     string `json:"kind"`
			ClientID string `json:"client_id"`
		} `json:"client"`
	} `json:"attempt"`
	Grade struct {
		Correct   *bool           `json:"correct"`
		Score     *float64        `json:"score"`
		Rating    *int            `json:"rating"`
		Scheduled bool            `json:"scheduled"`
		Blanks    map[string]bool `json:"blanks"`
	} `json:"grade"`
	Answer      json.RawMessage `json:"answer"`
	Explanation string          `json:"explanation"`
	NextDue     *string         `json:"next_due"`
}

func (e *probEnv) answer(t *testing.T, tok string, body map[string]any) reply {
	t.Helper()
	return e.req(t, "POST", "/attempts", tok, body, nil)
}

func attemptBody(p problems.View, response any, extra map[string]any) map[string]any {
	b := map[string]any{"problem_id": p.ID.String(), "problem_version": p.Version, "response": response, "latency_ms": 1500}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestAttemptsAgentClientIsForbidden(t *testing.T) {
	e := newProbEnv(t)
	live := openBody("Agents may not answer?")
	live["status"] = "live"
	p := e.create(t, e.human, live)
	body := attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})

	// quizzotic-agent has no attempts:write in its allowed scopes, and a token
	// that claims it cannot add it: scopes only ever narrow.
	claimed := e.idp.access("quizzotic-agent", "author-sub", with("scope", "problems:read attempts:write"))
	for name, tok := range map[string]string{"agent": e.agent, "agent claiming the scope": claimed} {
		r := e.answer(t, tok, body)
		if r.code != http.StatusForbidden || r.header.Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: status = %d, content-type = %q (%s)", name, r.code, r.header.Get("Content-Type"), r.body)
		}
	}
	// Nothing was stored: the human sees no attempt, and can still answer.
	if r := e.answer(t, e.human, body); r.code != http.StatusCreated {
		t.Fatalf("human answer = %d (%s)", r.code, r.body)
	}
	if r := e.req(t, "POST", "/attempts", "", body, nil); r.code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", r.code)
	}
}

func TestAttemptsRoundTripEveryType(t *testing.T) {
	e := newProbEnv(t)

	mkLive := func(body map[string]any) problems.View {
		body["status"] = "live"
		return e.create(t, e.human, body)
	}
	decode := func(t *testing.T, r reply, want int) attemptResult {
		t.Helper()
		if r.code != want {
			t.Fatalf("status = %d, want %d (%s)", r.code, want, r.body)
		}
		return decodeReply[attemptResult](t, r)
	}

	t.Run("mcq", func(t *testing.T) {
		p := mkLive(mcqBody("Which letter is third?", true))
		res := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"selected": []string{"c"}}, map[string]any{"rating": 4, "session_id": domain.NewIDGenerator(domain.SystemClock{}).New().String()})), 201)
		if !*res.Grade.Correct || *res.Grade.Score != 1 || *res.Grade.Rating != 4 || !res.Grade.Scheduled {
			t.Errorf("grade = %+v", res.Grade)
		}
		a := res.Attempt
		if a.ProblemID != p.ID.String() || a.ProblemVersion != 1 || a.CardEpoch != 1 || a.LatencyMS != 1500 || a.SessionID == nil ||
			*a.Rating != 4 || *a.AutoCorrect != true || a.Client.Kind != "human" || a.Client.ClientID != "quizzotic-cli" {
			t.Errorf("attempt = %+v", a)
		}
		var ans problems.MCQPayload
		if err := json.Unmarshal(res.Answer, &ans); err != nil || len(ans.Correct) != 1 || ans.Correct[0] != "c" {
			t.Errorf("answer = %s (%v)", res.Answer, err)
		}
		if res.Explanation != "Because of the explanation." || res.NextDue == nil {
			t.Fatalf("explanation = %q next_due = %v", res.Explanation, res.NextDue)
		}
		if _, err := time.Parse(time.RFC3339, *res.NextDue); err != nil {
			t.Errorf("next_due %q: %v", *res.NextDue, err)
		}

		wrong := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"selected": []string{"a"}}, nil)), 201)
		if *wrong.Grade.Correct || *wrong.Grade.Score != 0 || *wrong.Grade.Rating != 1 || wrong.Attempt.Rating != nil {
			t.Errorf("wrong grade = %+v", wrong)
		}

	})

	t.Run("cloze", func(t *testing.T) {
		p := mkLive(clozeBody("The seat of memory is the {{w}}."))
		res := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"answers": map[string]string{"w": "Hippocampus"}}, nil)), 201)
		if !*res.Grade.Correct || *res.Grade.Score != 1 || *res.Grade.Rating != 3 || !res.Grade.Blanks["w"] {
			t.Errorf("grade = %+v", res.Grade)
		}
		typo := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"answers": map[string]string{"w": "hippocampu"}}, nil)), 201)
		if !*typo.Grade.Correct {
			t.Errorf("one edit within max_edits should be right: %+v", typo.Grade)
		}
		miss := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"answers": map[string]string{}}, nil)), 201)
		if *miss.Grade.Correct || *miss.Grade.Score != 0 || miss.Grade.Blanks["w"] {
			t.Errorf("empty answer grade = %+v", miss.Grade)
		}
	})

	t.Run("open", func(t *testing.T) {
		p := mkLive(openBody("Explain spacing."))
		res := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"text": "Reviews spread out."}, map[string]any{"rating": 3})), 201)
		if res.Grade.Correct != nil || res.Attempt.AutoCorrect != nil || *res.Grade.Score != 0.67 || *res.Grade.Rating != 3 || !res.Grade.Scheduled {
			t.Errorf("grade = %+v attempt = %+v", res.Grade, res.Attempt)
		}
		var ans problems.OpenPayload
		if err := json.Unmarshal(res.Answer, &ans); err != nil || ans.ReferenceAnswer != "secret reference answer" {
			t.Errorf("answer = %s (%v)", res.Answer, err)
		}

		unrated := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"text": "later"}, nil)), 201)
		if unrated.Grade.Scheduled || unrated.Grade.Score != nil || unrated.Grade.Rating != nil || unrated.Attempt.Rating != nil {
			t.Errorf("unrated open = %+v", unrated)
		}
	})

	t.Run("attempts are stored once and read back", func(t *testing.T) {
		p := mkLive(mcqBody("Stored?", false))
		res := decode(t, e.answer(t, e.human, attemptBody(p, map[string]any{"selected": []string{"c"}}, nil)), 201)
		id, err := domain.ParseID(res.Attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		uid, err := domain.ParseID(func() string { s, _ := e.userFor(t, "author-sub"); return s }())
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.st.Attempts().Get(t.Context(), uid, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.ProblemID != p.ID || string(got.Response) != `{"selected":["c"]}` || got.Rating != nil || got.Client.Kind != domain.ActorHuman {
			t.Errorf("stored = %+v", got)
		}
	})
}

func TestAttemptsRatingRules(t *testing.T) {
	e := newProbEnv(t)
	live := mcqBody("Rating rules?", false)
	live["status"] = "live"
	p := e.create(t, e.human, live)
	right := map[string]any{"selected": []string{"c"}}

	t.Run("Again on a right answer is 422 on rating, and nothing is stored", func(t *testing.T) {
		r := e.answer(t, e.human, attemptBody(p, right, map[string]any{"rating": 1}))
		if r.code != http.StatusUnprocessableEntity || r.header.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		var pb struct {
			Type   string `json:"type"`
			Errors []struct {
				Field string `json:"field"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(r.body, &pb); err != nil || pb.Type != "urn:quizzotic:problem:validation" || len(pb.Errors) != 1 || pb.Errors[0].Field != "rating" {
			t.Errorf("problem = %s (%v)", r.body, err)
		}
	})
	t.Run("out of range", func(t *testing.T) {
		for _, n := range []int{0, 5, -1} {
			if r := e.answer(t, e.human, attemptBody(p, right, map[string]any{"rating": n})); r.code != http.StatusUnprocessableEntity {
				t.Errorf("rating %d = %d (%s)", n, r.code, r.body)
			}
		}
	})
	t.Run("a rating on a wrong answer is accepted", func(t *testing.T) {
		wrong := map[string]any{"selected": []string{"a"}}
		if r := e.answer(t, e.human, attemptBody(p, wrong, map[string]any{"rating": 1})); r.code != http.StatusCreated {
			t.Errorf("status = %d (%s)", r.code, r.body)
		}
	})
}

func TestAttemptsProblemVersioning(t *testing.T) {
	e := newProbEnv(t)
	body := mcqBody("Versioned?", false)
	body["status"] = "live"
	p := e.create(t, e.human, body)

	r := e.req(t, "GET", "/problems/"+p.ID.String(), e.human, nil, nil)
	etag := r.header.Get("ETag")
	patch := e.req(t, "PATCH", "/problems/"+p.ID.String(), e.human,
		map[string]any{"change_kind": "major", "prompt": "Versioned, differently?"}, map[string]string{"If-Match": etag})
	if patch.code != http.StatusOK {
		t.Fatalf("patch = %d (%s)", patch.code, patch.body)
	}

	stale := e.answer(t, e.human, attemptBody(p, map[string]any{"selected": []string{"c"}}, nil))
	if stale.code != http.StatusConflict {
		t.Fatalf("stale version = %d (%s)", stale.code, stale.body)
	}
	var pb struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(stale.body, &pb)
	if pb.Type != "urn:quizzotic:problem:stale-problem-version" {
		t.Errorf("type = %q", pb.Type)
	}

	p.Version = 2
	res := decodeReply[attemptResult](t, e.answer(t, e.human, attemptBody(p, map[string]any{"selected": []string{"c"}}, nil)))
	if res.Attempt.ProblemVersion != 2 || res.Attempt.CardEpoch != 2 {
		t.Errorf("attempt = %+v, want version 2 in card epoch 2", res.Attempt)
	}
}

func TestAttemptsNeedALiveProblem(t *testing.T) {
	e := newProbEnv(t)
	draft := e.create(t, e.human, openBody("Still a draft?"))
	if r := e.answer(t, e.human, attemptBody(draft, map[string]any{"text": "x"}, map[string]any{"rating": 3})); r.code != http.StatusConflict {
		t.Errorf("draft = %d (%s)", r.code, r.body)
	}
	if r := e.answer(t, e.human, map[string]any{"problem_id": domain.NewIDGenerator(domain.SystemClock{}).New().String(),
		"problem_version": 1, "response": map[string]any{"text": "x"}, "latency_ms": 1}); r.code != http.StatusNotFound {
		t.Errorf("unknown problem = %d (%s)", r.code, r.body)
	}
}

func TestAttemptsIdempotencyKey(t *testing.T) {
	e := newProbEnv(t)
	live := openBody("Retry me?")
	live["status"] = "live"
	p := e.create(t, e.human, live)
	body := attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})
	hdr := map[string]string{"Idempotency-Key": "attempt-key-1"}

	first := e.req(t, "POST", "/attempts", e.human, body, hdr)
	replay := e.req(t, "POST", "/attempts", e.human, body, hdr)
	if first.code != http.StatusCreated || replay.code != http.StatusCreated || string(first.body) != string(replay.body) ||
		replay.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("first = %d, replay = %d replayed=%q\n%s\n%s", first.code, replay.code, replay.header.Get("Idempotency-Replayed"), first.body, replay.body)
	}
}

func init() {
	// POST /attempts/{id}/rating names an attempt in its path. Another user's
	// attempt must answer 404 and leak nothing.
	ownerScope.Resource(ownerscope.Resource{
		Name: "attempt rating",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			prompt := "Rating owner-scope prompt " + domain.NewIDGenerator(domain.SystemClock{}).New().String()
			body := openBody(prompt)
			body["status"] = "live"
			r := c.Do("POST", "/problems", body)
			if r.Code != http.StatusCreated {
				t.Fatalf("create problem = %d (%s)", r.Code, r.Body)
			}
			var v problems.View
			if err := json.Unmarshal(r.Body, &v); err != nil {
				t.Fatal(err)
			}
			// An open attempt with no rating, so it can still be rated.
			r = c.Do("POST", "/attempts", map[string]any{"problem_id": v.ID.String(), "problem_version": 1,
				"latency_ms": 10, "response": map[string]any{"text": "unrated"}})
			if r.Code != http.StatusCreated {
				t.Fatalf("create attempt = %d (%s)", r.Code, r.Body)
			}
			var res attemptResult
			if err := json.Unmarshal(r.Body, &res); err != nil {
				t.Fatal(err)
			}
			return ownerscope.Created{
				Params:  ownerscope.Params{"id": res.Attempt.ID},
				Secrets: []string{prompt, "secret reference answer", "unrated"},
			}
		},
		Ops: []ownerscope.Op{{
			Pattern: "POST /attempts/{id}/rating", WantOwner: http.StatusOK,
			Body: func(ownerscope.Created) any { return map[string]any{"rating": 3} },
		}},
	})
}

func TestAttemptsLateRating(t *testing.T) {
	e := newProbEnv(t)
	live := openBody("Rate me later?")
	live["status"] = "live"
	p := e.create(t, e.human, live)
	rate := func(tok, id string, body any) reply {
		return e.req(t, "POST", "/attempts/"+id+"/rating", tok, body, nil)
	}

	unrated := decodeReply[attemptResult](t, e.answer(t, e.human, attemptBody(p, map[string]any{"text": "later"}, nil)))
	if unrated.Grade.Scheduled || unrated.Attempt.Rating != nil {
		t.Fatalf("unrated open = %+v", unrated)
	}
	id := unrated.Attempt.ID

	if r := rate(e.agent, id, map[string]any{"rating": 3}); r.code != http.StatusForbidden {
		t.Errorf("agent = %d (%s)", r.code, r.body)
	}
	if r := rate("", id, map[string]any{"rating": 3}); r.code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", r.code)
	}
	if r := rate(e.other, id, map[string]any{"rating": 3}); r.code != http.StatusNotFound {
		t.Errorf("other user = %d (%s)", r.code, r.body)
	}
	for _, body := range []any{map[string]any{}, map[string]any{"rating": 0}, map[string]any{"rating": 5}} {
		if r := rate(e.human, id, body); r.code != http.StatusUnprocessableEntity {
			t.Errorf("body %v = %d (%s)", body, r.code, r.body)
		}
	}
	if r := rate(e.human, domain.NewIDGenerator(domain.SystemClock{}).New().String(), map[string]any{"rating": 3}); r.code != http.StatusNotFound {
		t.Errorf("unknown attempt = %d", r.code)
	}

	r := rate(e.human, id, map[string]any{"rating": 2})
	if r.code != http.StatusOK {
		t.Fatalf("rate = %d (%s)", r.code, r.body)
	}
	res := decodeReply[attemptResult](t, r)
	if !res.Grade.Scheduled || *res.Grade.Rating != 2 || *res.Grade.Score != 0.33 || *res.Attempt.Rating != 2 || *res.Attempt.Score != 0.33 {
		t.Errorf("rated = %+v", res)
	}
	if r := rate(e.human, id, map[string]any{"rating": 4}); r.code != http.StatusConflict ||
		r.header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("second rating = %d (%s)", r.code, r.body)
	}
}

// Answering counts the stored attempt in the attempts metric; a refused answer
// counts nothing.
func TestAttemptsAreCountedInMetrics(t *testing.T) {
	e := newProbEnv(t)
	live := openBody("Is this counted?")
	live["status"] = "live"
	p := e.create(t, e.human, live)
	if !strings.Contains(e.metricsText(t), "quizzotic_attempts_recorded_total 0\n") {
		t.Fatalf("want 0 attempts at the start:\n%s", e.metricsText(t))
	}
	if r := e.answer(t, e.human, attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})); r.code != http.StatusCreated {
		t.Fatalf("answer = %d (%s)", r.code, r.body)
	}
	if r := e.answer(t, e.human, attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 9})); r.code == http.StatusCreated {
		t.Fatalf("an invalid rating was accepted")
	}
	if !strings.Contains(e.metricsText(t), "quizzotic_attempts_recorded_total 1\n") {
		t.Errorf("want 1 attempt:\n%s", e.metricsText(t))
	}
}
