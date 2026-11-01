// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

func init() {
	liveProblem := func(t testing.TB, c *ownerscope.Client, prompt string) string {
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
		return v.ID.String()
	}

	// A session lists the owner's problems; user B must never reach it, and
	// POST /sessions naming A's quiz must answer 404 like a by-ID path.
	ownerScope.Resource(ownerscope.Resource{
		Name: "sessions",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			prompt := "Session owner-scope prompt " + domain.NewIDGenerator(domain.SystemClock{}).New().String()
			pid := liveProblem(t, c, prompt)
			r := c.Do("POST", "/sessions", map[string]any{"kind": "review"})
			if r.Code != http.StatusCreated {
				t.Fatalf("create session = %d (%s)", r.Code, r.Body)
			}
			var s struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(r.Body, &s); err != nil {
				t.Fatal(err)
			}
			q := c.Do("POST", "/quizzes", map[string]any{"title": "Session owner-scope quiz", "status": "live",
				"problem_ids": []string{pid}})
			if q.Code != http.StatusCreated {
				t.Fatalf("create quiz = %d (%s)", q.Code, q.Body)
			}
			var quiz struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(q.Body, &quiz); err != nil {
				t.Fatal(err)
			}
			return ownerscope.Created{
				Params:  ownerscope.Params{"id": s.ID, "quiz": quiz.ID},
				Secrets: []string{prompt, "secret reference answer", "Session owner-scope quiz"},
			}
		},
		Ops: []ownerscope.Op{
			{Pattern: "GET /sessions/{id}"},
			{Pattern: "GET /sessions/{id}/next"},
			{Pattern: "POST /sessions/{id}/complete"},
			{Pattern: "POST /sessions", WantOwner: http.StatusCreated,
				Body: func(c ownerscope.Created) any {
					return map[string]any{"kind": "quiz", "quiz_id": c.Params["quiz"]}
				}},
		},
	})

	// A card exists once its problem has been answered.
	ownerScope.Resource(ownerscope.Resource{
		Name: "cards",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			prompt := "Card owner-scope prompt " + domain.NewIDGenerator(domain.SystemClock{}).New().String()
			pid := liveProblem(t, c, prompt)
			a := c.Do("POST", "/attempts", map[string]any{"problem_id": pid, "problem_version": 1,
				"latency_ms": 10, "rating": 3, "response": map[string]any{"text": "my private answer"}})
			if a.Code != http.StatusCreated {
				t.Fatalf("create attempt = %d (%s)", a.Code, a.Body)
			}
			return ownerscope.Created{
				Params:  ownerscope.Params{"problem_id": pid},
				Secrets: []string{prompt},
			}
		},
		Ops: []ownerscope.Op{
			{Pattern: "GET /cards/{problem_id}"},
			{Pattern: "GET /cards", List: true},
		},
	})
}

// sessionJSON mirrors Session.
type sessionJSON struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	QuizID *string `json:"quiz_id"`
	Items  []struct {
		Position  int    `json:"position"`
		ProblemID string `json:"problem_id"`
		CardEpoch int    `json:"card_epoch"`
		Kind      string `json:"kind"`
		Answered  bool   `json:"answered"`
	} `json:"items"`
	Answered    int     `json:"answered"`
	CreatedAt   string  `json:"created_at"`
	CompletedAt *string `json:"completed_at"`
}

func (s sessionJSON) kinds() []string {
	out := []string{}
	for _, it := range s.Items {
		out = append(out, it.Kind)
	}
	return out
}

