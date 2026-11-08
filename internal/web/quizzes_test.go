// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/web"
)

type quizEnv struct {
	*probEnv
	quizzes  *quizzes.Service
	sessions *review.SessionService
	answers  *attempts.Service
	other    domain.ID
}

func newQuizEnv(t *testing.T) *quizEnv {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := domain.NewFakeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	u := auth.User{ID: ids.New(), DisplayName: "A", Email: "a@example.com", CreatedAt: clock.Now()}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	u2 := auth.User{ID: ids.New(), DisplayName: "B", Email: "b@example.com", CreatedAt: clock.Now()}
	if err := st.Users().Create(ctx, u2); err != nil {
		t.Fatal(err)
	}
	probs := problems.NewService(st.Problems(), store.ProblemsTx(st), clock, ids)
	qsvc := quizzes.NewService(store.QuizzesTx(st), probs, clock, ids)
	ssvc := review.NewSessionService(store.DefaultCards().SessionTx(st), probs, nil, clock, ids)
	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := r.Cookie("ok"); err != nil {
				web.RedirectToLogin(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), auth.WebSession{UserID: u.ID, CSRFToken: testCSRF})))
		})
	}
	site, err := web.New(web.Config{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	site.UseProblems(probs, nil)
	answers := attempts.NewService(store.AttemptsCardTx(st, store.DefaultCards().CardUpdater), probs, clock, ids, attempts.WithReader(st.Attempts()))
	site.UseQuizzes(qsvc, probs, ssvc)
	site.UseReview(ssvc, answers, probs)
	mux := http.NewServeMux()
	site.Register(mux)
	return &quizEnv{probEnv: &probEnv{t: t, mux: mux, svc: probs, owner: u.ID}, quizzes: qsvc, sessions: ssvc, answers: answers, other: u2.ID}
}

func (e *quizEnv) problem(prompt string) domain.ID {
	f := mcqForm()
	f.Set("prompt", prompt)
	return e.create(f)
}

var quizLocRe = regexp.MustCompile(`^/quizzes/([0-9a-z-]+)\?notice=`)

func (e *quizEnv) createQuiz(form url.Values) domain.ID {
	e.t.Helper()
	rec := e.do("POST", "/quizzes", form, true)
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("create quiz = %d\n%s", rec.Code, rec.Body)
	}
	m := quizLocRe.FindStringSubmatch(rec.Header().Get("Location"))
	if m == nil {
		e.t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	id, err := domain.ParseID(m[1])
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *quizEnv) items(id domain.ID) []string {
	e.t.Helper()
	q, err := e.quizzes.Get(context.Background(), e.owner, id)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, p := range q.Items {
		out = append(out, p.String())
	}
	return out
}

func TestQuizPagesRequireLoginAndCSRF(t *testing.T) {
	e := newQuizEnv(t)
	id := e.createQuiz(url.Values{"title": {"Q"}, "status": {"live"}})
	for _, rt := range [][2]string{{"GET", "/quizzes"}, {"GET", "/quizzes/new"}, {"GET", "/quizzes/" + id.String()},
		{"POST", "/quizzes"}, {"POST", "/quizzes/" + id.String()}, {"POST", "/quizzes/" + id.String() + "/start"}} {
		req := httptest.NewRequest(rt[0], rt[1], nil)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
			t.Fatalf("%s %s anonymous = %d %q", rt[0], rt[1], rec.Code, rec.Header().Get("Location"))
		}
	}
	for _, path := range []string{"/quizzes", "/quizzes/" + id.String(), "/quizzes/" + id.String() + "/start"} {
		if rec := e.do("POST", path, url.Values{"title": {"x"}, "order": {"ordered"}}, false); rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without CSRF = %d, want 403", path, rec.Code)
		}
	}
	page := e.do("GET", "/quizzes/new", nil, false).Body.String()
	if !strings.Contains(page, `name="csrf_token" value="`+testCSRF+`"`) {
		t.Fatalf("form lacks CSRF token:\n%s", page)
	}
}

