// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

// flowEnv runs the Settings page over the real store, the real settings
// service (with the real card recompute installed, as the server does) and the
// real session service.
type flowEnv struct {
	t        *testing.T
	mux      *http.ServeMux
	st       store.Store
	clock    *domain.FakeClock
	owner    domain.ID
	problems *problems.Service
	attempts *attempts.Service
	sessions *review.SessionService
}

func newFlowEnv(t *testing.T) *flowEnv {
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
	clock := domain.NewFakeClock(time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	u := auth.User{ID: ids.New(), DisplayName: "A", Email: "a@example.com", CreatedAt: clock.Now()}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	cards := store.DefaultCards()
	probSvc := problems.NewService(st.Problems(), store.ProblemsEditTx(st, cards.MajorEdit), clock, ids)
	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), auth.WebSession{UserID: u.ID, CSRFToken: testCSRF})))
		})
	}
	site, err := web.New(web.Config{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	site.UseSettings(cards.SettingsService(st))
	mux := http.NewServeMux()
	site.Register(mux)
	return &flowEnv{
		t: t, mux: mux, st: st, clock: clock, owner: u.ID, problems: probSvc,
		attempts: attempts.NewService(store.AttemptsCardTx(st, cards.CardUpdater), probSvc, clock, ids),
		sessions: review.NewSessionService(cards.SessionTx(st), probSvc, nil, clock, ids),
	}
}

func (e *flowEnv) saveSettings(form url.Values) {
	e.t.Helper()
	form.Set("csrf_token", testCSRF)
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("POST /settings = %d: %s", rec.Code, rec.Body)
	}
}

func (e *flowEnv) actor() domain.Actor {
	return domain.Actor{Kind: domain.ActorHuman, UserID: e.owner, ClientID: "c"}
}

func (e *flowEnv) liveProblem() domain.ID {
	e.t.Helper()
	v, err := e.problems.Create(context.Background(), e.actor(), problems.CreateInput{
		Type: problems.TypeOpen, Prompt: "p", Payload: []byte(`{"reference_answer":"x"}`), Status: problems.StatusLive})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.ID
}

func (e *flowEnv) newCards() int {
	e.t.Helper()
	v, err := e.sessions.Create(context.Background(), e.owner, review.CreateSessionInput{Kind: review.SessionReview})
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, it := range v.Items {
		if it.Kind == review.ItemNew {
			n++
		}
	}
	return n
}

func settingsForm(over map[string]string) url.Values {
	f := validForm()
	f.Del("csrf_token")
	for k, v := range over {
		f.Set(k, v)
	}
	return f
}

// Done when: changing new_per_day changes the next session's new-card count.
func TestChangingNewPerDayChangesNextSessionNewCardCount(t *testing.T) {
	e := newFlowEnv(t)
	for range 6 {
		e.liveProblem()
	}
	e.saveSettings(settingsForm(map[string]string{"new_per_day": "2"}))
	if got := e.newCards(); got != 2 {
		t.Fatalf("new cards with new_per_day=2: %d", got)
	}
	e.saveSettings(settingsForm(map[string]string{"new_per_day": "5"}))
	if got := e.newCards(); got != 5 {
		t.Fatalf("new cards with new_per_day=5: %d", got)
	}
	e.saveSettings(settingsForm(map[string]string{"new_per_day": "0"}))
	if got := e.newCards(); got != 0 {
		t.Fatalf("new cards with new_per_day=0: %d", got)
	}
}

// Saving a changed desired retention recomputes the user's cards with it.
func TestSavingRetentionRecomputesCards(t *testing.T) {
	e := newFlowEnv(t)
	ctx := context.Background()
	p := e.liveProblem()
	three := 3
	for range 5 {
		if _, err := e.attempts.Answer(ctx, e.actor(), attempts.AnswerInput{ProblemID: p, ProblemVersion: 1,
			Response: []byte(`{"text":"x"}`), Rating: &three}); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(24 * time.Hour)
	}
	dueOf := func() time.Time {
		cs, err := e.st.Cards().List(ctx, e.owner)
		if err != nil || len(cs) != 1 {
			t.Fatalf("cards = %v, err %v", cs, err)
		}
		return cs[0].Due
	}
	before := dueOf()
	e.saveSettings(settingsForm(map[string]string{"desired_retention": "0.7"}))
	low := dueOf()
	if !low.After(before) {
		t.Fatalf("lower retention should push due out: before %v, after %v", before, low)
	}
	e.saveSettings(settingsForm(map[string]string{"desired_retention": "0.97"}))
	if high := dueOf(); !high.Before(low) {
		t.Fatalf("higher retention should pull due in: 0.7 -> %v, 0.97 -> %v", low, high)
	}
}