// nextJSON mirrors SessionNext.
type nextJSON struct {
	SessionID string `json:"session_id"`
	Remaining int    `json:"remaining"`
	Item      *struct {
		Position int    `json:"position"`
		Kind     string `json:"kind"`
		Problem  struct {
			ID      string          `json:"id"`
			Version int             `json:"version"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"problem"`
	} `json:"item"`
}

// counter reads one unlabelled counter from /metrics.
func (e *itEnv) counter(t *testing.T, name string) int {
	t.Helper()
	for _, line := range strings.Split(e.metricsText(t), "\n") {
		if v, ok := strings.CutPrefix(line, name+" "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatalf("%s: %v", line, err)
			}
			return n
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

func TestSessionsEndToEnd(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()
	attemptsBefore := e.counter(t, "quizzotic_attempts_recorded_total")
	sessionsBefore := e.counter(t, "quizzotic_sessions_served_total")

	mk := func(prompt string, status string) problems.View {
		b := mcqBody(prompt, false)
		b["status"] = status
		return e.create(t, e.human, b)
	}
	due1, due2 := mk("Due one", "live"), mk("Due two", "live")
	new1, new2 := mk("New one", "live"), mk("New two", "live")
	draft := mk("A draft", "draft")

	// Answering schedules a card; the response carries its real next due date.
	answered := 0
	for _, p := range []problems.View{due1, due2} {
		r := e.answer(t, e.human, attemptBody(p, map[string]any{"selected": []string{"c"}}, nil))
		if r.code != http.StatusCreated {
			t.Fatalf("answer = %d (%s)", r.code, r.body)
		}
		answered++
		res := decodeReply[attemptResult](t, r)
		if res.NextDue == nil {
			t.Fatal("answer returned no next_due")
		}
		due, err := time.Parse(time.RFC3339, *res.NextDue)
		if err != nil || !due.After(time.Now()) {
			t.Fatalf("next_due = %q (%v), want a future time", *res.NextDue, err)
		}
	}
	// Make the two answered cards due now: the test cannot wait days.
	uid, _ := e.userFor(t, "author-sub")
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []problems.View{due1, due2} {
		c, err := e.st.Cards().Get(ctx, owner, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		c.State, c.Due, c.LastReview = fsrs.Review, time.Now().Add(-time.Hour), time.Now().Add(-72*time.Hour)
		if err := e.st.Cards().Put(ctx, owner, c); err != nil {
			t.Fatal(err)
		}
	}

	// POST /sessions: two due reviews first, then the two new problems; the
	// draft never enters review.
	r := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "review"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("POST /sessions = %d (%s)", r.code, r.body)
	}
	s := decodeReply[sessionJSON](t, r)
	if r.header.Get("Location") != "/api/v1/sessions/"+s.ID {
		t.Errorf("Location = %q", r.header.Get("Location"))
	}
	if got := strings.Join(s.kinds(), ","); got != "review,review,new,new" {
		t.Fatalf("session mix = %s, want 2 due reviews then 2 new", got)
	}
	dueSet := map[string]bool{due1.ID.String(): true, due2.ID.String(): true}
	newSet := map[string]bool{new1.ID.String(): true, new2.ID.String(): true}
	for i, it := range s.Items {
		wantSet := dueSet
		if i >= 2 {
			wantSet = newSet
		}
		if !wantSet[it.ProblemID] || it.ProblemID == draft.ID.String() || it.CardEpoch != 1 || it.Answered || it.Position != i {
			t.Errorf("item %d = %+v", i, it)
		}
	}
	if s.Kind != "review" || s.QuizID != nil || s.CompletedAt != nil || s.Answered != 0 {
		t.Errorf("session = %+v", s)
	}

	// GET returns the same frozen list.
	g := decodeReply[sessionJSON](t, e.req(t, "GET", "/sessions/"+s.ID, e.human, nil, nil))
	if strings.Join(g.kinds(), ",") != "review,review,new,new" || g.Items[0].ProblemID != s.Items[0].ProblemID {
		t.Errorf("GET /sessions/{id} = %+v", g)
	}

	// next serves a prompt: no answer, no explanation.
	nr := e.req(t, "GET", "/sessions/"+s.ID+"/next", e.human, nil, nil)
	if nr.code != http.StatusOK {
		t.Fatalf("next = %d (%s)", nr.code, nr.body)
	}
	if strings.Contains(string(nr.body), "Because of the explanation.") || strings.Contains(string(nr.body), `"correct"`) {
		t.Errorf("next leaks the answer: %s", nr.body)
	}
	n := decodeReply[nextJSON](t, nr)
	if n.Item == nil || n.Item.Position != 0 || n.Item.Problem.ID != s.Items[0].ProblemID || n.Remaining != 4 || n.SessionID != s.ID {
		t.Fatalf("next = %+v", n)
	}

	// Answer everything in the session, in order.
	for i := range s.Items {
		n := decodeReply[nextJSON](t, e.req(t, "GET", "/sessions/"+s.ID+"/next", e.human, nil, nil))
		if n.Item == nil || n.Item.Position != i || n.Remaining != 4-i {
			t.Fatalf("next #%d = %+v", i, n)
		}
		body := map[string]any{"problem_id": n.Item.Problem.ID, "problem_version": n.Item.Problem.Version, "latency_ms": 5,
			"session_id": s.ID, "response": map[string]any{"selected": []string{"c"}}}
		if r := e.answer(t, e.human, body); r.code != http.StatusCreated {
			t.Fatalf("answer in session = %d (%s)", r.code, r.body)
		}
		answered++
		if mid := decodeReply[sessionJSON](t, e.req(t, "GET", "/sessions/"+s.ID, e.human, nil, nil)); mid.Answered != i+1 || !mid.Items[i].Answered {
			t.Errorf("after answer %d: %+v", i, mid)
		}
	}
	end := decodeReply[nextJSON](t, e.req(t, "GET", "/sessions/"+s.ID+"/next", e.human, nil, nil))
	if end.Item != nil || end.Remaining != 0 {
		t.Errorf("next after the last answer = %+v", end)
	}

	// Complete is idempotent; a completed session serves nothing more.
	c1 := decodeReply[sessionJSON](t, e.req(t, "POST", "/sessions/"+s.ID+"/complete", e.human, nil, nil))
	c2 := decodeReply[sessionJSON](t, e.req(t, "POST", "/sessions/"+s.ID+"/complete", e.human, nil, nil))
	if c1.CompletedAt == nil || c2.CompletedAt == nil || *c1.CompletedAt != *c2.CompletedAt || c1.Answered != 4 {
		t.Errorf("complete = %+v then %+v", c1, c2)
	}
	if r := e.req(t, "GET", "/sessions/"+s.ID+"/next", e.human, nil, nil); r.code != http.StatusConflict ||
		r.header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("next after complete = %d (%s)", r.code, r.body)
	}

	// Both counters moved: every answer and the one served session.
	if got := e.counter(t, "quizzotic_attempts_recorded_total") - attemptsBefore; got != answered {
		t.Errorf("attempts counter rose by %d, want %d", got, answered)
	}
	if got := e.counter(t, "quizzotic_sessions_served_total") - sessionsBefore; got != 1 {
		t.Errorf("sessions counter rose by %d, want 1", got)
	}
	// Reading and completing a session does not count it as served again.
	e.req(t, "GET", "/sessions/"+s.ID, e.human, nil, nil)
	if got := e.counter(t, "quizzotic_sessions_served_total") - sessionsBefore; got != 1 {
		t.Errorf("sessions counter after reads = +%d, want +1", got)
	}

	// The answers created cards for the new problems and moved the due ones.
	cp := decodeReply[cardPage](t, e.req(t, "GET", "/cards", e.human, nil, nil))
	if len(cp.Items) != 4 || cp.NextCursor != nil {
		t.Fatalf("GET /cards = %+v", cp)
	}
	one := decodeReply[cardJSON](t, e.req(t, "GET", "/cards/"+new1.ID.String(), e.human, nil, nil))
	if one.ProblemID != new1.ID.String() || one.Reps != 1 || one.State == "" || one.Due == "" || one.LastReview == nil || one.CardEpoch != 1 {
		t.Errorf("GET /cards/{id} = %+v", one)
	}
	if r := e.req(t, "GET", "/cards/"+draft.ID.String(), e.human, nil, nil); r.code != http.StatusNotFound {
		t.Errorf("card of a never-answered problem = %d", r.code)
	}
	if r := e.req(t, "GET", "/cards/not-an-id", e.human, nil, nil); r.code != http.StatusNotFound {
		t.Errorf("card of a malformed ID = %d", r.code)
	}
}

func TestSessionsAreHumansOnly(t *testing.T) {
	e := newProbEnv(t)
	b := mcqBody("Humans only", false)
	b["status"] = "live"
	e.create(t, e.human, b)
	s := decodeReply[sessionJSON](t, e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "review"}, nil))

	// quizzotic-agent has no sessions:write, and a token that claims the scope
	// cannot add it; quizzotic-attempt-reader holds only attempts:read.
	claimed := e.idp.access("quizzotic-agent", "author-sub", with("scope", "problems:read sessions:write"))
	reader := e.idp.access(itAttemptReader, "author-sub")
	for name, tok := range map[string]string{"agent": e.agent, "agent claiming the scope": claimed, "attempts reader": reader} {
		for _, op := range []struct{ method, path string }{
			{"POST", "/sessions"}, {"GET", "/sessions/" + s.ID}, {"GET", "/sessions/" + s.ID + "/next"}, {"POST", "/sessions/" + s.ID + "/complete"},
		} {
			var body any
			if op.path == "/sessions" {
				body = map[string]any{"kind": "review"}
			}
			r := e.req(t, op.method, op.path, tok, body, nil)
			if r.code != http.StatusForbidden || r.header.Get("Content-Type") != "application/problem+json" {
				t.Errorf("%s: %s %s = %d (%s)", name, op.method, op.path, r.code, r.body)
			}
		}
	}
	if r := e.req(t, "POST", "/sessions", "", map[string]any{"kind": "review"}, nil); r.code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", r.code)
	}
	// None of that touched the session.
	if g := decodeReply[sessionJSON](t, e.req(t, "GET", "/sessions/"+s.ID, e.human, nil, nil)); g.CompletedAt != nil {
		t.Errorf("session completed by a refused request: %+v", g)
	}
	// An agent with attempts:read may read cards, since cards are not session state.
	if r := e.req(t, "GET", "/cards", reader, nil, nil); r.code != http.StatusOK {
		t.Errorf("attempt reader GET /cards = %d (%s)", r.code, r.body)
	}
	if r := e.req(t, "GET", "/cards", e.agent, nil, nil); r.code != http.StatusForbidden {
		t.Errorf("agent without attempts:read GET /cards = %d", r.code)
	}
}

