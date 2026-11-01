// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"encoding/json"
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
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/web"
)

type todayEnv struct {
	mux      *http.ServeMux
	clock    *domain.FakeClock
	owner    domain.ID
	problems *problems.Service
	answers  *attempts.Service
	cards    *review.CardService
	sessions *review.SessionService
}

func newTodayEnv(t *testing.T) todayEnv {
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
	clock := domain.NewFakeClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	u := auth.User{ID: ids.New(), DisplayName: "U", Email: "u@example.com", CreatedAt: clock.Now()}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	cards := store.DefaultCards()
	probs := problems.NewService(st.Problems(), store.ProblemsEditTx(st, cards.MajorEdit), clock, ids)
	answers := attempts.NewService(store.AttemptsCardTx(st, cards.CardUpdater), probs, clock, ids, attempts.WithReader(st.Attempts()))
	sessions := review.NewSessionService(cards.SessionTx(st), probs, nil, clock, ids)

	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := r.Cookie("ok"); err != nil {
				web.RedirectToLogin(w, r)
				return
			}
			ws := auth.WebSession{UserID: u.ID, CSRFToken: testCSRF}
			next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), ws)))
		})
	}
	site, err := web.New(web.Config{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	site.UseToday(cards.Builder(st), sessions, clock.Now)
	mux := http.NewServeMux()
	site.Register(mux)
	return todayEnv{mux: mux, clock: clock, owner: u.ID, problems: probs, answers: answers,
		cards: review.NewCardService(st.Cards(), nil), sessions: sessions}
}

func (e todayEnv) liveProblem(t *testing.T, prompt string) problems.View {
	t.Helper()
	v, err := e.problems.Create(context.Background(), domain.Actor{Kind: domain.ActorHuman, UserID: e.owner, ClientID: "c"}, problems.CreateInput{
		Type: problems.TypeOpen, Prompt: prompt, Payload: []byte(`{"reference_answer":"x"}`), Status: problems.StatusLive})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (e todayEnv) answer(t *testing.T, v problems.View, rating int) {
	t.Helper()
	_, err := e.answers.Answer(context.Background(), domain.Actor{Kind: domain.ActorHuman, UserID: e.owner, ClientID: "c"},
		attempts.AnswerInput{ProblemID: v.ID, ProblemVersion: v.Version, Response: json.RawMessage(`{"text":"x"}`), Rating: &rating})
	if err != nil {
		t.Fatal(err)
	}
}

// dueFromEndpoint counts the cards GET /cards?due_before=now lists, page by page.
func (e todayEnv) dueFromEndpoint(t *testing.T) int {
	t.Helper()
	n, cursor := 0, ""
	for {
		res, err := e.cards.List(context.Background(), e.owner, review.CardQuery{
			DueBefore: e.clock.Now().Format(time.RFC3339), Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		n += len(res.Items)
		if res.NextCursor == nil {
			return n
		}
		cursor = *res.NextCursor
	}
}

func pageInt(t *testing.T, body, id string) int {
	t.Helper()
	m := regexp.MustCompile(`id="` + id + `">(\d+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page has no #%s:\n%s", id, body)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestTodayCountsMatchCardsEndpointAndDayLimits(t *testing.T) {
	e := newTodayEnv(t)
	var ps []problems.View
	for i := 0; i < 6; i++ {
		ps = append(ps, e.liveProblem(t, "p"+strconv.Itoa(i)))
	}
	// Day 1: introduce four cards.
	for _, p := range ps[:4] {
		e.answer(t, p, 3)
	}
	// Two days on, every card is due and a new study day has begun.
	e.clock.Advance(48 * time.Hour)
	if got := e.dueFromEndpoint(t); got != 4 {
		t.Fatalf("setup: endpoint lists %d due cards, want 4", got)
	}
	body := get(e.mux, "/", true).Body.String()
	if got := pageInt(t, body, "due"); got != 4 {
		t.Errorf("due = %d, want 4", got)
	}
	if got := pageInt(t, body, "reviews-left"); got != 200 {
		t.Errorf("reviews left = %d, want the full 200", got)
	}
	if got := pageInt(t, body, "new-left"); got != 20 {
		t.Errorf("new left = %d, want the full 20", got)
	}

	// Today: review one due card, introduce one new card.
	e.answer(t, ps[0], 3)
	e.answer(t, ps[4], 3)
	want := e.dueFromEndpoint(t)
	body = get(e.mux, "/", true).Body.String()
	if got := pageInt(t, body, "due"); got != want {
		t.Errorf("due = %d, endpoint lists %d", got, want)
	}
	if got := pageInt(t, body, "reviews-left"); got != 199 {
		t.Errorf("reviews left = %d, want 199", got)
	}
	if got := pageInt(t, body, "new-left"); got != 19 {
		t.Errorf("new left = %d, want 19", got)
	}
}

func TestTodayHasNothingDue(t *testing.T) {
	body := get(newTodayEnv(t).mux, "/", true).Body.String()
	if pageInt(t, body, "due") != 0 || pageInt(t, body, "new-left") != 20 {
		t.Errorf("unexpected counts:\n%s", body)
	}
}

func (e todayEnv) post(token string) *httptest.ResponseRecorder {
	form := url.Values{}
	if token != "" {
		form.Set("csrf_token", token)
	}
	req := httptest.NewRequest("POST", "/review/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ok", Value: "1"})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func TestStartReviewCreatesSessionAndRedirects(t *testing.T) {
	e := newTodayEnv(t)
	e.liveProblem(t, "a")
	e.liveProblem(t, "b")
	rec := e.post(testCSRF)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	idStr, ok := strings.CutPrefix(loc, "/review/")
	if !ok {
		t.Fatalf("Location = %q", loc)
	}
	id, err := domain.ParseID(idStr)
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.sessions.Get(context.Background(), e.owner, id)
	if err != nil {
		t.Fatalf("session %s not created: %v", id, err)
	}
	if s.Kind != review.SessionReview || len(s.Items) != 2 {
		t.Errorf("session = kind %s with %d items", s.Kind, len(s.Items))
	}
}

func TestStartReviewRequiresCSRFAndSession(t *testing.T) {
	e := newTodayEnv(t)
	for _, tok := range []string{"", "wrong"} {
		if rec := e.post(tok); rec.Code != http.StatusForbidden {
			t.Errorf("token %q: status = %d, want 403", tok, rec.Code)
		}
	}
	req := httptest.NewRequest("POST", "/review/start", nil)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
		t.Errorf("anonymous: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTodayFormCarriesCSRFToken(t *testing.T) {
	body := get(newTodayEnv(t).mux, "/", true).Body.String()
	if forms, tokens := strings.Count(body, "<form"), strings.Count(body, `name="csrf_token" value="`+testCSRF+`"`); forms != tokens || forms < 2 {
		t.Fatalf("forms = %d, tokens = %d", forms, tokens)
	}
	if !strings.Contains(body, "Start review") {
		t.Error("no Start review button")
	}
}
