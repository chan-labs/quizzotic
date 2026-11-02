// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

// memRepo stores attempts in memory and joins "transactions" trivially.
type memRepo struct {
	rows map[domain.ID]Attempt
}

func (m *memRepo) Create(_ context.Context, owner domain.ID, a Attempt) error {
	if a.UserID != owner {
		return errors.New("owner mismatch")
	}
	m.rows[a.ID] = a
	return nil
}

func (m *memRepo) Get(_ context.Context, owner, id domain.ID) (Attempt, error) {
	a, ok := m.rows[id]
	if !ok || a.UserID != owner {
		return Attempt{}, ErrNotFound
	}
	return a, nil
}

func (m *memRepo) SetRating(_ context.Context, owner, id domain.ID, rating int, score *float64) error {
	a, ok := m.rows[id]
	if !ok || a.UserID != owner {
		return ErrNotFound
	}
	if a.Rating != nil {
		return ErrAlreadyRated
	}
	a.Rating = &rating
	if a.Score == nil {
		a.Score = score
	}
	m.rows[id] = a
	return nil
}

func (m *memRepo) ForCard(_ context.Context, owner, problem domain.ID, epoch int) ([]Attempt, error) {
	var out []Attempt
	for _, a := range m.rows {
		if a.UserID == owner && a.ProblemID == problem && a.CardEpoch == epoch {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *memRepo) List(_ context.Context, owner domain.ID, _ ListFilter) ([]Attempt, error) {
	var out []Attempt
	for _, a := range m.rows {
		if a.UserID == owner {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *memRepo) InTx(_ context.Context, fn func(Repo) error) error {
	snapshot := map[domain.ID]Attempt{}
	for k, v := range m.rows {
		snapshot[k] = v
	}
	if err := fn(m); err != nil {
		m.rows = snapshot
		return err
	}
	return nil
}

type fakeProblems struct {
	subj map[domain.ID]problems.AnswerSubject
}

func (f fakeProblems) ForAnswer(_ context.Context, _, id domain.ID) (problems.AnswerSubject, error) {
	s, ok := f.subj[id]
	if !ok {
		return problems.AnswerSubject{}, platform.NotFound("no such problem")
	}
	return s, nil
}

type recordingCards struct {
	calls  []int
	next   *time.Time
	failed error
}

func (r *recordingCards) Apply(_ context.Context, _ Attempt, rating int) (*time.Time, error) {
	r.calls = append(r.calls, rating)
	return r.next, r.failed
}

type env struct {
	repo  *memRepo
	cards *recordingCards
	svc   *Service
	h     *Handler
	user  domain.ID
	mcq   domain.ID
	cloze domain.ID
	open  domain.ID
}

var now = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := fakeClock{now}
	ids := domain.NewIDGenerator(clock)
	e := &env{repo: &memRepo{rows: map[domain.ID]Attempt{}}, cards: &recordingCards{}, user: ids.New(),
		mcq: ids.New(), cloze: ids.New(), open: ids.New()}
	src := fakeProblems{subj: map[domain.ID]problems.AnswerSubject{
		e.mcq: {ID: e.mcq, Status: problems.StatusLive, Version: 2, CardEpoch: 2, Type: problems.TypeMCQ, Explanation: "because",
			Payload: json.RawMessage(`{"choices":[{"id":"a","text":"A"},{"id":"b","text":"B"},{"id":"c","text":"C"}],"correct":["b"]}`)},
		e.cloze: {ID: e.cloze, Status: problems.StatusLive, Version: 1, CardEpoch: 1, Type: problems.TypeCloze,
			Payload: json.RawMessage(`{"blanks":[{"id":"x","accepted":["Paris"],"max_edits":1},{"id":"y","accepted":["France"]}]}`)},
		e.open: {ID: e.open, Status: problems.StatusLive, Version: 1, CardEpoch: 1, Type: problems.TypeOpen,
			Payload: json.RawMessage(`{"reference_answer":"ref"}`)},
	}}
	e.svc = NewService(e.repo, src, clock, ids, WithCardUpdater(e.cards))
	e.h = NewHandler(e.svc, nil)
	return e
}

func human(user domain.ID) domain.Actor {
	return domain.Actor{Kind: domain.ActorHuman, UserID: user, ClientID: "web-session"}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func ptr[T any](v T) *T { return &v }

func TestAnswerEveryType(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	t.Run("mcq right defaults to Good", func(t *testing.T) {
		res, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.mcq, ProblemVersion: 2,
			Response: raw(`{"selected":["b"]}`), LatencyMS: 800})
		if err != nil {
			t.Fatal(err)
		}
		g := res.Grade
		if g.Correct == nil || !*g.Correct || g.Score == nil || *g.Score != 1 || g.Rating == nil || *g.Rating != 3 || !g.Scheduled {
			t.Errorf("grade = %+v", g)
		}
		a := res.Attempt
		if a.CardEpoch != 2 || a.ProblemVersion != 2 || a.Rating != nil || a.LatencyMS != 800 || !a.AnsweredAt.Equal(now) ||
			a.Client.Kind != domain.ActorHuman || a.SessionID != nil {
			t.Errorf("attempt = %+v", a)
		}
		if res.Explanation != "because" || !bytes.Contains(res.Answer, []byte(`"correct":["b"]`)) || res.NextDue != nil {
			t.Errorf("result = %+v", res)
		}
		if stored, err := e.repo.Get(ctx, e.user, a.ID); err != nil || string(stored.Response) != `{"selected":["b"]}` {
			t.Errorf("stored = %+v %v", stored, err)
		}
	})

	t.Run("mcq wrong is Again whatever is sent", func(t *testing.T) {
		res, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.mcq, ProblemVersion: 2,
			Response: raw(`{"selected":["a"]}`), Rating: ptr(4)})
		if err != nil {
			t.Fatal(err)
		}
		if g := res.Grade; *g.Correct || *g.Score != 0 || *g.Rating != grading.RatingAgain {
			t.Errorf("grade = %+v", g)
		}
	})

	t.Run("cloze partial credit and per-blank result", func(t *testing.T) {
		res, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.cloze, ProblemVersion: 1,
			Response: raw(`{"answers":{"x":"paris","y":"Spain"}}`)})
		if err != nil {
			t.Fatal(err)
		}
		g := res.Grade
		if *g.Correct || *g.Score != 0.5 || *g.Rating != grading.RatingAgain || !g.Blanks["x"] || g.Blanks["y"] {
			t.Errorf("grade = %+v", g)
		}
	})

	t.Run("cloze all right with a chosen rating", func(t *testing.T) {
		e.cards.calls = nil
		res, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.cloze, ProblemVersion: 1,
			Response: raw(`{"answers":{"x":"Pariss","y":"france"}}`), Rating: ptr(4)})
		if err != nil {
			t.Fatal(err)
		}
		if !*res.Grade.Correct || *res.Grade.Rating != 4 || *res.Attempt.Rating != 4 || len(e.cards.calls) != 1 || e.cards.calls[0] != 4 {
			t.Errorf("result = %+v cards=%v", res, e.cards.calls)
		}
	})

	t.Run("open rated is scored from the rating", func(t *testing.T) {
		res, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.open, ProblemVersion: 1,
			Response: raw(`{"text":"my try"}`), Rating: ptr(2)})
		if err != nil {
			t.Fatal(err)
		}
		g := res.Grade
		if g.Correct != nil || res.Attempt.AutoCorrect != nil || *g.Score != 0.33 || *g.Rating != 2 || !g.Scheduled {
			t.Errorf("grade = %+v attempt = %+v", g, res.Attempt)
		}
		if !bytes.Contains(res.Answer, []byte("ref")) {
			t.Errorf("answer = %s", res.Answer)
		}
	})

	t.Run("open without a rating is stored unscheduled", func(t *testing.T) {
		e.cards.calls = nil
		res, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.open, ProblemVersion: 1,
			Response: raw(`{"text":"later"}`)})
		if err != nil {
			t.Fatal(err)
		}
		g := res.Grade
		if g.Scheduled || g.Score != nil || g.Rating != nil || res.Attempt.Score != nil || res.Attempt.Rating != nil || len(e.cards.calls) != 0 {
			t.Errorf("result = %+v cards=%v", res, e.cards.calls)
		}
		if _, err := e.repo.Get(ctx, e.user, res.Attempt.ID); err != nil {
			t.Errorf("attempt not stored: %v", err)
		}
	})
}