func TestQuizSessionsOverHTTP(t *testing.T) {
	e := newProbEnv(t)
	var ids []string
	for _, p := range []string{"Quiz one", "Quiz two", "Quiz three"} {
		b := mcqBody(p, false)
		b["status"] = "live"
		ids = append(ids, e.create(t, e.human, b).ID.String())
	}
	mkQuiz := func(status string) string {
		r := e.req(t, "POST", "/quizzes", e.human, map[string]any{"title": "Q " + status, "status": status,
			"problem_ids": []string{ids[2], ids[0], ids[1]}}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create quiz = %d (%s)", r.code, r.body)
		}
		return decodeReply[quizJSON](t, r).ID
	}
	live := mkQuiz("live")

	before := e.counter(t, "quizzotic_sessions_served_total")
	s := decodeReply[sessionJSON](t, e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "quiz", "quiz_id": live}, nil))
	if s.Kind != "quiz" || s.QuizID == nil || *s.QuizID != live || strings.Join(s.kinds(), ",") != "quiz,quiz,quiz" {
		t.Fatalf("quiz session = %+v", s)
	}
	for i, want := range []string{ids[2], ids[0], ids[1]} {
		if s.Items[i].ProblemID != want {
			t.Errorf("item %d = %s, want authored order %s", i, s.Items[i].ProblemID, want)
		}
	}
	sh := decodeReply[sessionJSON](t, e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "quiz", "quiz_id": live, "shuffle": true}, nil))
	if len(sh.Items) != 3 {
		t.Errorf("shuffled quiz session = %+v", sh)
	}
	if got := e.counter(t, "quizzotic_sessions_served_total") - before; got != 2 {
		t.Errorf("sessions counter rose by %d, want 2", got)
	}

	// A quiz answer goes through the normal attempt path and updates the card.
	n := decodeReply[nextJSON](t, e.req(t, "GET", "/sessions/"+s.ID+"/next", e.human, nil, nil))
	if n.Item == nil || n.Item.Kind != "quiz" || n.Item.Problem.ID != ids[2] {
		t.Fatalf("next = %+v", n)
	}
	body := map[string]any{"problem_id": ids[2], "problem_version": 1, "latency_ms": 5, "session_id": s.ID,
		"response": map[string]any{"selected": []string{"c"}}}
	if r := e.answer(t, e.human, body); r.code != http.StatusCreated {
		t.Fatalf("answer = %d (%s)", r.code, r.body)
	}
	if r := e.req(t, "GET", "/cards/"+ids[2], e.human, nil, nil); r.code != http.StatusOK {
		t.Errorf("card after a quiz answer = %d", r.code)
	}

	// Validation: draft and archived quizzes, malformed requests.
	for name, body := range map[string]map[string]any{
		"draft quiz":       {"kind": "quiz", "quiz_id": mkQuiz("draft")},
		"archived quiz":    {"kind": "quiz", "quiz_id": mkQuiz("archived")},
		"quiz without id":  {"kind": "quiz"},
		"review with quiz": {"kind": "review", "quiz_id": live},
		"review shuffled":  {"kind": "review", "shuffle": true},
		"unknown kind":     {"kind": "exam"},
		"missing kind":     {},
	} {
		r := e.req(t, "POST", "/sessions", e.human, body, nil)
		if r.code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d (%s), want 422", name, r.code, r.body)
		}
	}
	if r := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "quiz", "quiz_id": "nope"}, nil); r.code != http.StatusUnprocessableEntity {
		t.Errorf("malformed quiz id = %d", r.code)
	}
	if r := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "quiz", "quiz_id": domain.NewIDGenerator(domain.SystemClock{}).New().String()}, nil); r.code != http.StatusNotFound {
		t.Errorf("unknown quiz = %d", r.code)
	}
	if r := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "review", "surprise": 1}, nil); r.code != http.StatusBadRequest {
		t.Errorf("unknown field = %d (%s)", r.code, r.body)
	}
	// Another user cannot serve this user's quiz.
	if r := e.req(t, "POST", "/sessions", e.other, map[string]any{"kind": "quiz", "quiz_id": live}, nil); r.code != http.StatusNotFound {
		t.Errorf("other user's quiz = %d", r.code)
	}
}