func TestQuizCreateOrderAndStartEndToEnd(t *testing.T) {
	e := newQuizEnv(t)
	a, b, c := e.problem("Alpha question"), e.problem("Bravo question"), e.problem("Charlie question")

	// Create with items a then b; the list shows it with a Start button once live.
	id := e.createQuiz(url.Values{"title": {"Geo"}, "description": {"d"}, "status": {"live"}, "add": {a.String(), b.String()}})
	if got := e.items(id); strings.Join(got, ",") != a.String()+","+b.String() {
		t.Fatalf("items = %v", got)
	}
	list := e.do("GET", "/quizzes", nil, false).Body.String()
	if !strings.Contains(list, "Geo") || !strings.Contains(list, "/quizzes/"+id.String()+"/start") {
		t.Fatalf("list page:\n%s", list)
	}

	// Edit page names the items and offers the remaining problem.
	edit := e.do("GET", "/quizzes/"+id.String(), nil, false)
	body := edit.Body.String()
	for _, want := range []string{"Alpha question", "Bravo question", "Charlie question", `value="` + c.String() + `"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("edit page missing %q:\n%s", want, body)
		}
	}

	// Reorder (b first), drop a, add c.
	save := url.Values{"etag": {etagOf(t, body)}, "title": {"Geo"}, "status": {"live"},
		"item": {a.String(), b.String()}, "pos_" + a.String(): {"2"}, "pos_" + b.String(): {"1"},
		"remove_" + a.String(): {"1"}, "add": {c.String()}}
	if rec := e.do("POST", "/quizzes/"+id.String(), save, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d\n%s", rec.Code, rec.Body)
	}
	if got := e.items(id); strings.Join(got, ",") != b.String()+","+c.String() {
		t.Fatalf("after save items = %v", got)
	}
	// Reorder only: c before b.
	body = e.do("GET", "/quizzes/"+id.String(), nil, false).Body.String()
	save = url.Values{"etag": {etagOf(t, body)}, "title": {"Geo"}, "status": {"live"},
		"item": {b.String(), c.String()}, "pos_" + b.String(): {"5"}, "pos_" + c.String(): {"3"}}
	if rec := e.do("POST", "/quizzes/"+id.String(), save, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("reorder = %d\n%s", rec.Code, rec.Body)
	}
	if got := e.items(id); strings.Join(got, ",") != c.String()+","+b.String() {
		t.Fatalf("after reorder items = %v", got)
	}

	// A stale form is refused.
	if rec := e.do("POST", "/quizzes/"+id.String(), save, true); rec.Code != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409", rec.Code)
	}

	// Start ordered: the session is frozen with the quiz's items in order.
	rec := e.do("POST", "/quizzes/"+id.String()+"/start", url.Values{"order": {"ordered"}}, true)
	sid := e.sessionFrom(rec)
	sv, err := e.sessions.Get(context.Background(), e.owner, sid)
	if err != nil {
		t.Fatal(err)
	}
	if sv.Kind != review.SessionQuiz || sv.QuizID == nil || *sv.QuizID != id || len(sv.Items) != 2 ||
		sv.Items[0].ProblemID != c || sv.Items[1].ProblemID != b {
		t.Fatalf("ordered session = %+v", sv)
	}
	// Editing the quiz afterwards does not change the frozen session.
	body = e.do("GET", "/quizzes/"+id.String(), nil, false).Body.String()
	e.do("POST", "/quizzes/"+id.String(), url.Values{"etag": {etagOf(t, body)}, "title": {"Geo"}, "status": {"live"},
		"item": {b.String(), c.String()}, "remove_" + b.String(): {"1"}}, true)
	if sv2, _ := e.sessions.Get(context.Background(), e.owner, sid); len(sv2.Items) != 2 {
		t.Fatalf("session not frozen: %+v", sv2)
	}

}

func (e *quizEnv) sessionFrom(rec *httptest.ResponseRecorder) domain.ID {
	e.t.Helper()
	res := rec.Result()
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/review/") {
		e.t.Fatalf("start = %d %q, want 303 to /review/{id}", res.StatusCode, loc)
	}
	id, err := domain.ParseID(strings.TrimPrefix(loc, "/review/"))
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestQuizShuffledStartMatchesBuilderShuffle(t *testing.T) {
	e := newQuizEnv(t)
	var adds []string
	var authored []domain.ID
	for i := range 8 {
		id := e.problem("P" + strconv.Itoa(i))
		adds = append(adds, id.String())
		authored = append(authored, id)
	}
	id := e.createQuiz(url.Values{"title": {"Eight"}, "status": {"live"}, "add": adds})
	sid := e.sessionFrom(e.do("POST", "/quizzes/"+id.String()+"/start", url.Values{"order": {"shuffled"}}, true))
	sv, err := e.sessions.Get(context.Background(), e.owner, sid)
	if err != nil {
		t.Fatal(err)
	}
	// The expected plan comes from the same builder, seeded by the session ID.
	want, err := review.NewQuizBuilder(fakeQuizSource{authored}, fakeProblems{authored}).Build(context.Background(), e.owner, sid, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sv.Items) != len(want.Items) || len(want.Items) != 8 {
		t.Fatalf("got %d items, want %d", len(sv.Items), len(want.Items))
	}
	same := true
	for i := range sv.Items {
		if sv.Items[i].ProblemID != want.Items[i].ProblemID {
			t.Fatalf("item %d = %s, want %s (shuffle ignored)", i, sv.Items[i].ProblemID, want.Items[i].ProblemID)
		}
		if sv.Items[i].ProblemID != authored[i] {
			same = false
		}
	}
	if same {
		t.Fatal("shuffled session equals authored order; the shuffle did not run")
	}
}

type fakeQuizSource struct{ items []domain.ID }

func (f fakeQuizSource) QuizProblems(context.Context, domain.ID, domain.ID) (review.QuizContents, error) {
	return review.QuizContents{Live: true, Problems: f.items}, nil
}

type fakeProblems struct{ ids []domain.ID }

func (f fakeProblems) Epoch(context.Context, domain.ID, domain.ID) (review.ProblemEpoch, error) {
	return review.ProblemEpoch{}, review.ErrNotFound
}

func (f fakeProblems) LiveEpochs(context.Context, domain.ID) ([]review.ProblemEpoch, error) {
	var out []review.ProblemEpoch
	for _, id := range f.ids {
		out = append(out, review.ProblemEpoch{ID: id, Live: true, Epoch: 1})
	}
	return out, nil
}

func TestQuizCrossOwner(t *testing.T) {
	e := newQuizEnv(t)
	ctx := context.Background()
	mine := e.problem("Mine")
	qid := e.createQuiz(url.Values{"title": {"Mine"}, "status": {"live"}, "add": {mine.String()}})
	// A second owner with a quiz and a problem of their own.
	actor := domain.Actor{Kind: domain.ActorHuman, UserID: e.other, ClientID: "t"}
	theirP, err := e.svc.Create(ctx, actor, problems.CreateInput{Type: "open", Prompt: "Theirs",
		Payload: []byte(`{"reference_answer":"x","rubric":["y"]}`), Status: problems.StatusLive})
	if err != nil {
		t.Fatal(err)
	}
	theirQ, err := e.quizzes.Create(ctx, actor, quizzes.CreateInput{Title: "Theirs", Status: quizzes.StatusLive, ProblemIDs: []string{theirP.ID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	tq := theirQ.ID.String()
	if rec := e.do("GET", "/quizzes/"+tq, nil, false); rec.Code != http.StatusNotFound {
		t.Fatalf("GET other's quiz = %d", rec.Code)
	}
	if rec := e.do("POST", "/quizzes/"+tq, url.Values{"etag": {quizzes.ETag(theirQ)}, "title": {"hijack"}, "status": {"live"}}, true); rec.Code != http.StatusNotFound {
		t.Fatalf("POST other's quiz = %d", rec.Code)
	}
	if rec := e.do("POST", "/quizzes/"+tq+"/start", url.Values{"order": {"ordered"}}, true); rec.Code != http.StatusNotFound {
		t.Fatalf("start other's quiz = %d", rec.Code)
	}
	got, err := e.quizzes.Get(ctx, e.other, theirQ.ID)
	if err != nil || got.Title != "Theirs" {
		t.Fatalf("other's quiz changed: %+v %v", got, err)
	}
	// Another owner's problem cannot be added to my quiz.
	body := e.do("GET", "/quizzes/"+qid.String(), nil, false).Body.String()
	rec := e.do("POST", "/quizzes/"+qid.String(), url.Values{"etag": {etagOf(t, body)}, "title": {"Mine"}, "status": {"live"},
		"item": {mine.String()}, "add": {theirP.ID.String()}}, true)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("add other's problem = %d", rec.Code)
	}
	if items := e.items(qid); len(items) != 1 || items[0] != mine.String() {
		t.Fatalf("items = %v", items)
	}
	if rec := e.do("POST", "/quizzes", url.Values{"title": {"X"}, "add": {theirP.ID.String()}}, true); rec.Code != http.StatusNotFound && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create with other's problem = %d", rec.Code)
	}
}

func TestQuizStartRefusesDraftAndBadInput(t *testing.T) {
	e := newQuizEnv(t)
	p := e.problem("Only")
	id := e.createQuiz(url.Values{"title": {"Draft one"}, "status": {"draft"}, "add": {p.String()}})
	rec := e.do("POST", "/quizzes/"+id.String()+"/start", url.Values{"order": {"ordered"}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not live") {
		t.Fatalf("draft start = %d\n%s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/quizzes/"+id.String()+"/start", url.Values{"order": {"sideways"}}, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad order = %d", rec.Code)
	}
	if rec := e.do("POST", "/quizzes/"+domain.NewIDGenerator(domain.SystemClock{}).New().String()+"/start", url.Values{"order": {"ordered"}}, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown quiz = %d", rec.Code)
	}
	if rec := e.do("POST", "/quizzes", url.Values{"title": {""}}, true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty title = %d", rec.Code)
	}
}

// M5.6 end to end: create a quiz, Start it, follow the redirect, answer every
// item through the review handlers, and the quiz session completes.
func TestQuizStartThroughReviewToCompletion(t *testing.T) {
	e := newQuizEnv(t)
	a, b := e.problem("First?"), e.problem("Second?")
	id := e.createQuiz(url.Values{"title": {"Geo"}, "status": {"live"}, "item": {a.String(), b.String()}})
	sid := e.sessionFrom(e.do("POST", "/quizzes/"+id.String()+"/start", url.Values{"order": {"ordered"}}, true))
	hidden := regexp.MustCompile(`name="(problem_id|problem_version)" value="([^"]*)"`)
	choice := regexp.MustCompile(`name="selected" value="([^"]+)"`)
	var order []string
	for i := range 2 {
		page := e.do("GET", "/review/"+sid.String(), nil, true)
		body := page.Body.String()
		if page.Code != http.StatusOK || !strings.Contains(body, "Quiz") || !strings.Contains(body, `data-review-phase="prompt"`) {
			t.Fatalf("item %d: %d\n%s", i, page.Code, body)
		}
		form := url.Values{"selected": {choice.FindStringSubmatch(body)[1]}, "latency_ms": {"900"}}
		for _, m := range hidden.FindAllStringSubmatch(body, -1) {
			form.Set(m[1], m[2])
		}
		order = append(order, form.Get("problem_id"))
		if rec := e.do("POST", "/review/"+sid.String()+"/answer", form, true); rec.Code != http.StatusSeeOther {
			t.Fatalf("answer %d: %d\n%s", i, rec.Code, rec.Body)
		}
	}
	if order[0] != a.String() || order[1] != b.String() {
		t.Errorf("served %v, want authored order %s, %s", order, a, b)
	}
	done := e.do("GET", "/review/"+sid.String(), nil, true).Body.String()
	if !strings.Contains(done, "Quiz complete") || !strings.Contains(done, "2 answered") {
		t.Fatalf("not complete:\n%s", done)
	}
	if sv, err := e.sessions.Get(context.Background(), e.owner, sid); err != nil || sv.CompletedAt == nil || sv.Answered != 2 {
		t.Errorf("session = %+v, err %v", sv, err)
	}
	res, _ := e.answers.List(context.Background(), e.owner, attempts.ListQuery{Limit: 10})
	if len(res.Items) != 2 || res.Items[0].LatencyMS != 900 {
		t.Errorf("attempts = %+v", res.Items)
	}
}