func TestAnswerCardHook(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	due := now.Add(24 * time.Hour)
	e.cards.next = &due
	in := AnswerInput{ProblemID: e.mcq, ProblemVersion: 2, Response: raw(`{"selected":["b"]}`)}

	res, err := e.svc.Answer(ctx, human(e.user), in)
	if err != nil || res.NextDue == nil || !res.NextDue.Equal(due) {
		t.Fatalf("res = %+v err = %v", res, err)
	}

	// A failing hook rolls the attempt back: grade, store and card are one unit.
	e.cards.failed = errors.New("card store down")
	before := len(e.repo.rows)
	if _, err := e.svc.Answer(ctx, human(e.user), in); err == nil {
		t.Fatal("expected the hook error")
	}
	if len(e.repo.rows) != before {
		t.Errorf("attempt stored although the card update failed")
	}
}

func TestAnswerRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ok := raw(`{"selected":["b"]}`)

	problemOf := func(t *testing.T, err error) *platform.Problem {
		t.Helper()
		var p *platform.Problem
		if !errors.As(err, &p) {
			t.Fatalf("err = %v, want a problem", err)
		}
		return p
	}

	t.Run("agent", func(t *testing.T) {
		agent := domain.Actor{Kind: domain.ActorAgent, UserID: e.user, ClientID: "a"}
		_, err := e.svc.Answer(ctx, agent, AnswerInput{ProblemID: e.mcq, ProblemVersion: 2, Response: ok})
		if p := problemOf(t, err); p.Status != http.StatusForbidden {
			t.Errorf("status = %d", p.Status)
		}
	})
	t.Run("stale version", func(t *testing.T) {
		_, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.mcq, ProblemVersion: 1, Response: ok})
		if p := problemOf(t, err); p.Status != http.StatusConflict || p.Type != platform.TypeStaleProblemVersion {
			t.Errorf("problem = %+v", p)
		}
	})
	t.Run("future version", func(t *testing.T) {
		_, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: e.mcq, ProblemVersion: 3, Response: ok})
		if p := problemOf(t, err); p.Status != http.StatusConflict {
			t.Errorf("problem = %+v", p)
		}
	})
	t.Run("not live", func(t *testing.T) {
		draft := domain.NewIDGenerator(fakeClock{now}).New()
		src := fakeProblems{subj: map[domain.ID]problems.AnswerSubject{
			draft: {ID: draft, Status: problems.StatusDraft, Version: 1, CardEpoch: 1, Type: problems.TypeOpen},
		}}
		svc := NewService(e.repo, src, fakeClock{now}, domain.NewIDGenerator(fakeClock{now}))
		_, err := svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: draft, ProblemVersion: 1, Response: raw(`{"text":"x"}`)})
		if p := problemOf(t, err); p.Status != http.StatusConflict {
			t.Errorf("problem = %+v", p)
		}
	})
	t.Run("unknown problem", func(t *testing.T) {
		_, err := e.svc.Answer(ctx, human(e.user), AnswerInput{ProblemID: domain.NewIDGenerator(fakeClock{now}).New(), ProblemVersion: 1, Response: ok})
		if p := problemOf(t, err); p.Status != http.StatusNotFound {
			t.Errorf("problem = %+v", p)
		}
	})

	invalid := map[string]AnswerInput{
		"missing response":      {ProblemID: e.mcq, ProblemVersion: 2},
		"zero version":          {ProblemID: e.mcq, Response: ok},
		"negative latency":      {ProblemID: e.mcq, ProblemVersion: 2, Response: ok, LatencyMS: -1},
		"bad session id":        {ProblemID: e.mcq, ProblemVersion: 2, Response: ok, SessionID: "nope"},
		"rating out of range":   {ProblemID: e.mcq, ProblemVersion: 2, Response: ok, Rating: ptr(5)},
		"rating zero":           {ProblemID: e.mcq, ProblemVersion: 2, Response: ok, Rating: ptr(0)},
		"wrong-rating on wrong": {ProblemID: e.mcq, ProblemVersion: 2, Response: raw(`{"selected":["a"]}`), Rating: ptr(9)},
		"unknown choice":        {ProblemID: e.mcq, ProblemVersion: 2, Response: raw(`{"selected":["zzz"]}`)},
		"two on single":         {ProblemID: e.mcq, ProblemVersion: 2, Response: raw(`{"selected":["a","b"]}`)},
		"none selected":         {ProblemID: e.mcq, ProblemVersion: 2, Response: raw(`{"selected":[]}`)},
		"wrong response shape":  {ProblemID: e.mcq, ProblemVersion: 2, Response: raw(`{"text":"b"}`)},
		"unknown blank":         {ProblemID: e.cloze, ProblemVersion: 1, Response: raw(`{"answers":{"nope":"x"}}`)},
		"open as mcq shape":     {ProblemID: e.open, ProblemVersion: 1, Response: raw(`{"selected":["a"]}`)},
	}
	for name, in := range invalid {
		t.Run(name, func(t *testing.T) {
			before := len(e.repo.rows)
			_, err := e.svc.Answer(ctx, human(e.user), in)
			if p := problemOf(t, err); p.Status != http.StatusUnprocessableEntity || len(p.Errors) == 0 {
				t.Errorf("problem = %+v", p)
			}
			if len(e.repo.rows) != before {
				t.Error("an invalid attempt was stored")
			}
		})
	}
}