func TestEmptySessionAndSessionLookups(t *testing.T) {
	e := newProbEnv(t)
	r := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "review"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("empty review session = %d (%s)", r.code, r.body)
	}
	if strings.Contains(string(r.body), `"items":null`) {
		t.Errorf("items must be [], got %s", r.body)
	}
	s := decodeReply[sessionJSON](t, r)
	if len(s.Items) != 0 {
		t.Fatalf("items = %+v", s.Items)
	}
	n := decodeReply[nextJSON](t, e.req(t, "GET", "/sessions/"+s.ID+"/next", e.human, nil, nil))
	if n.Item != nil || n.Remaining != 0 {
		t.Errorf("next of an empty session = %+v", n)
	}
	for _, path := range []string{"/sessions/nope", "/sessions/nope/next", "/sessions/" + domain.NewIDGenerator(domain.SystemClock{}).New().String()} {
		if r := e.req(t, "GET", path, e.human, nil, nil); r.code != http.StatusNotFound {
			t.Errorf("GET %s = %d", path, r.code)
		}
	}
	if r := e.req(t, "POST", "/sessions/nope/complete", e.human, nil, nil); r.code != http.StatusNotFound {
		t.Errorf("complete unknown = %d", r.code)
	}

	// Idempotency-Key replays the original session instead of building another.
	hdr := map[string]string{"Idempotency-Key": "session-key-1"}
	a := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "review"}, hdr)
	b := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "review"}, hdr)
	if a.code != http.StatusCreated || b.code != http.StatusCreated || decodeReply[sessionJSON](t, a).ID != decodeReply[sessionJSON](t, b).ID ||
		b.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay: %d/%d replayed=%q", a.code, b.code, b.header.Get("Idempotency-Replayed"))
	}
}

