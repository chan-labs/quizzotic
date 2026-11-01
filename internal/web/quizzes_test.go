// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	site.UseQuizzes(qsvc, probs, ssvc)
	mux := http.NewServeMux()
	site.Register(mux)
	return &quizEnv{probEnv: &probEnv{t: t, mux: mux, svc: probs, owner: u.ID}, quizzes: qsvc, sessions: ssvc}
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
	for _, path := range []string{"/quizzes", "/quizzes/new"} {
		req, _ := http.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
			t.Fatalf("%s anonymous = %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	id := e.createQuiz(url.Values{"title": {"Q"}, "status": {"live"}})
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

	// Start shuffled: same items, session built with the shuffle flag, and the
	// same shuffled order as the API would give for that session ID.
	rec = e.do("POST", "/quizzes/"+id.String()+"/start", url.Values{"order": {"shuffled"}}, true)
	sid2 := e.sessionFrom(rec)
	sv3, err := e.sessions.Get(context.Background(), e.owner, sid2)
	if err != nil || sid2 == sid || len(sv3.Items) != 1 {
		t.Fatalf("shuffled session = %+v err %v", sv3, err)
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

func TestQuizShuffledStartKeepsAllItems(t *testing.T) {
	e := newQuizEnv(t)
	var adds []string
	for _, p := range []string{"P1", "P2", "P3", "P4", "P5", "P6"} {
		adds = append(adds, e.problem(p).String())
	}
	id := e.createQuiz(url.Values{"title": {"Six"}, "status": {"live"}, "add": adds})
	sid := e.sessionFrom(e.do("POST", "/quizzes/"+id.String()+"/start", url.Values{"order": {"shuffled"}}, true))
	sv, err := e.sessions.Get(context.Background(), e.owner, sid)
	if err != nil || len(sv.Items) != 6 {
		t.Fatalf("session = %+v err %v", sv, err)
	}
	seen := map[string]bool{}
	for _, it := range sv.Items {
		seen[it.ProblemID.String()] = true
	}
	for _, a := range adds {
		if !seen[a] {
			t.Fatalf("shuffled session lost %s", a)
		}
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