// post sends body to the handler as principal p (nil: no principal).
func post(e *env, p *auth.Principal, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/attempts", bytes.NewBufferString(body))
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	e.h.Create(rec, req)
	return rec
}

func humanPrincipal(e *env) *auth.Principal {
	return &auth.Principal{UserID: e.user, Kind: auth.KindHuman, ClientID: "quizzotic-cli"}
}

func TestHandlerCreate(t *testing.T) {
	e := newEnv(t)
	body := func(problem domain.ID, version int, response, extra string) string {
		return `{"problem_id":"` + problem.String() + `","problem_version":` + itoa(version) +
			`,"response":` + response + `,"latency_ms":500` + extra + `}`
	}

	t.Run("created", func(t *testing.T) {
		rec := post(e, humanPrincipal(e), body(e.mcq, 2, `{"selected":["b"]}`, `,"rating":2`))
		if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
		var out struct {
			Attempt     map[string]any `json:"attempt"`
			Grade       map[string]any `json:"grade"`
			Answer      map[string]any `json:"answer"`
			Explanation string         `json:"explanation"`
			NextDue     *string        `json:"next_due"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Grade["rating"] != float64(2) || out.Attempt["rating"] != float64(2) || out.Explanation != "because" ||
			out.Answer["correct"] == nil || out.NextDue != nil || out.Attempt["id"] == "" {
			t.Errorf("body = %s", rec.Body)
		}
	})

	t.Run("rating Again on a right answer is 422 on the rating field", func(t *testing.T) {
		before := len(e.repo.rows)
		rec := post(e, humanPrincipal(e), body(e.mcq, 2, `{"selected":["b"]}`, `,"rating":1`))
		assertRatingProblem(t, rec)
		if len(e.repo.rows) != before {
			t.Error("attempt stored for an invalid rating")
		}
	})

	t.Run("rating Again on a right cloze answer is 422", func(t *testing.T) {
		assertRatingProblem(t, post(e, humanPrincipal(e),
			body(e.cloze, 1, `{"answers":{"x":"Paris","y":"France"}}`, `,"rating":1`)))
	})

	t.Run("rating Again on a wrong answer is fine", func(t *testing.T) {
		rec := post(e, humanPrincipal(e), body(e.mcq, 2, `{"selected":["a"]}`, `,"rating":1`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
	})

	t.Run("stale version is 409", func(t *testing.T) {
		rec := post(e, humanPrincipal(e), body(e.mcq, 1, `{"selected":["b"]}`, ""))
		if rec.Code != http.StatusConflict || rec.Header().Get("Content-Type") != platform.ProblemContentType {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
		var p platform.Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.Type != platform.TypeStaleProblemVersion {
			t.Errorf("type = %q", p.Type)
		}
	})

	t.Run("agent principal is 403", func(t *testing.T) {
		agent := &auth.Principal{UserID: e.user, Kind: auth.KindAgent, ClientID: "quizzotic-agent"}
		if rec := post(e, agent, body(e.mcq, 2, `{"selected":["b"]}`, "")); rec.Code != http.StatusForbidden {
			t.Errorf("status = %d (%s)", rec.Code, rec.Body)
		}
	})

	t.Run("no principal is 401", func(t *testing.T) {
		if rec := post(e, nil, body(e.mcq, 2, `{"selected":["b"]}`, "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("malformed input", func(t *testing.T) {
		cases := map[string]struct {
			body string
			want int
		}{
			"not json":           {`{`, http.StatusBadRequest},
			"unknown field":      {`{"nope":1}`, http.StatusBadRequest},
			"bad problem id":     {`{"problem_id":"x","problem_version":1,"response":{},"latency_ms":1}`, http.StatusUnprocessableEntity},
			"missing problem id": {`{"problem_version":1,"response":{},"latency_ms":1}`, http.StatusUnprocessableEntity},
			"unknown problem":    {body(domain.NewIDGenerator(fakeClock{now}).New(), 1, `{"text":"x"}`, ""), http.StatusNotFound},
		}
		for name, c := range cases {
			if rec := post(e, humanPrincipal(e), c.body); rec.Code != c.want {
				t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, c.want, rec.Body)
			}
		}
	})

	t.Run("nil service is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		NewHandler(nil, nil).Create(rec, httptest.NewRequest("POST", "/api/v1/attempts", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func assertRatingProblem(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("Content-Type") != platform.ProblemContentType {
		t.Fatalf("status = %d, content-type = %q (%s)", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	var p platform.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != platform.TypeValidation || len(p.Errors) != 1 || p.Errors[0].Field != "rating" {
		t.Errorf("problem = %+v", p)
	}
}

// TestMapGradingError covers the handler's mapping of grading sentinels alone.
func TestMapGradingError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		field  string
	}{
		{"invalid rating", grading.ErrInvalidRating, http.StatusUnprocessableEntity, "rating"},
		{"wrapped invalid rating", errors.Join(errors.New("ctx"), grading.ErrInvalidRating), http.StatusUnprocessableEntity, "rating"},
		{"rating required", grading.ErrRatingRequired, http.StatusUnprocessableEntity, "rating"},
		{"unknown type is not a client error", grading.ErrUnknownProblemType, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapGradingError(c.err)
			var p *platform.Problem
			if c.status == 0 {
				if errors.As(got, &p) {
					t.Fatalf("mapped to a client problem: %+v", p)
				}
				return
			}
			if !errors.As(got, &p) || p.Status != c.status || len(p.Errors) != 1 || p.Errors[0].Field != c.field {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestUnknownProblemTypeIsOpaque500(t *testing.T) {
	e := newEnv(t)
	bad := domain.NewIDGenerator(fakeClock{now}).New()
	src := fakeProblems{subj: map[domain.ID]problems.AnswerSubject{
		bad: {ID: bad, Status: problems.StatusLive, Version: 1, CardEpoch: 1, Type: "essay"},
	}}
	e.h = NewHandler(NewService(e.repo, src, fakeClock{now}, domain.NewIDGenerator(fakeClock{now})), nil)
	rec := post(e, humanPrincipal(e), `{"problem_id":"`+bad.String()+`","problem_version":1,"response":{"text":"x"},"latency_ms":1}`)
	if rec.Code != http.StatusInternalServerError || bytes.Contains(rec.Body.Bytes(), []byte("essay")) {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type fakeSessions struct {
	owned map[domain.ID]domain.ID // session -> owner
	err   error
}

func (f fakeSessions) Exists(_ context.Context, owner, id domain.ID) (bool, error) {
	return f.err == nil && f.owned[id] == owner, f.err
}

func TestAnswerValidatesSession(t *testing.T) {
	e := newEnv(t)
	ids := domain.NewIDGenerator(fakeClock{now})
	own, foreign, unknown := ids.New(), ids.New(), ids.New()
	other := ids.New()
	e.svc.sessions = fakeSessions{owned: map[domain.ID]domain.ID{own: e.user, foreign: other}}
	in := func(sid string) AnswerInput {
		return AnswerInput{ProblemID: e.mcq, ProblemVersion: 2, Response: raw(`{"selected":["b"]}`), LatencyMS: 1, SessionID: sid}
	}
	ctx := context.Background()

	for name, sid := range map[string]domain.ID{"unknown": unknown, "foreign": foreign} {
		_, err := e.svc.Answer(ctx, human(e.user), in(sid.String()))
		var p *platform.Problem
		if !errors.As(err, &p) || p.Status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Field != "session_id" {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if len(e.repo.rows) != 0 {
		t.Fatalf("rejected attempts were stored: %d", len(e.repo.rows))
	}
	if res, err := e.svc.Answer(ctx, human(e.user), in(own.String())); err != nil || res.Attempt.SessionID == nil || *res.Attempt.SessionID != own.String() {
		t.Errorf("own session: %+v, %v", res, err)
	}
	// No session_id: no lookup.
	if _, err := e.svc.Answer(ctx, human(e.user), in("")); err != nil {
		t.Errorf("no session: %v", err)
	}
	// A lookup failure is not mistaken for "no such session".
	e.svc.sessions = fakeSessions{err: errors.New("db down")}
	if _, err := e.svc.Answer(ctx, human(e.user), in(own.String())); err == nil || errors.As(err, new(*platform.Problem)) {
		t.Errorf("lookup failure = %v", err)
	}
}

func TestHandlerCreateUnknownSession(t *testing.T) {
	e := newEnv(t)
	e.svc.sessions = fakeSessions{}
	sid := domain.NewIDGenerator(fakeClock{now}).New().String()
	rec := post(e, humanPrincipal(e), `{"problem_id":"`+e.mcq.String()+`","problem_version":2,"response":{"selected":["b"]},"latency_ms":1,"session_id":"`+sid+`"}`)
	if rec.Code != http.StatusUnprocessableEntity || !bytes.Contains(rec.Body.Bytes(), []byte("session_id")) {
		t.Errorf("status = %d, body = %s", rec.Code, rec.Body)
	}
}