// cardJSON mirrors Card.
type cardJSON struct {
	ProblemID  string  `json:"problem_id"`
	CardEpoch  int     `json:"card_epoch"`
	State      string  `json:"state"`
	Step       int     `json:"step"`
	Stability  float64 `json:"stability"`
	Difficulty float64 `json:"difficulty"`
	Due        string  `json:"due"`
	LastReview *string `json:"last_review"`
	Reps       int     `json:"reps"`
	Lapses     int     `json:"lapses"`
}

type cardPage struct {
	Items      []cardJSON `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

func TestCardsEndpoints(t *testing.T) {
	e := newProbEnv(t)
	var ps []problems.View
	for _, p := range []string{"Card one", "Card two", "Card three"} {
		b := mcqBody(p, false)
		b["status"] = "live"
		v := e.create(t, e.human, b)
		ps = append(ps, v)
		if r := e.answer(t, e.human, attemptBody(v, map[string]any{"selected": []string{"c"}}, nil)); r.code != http.StatusCreated {
			t.Fatalf("answer = %d (%s)", r.code, r.body)
		}
	}
	if r := e.req(t, "POST", "/tags", e.human, map[string]string{"slug": "memory", "name": "memory"}, nil); r.code != http.StatusCreated {
		t.Fatalf("create tag = %d (%s)", r.code, r.body)
	}
	if r := e.req(t, "PUT", "/problems/"+ps[0].ID.String()+"/tags", e.human, map[string]any{"tags": []string{"memory"}}, nil); r.code != http.StatusOK {
		t.Fatalf("tag = %d (%s)", r.code, r.body)
	}

	get := func(q string) (cardPage, reply) {
		r := e.req(t, "GET", "/cards"+q, e.human, nil, nil)
		if r.code != http.StatusOK {
			return cardPage{}, r
		}
		return decodeReply[cardPage](t, r), r
	}
	if all, _ := get(""); len(all.Items) != 3 {
		t.Fatalf("all = %+v", all)
	}
	// Pagination by problem ID.
	p1, _ := get("?limit=2")
	if len(p1.Items) != 2 || p1.NextCursor == nil {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2, _ := get("?limit=2&cursor=" + *p1.NextCursor)
	if len(p2.Items) != 1 || p2.NextCursor != nil || p2.Items[0].ProblemID == p1.Items[0].ProblemID {
		t.Fatalf("page 2 = %+v", p2)
	}
	// due_before: every card is due in the future, so none is due before now,
	// and all are due before next year.
	if early, _ := get("?due_before=" + time.Now().UTC().Format(time.RFC3339)); len(early.Items) != 0 {
		t.Errorf("due before now = %+v", early)
	}
	if later, _ := get("?due_before=" + time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)); len(later.Items) != 3 {
		t.Errorf("due within a year = %+v", later)
	}
	// tag filter by slug.
	if tagged, _ := get("?tag=memory"); len(tagged.Items) != 1 || tagged.Items[0].ProblemID != ps[0].ID.String() {
		t.Errorf("tag=memory = %+v", tagged)
	}
	for _, q := range []string{"?tag=no-such-tag", "?due_before=yesterday", "?cursor=!!"} {
		if _, r := get(q); r.code != http.StatusUnprocessableEntity {
			t.Errorf("GET /cards%s = %d (%s), want 422", q, r.code, r.body)
		}
	}
	if _, r := get("?limit=0"); r.code != http.StatusUnprocessableEntity {
		t.Errorf("limit=0 = %d", r.code)
	}
	// Another user has no cards and cannot read ours.
	if o := decodeReply[cardPage](t, e.req(t, "GET", "/cards", e.other, nil, nil)); len(o.Items) != 0 {
		t.Errorf("other user's cards = %+v", o)
	}
	if r := e.req(t, "GET", "/cards/"+ps[0].ID.String(), e.other, nil, nil); r.code != http.StatusNotFound {
		t.Errorf("other user's card = %d", r.code)
	}
}
