// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// moveClock is a clock the test can advance.
type moveClock struct{ t time.Time }

func (c *moveClock) Now() time.Time { return c.t }

// rateEnv is newEnv with a clock that moves.
func rateEnv(t *testing.T) (*env, *moveClock) {
	t.Helper()
	e := newEnv(t)
	clock := &moveClock{now}
	e.svc = NewService(e.repo, e.svc.problems, clock, domain.NewIDGenerator(clock), WithCardUpdater(e.cards))
	e.h = NewHandler(e.svc, nil)
	return e, clock
}

func answer(t *testing.T, e *env, problem domain.ID, version int, response string, rating *int) Result {
	t.Helper()
	res, err := e.svc.Answer(context.Background(), human(e.user), AnswerInput{ProblemID: problem, ProblemVersion: version,
		Response: raw(response), Rating: rating})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func wantProblem(t *testing.T, err error, status int) {
	t.Helper()
	var pb *platform.Problem
	if !errors.As(err, &pb) || pb.Status != status {
		t.Fatalf("err = %v, want a %d problem", err, status)
	}
}

func TestRateOpenAttemptSchedulesIt(t *testing.T) {
	e, _ := rateEnv(t)
	ctx := context.Background()
	unrated := answer(t, e, e.open, 1, `{"text":"later"}`, nil)
	if unrated.Grade.Scheduled || len(e.cards.calls) != 0 {
		t.Fatalf("an open attempt without a rating must stay unscheduled: %+v cards=%v", unrated.Grade, e.cards.calls)
	}
	next := now.Add(48 * time.Hour)
	e.cards.next = &next

	res, err := e.svc.Rate(ctx, human(e.user), unrated.Attempt.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	g := res.Grade
	if !g.Scheduled || g.Rating == nil || *g.Rating != 2 || g.Score == nil || *g.Score != 0.33 || g.Correct != nil {
		t.Errorf("grade = %+v", g)
	}
	if res.Attempt.Rating == nil || *res.Attempt.Rating != 2 || res.Attempt.Score == nil || *res.Attempt.Score != 0.33 {
		t.Errorf("attempt = %+v", res.Attempt)
	}
	if res.NextDue == nil || !res.NextDue.Equal(next) || !bytes.Contains(res.Answer, []byte("ref")) {
		t.Errorf("result = %+v", res)
	}
	if len(e.cards.calls) != 1 || e.cards.calls[0] != 2 {
		t.Errorf("card hook calls = %v, want [2]", e.cards.calls)
	}
	stored, _ := e.repo.Get(ctx, e.user, unrated.Attempt.ID)
	if stored.Rating == nil || *stored.Rating != 2 || stored.Score == nil || *stored.Score != 0.33 {
		t.Errorf("stored = %+v", stored)
	}
}

func TestRateOnlyOnce(t *testing.T) {
	e, _ := rateEnv(t)
	ctx := context.Background()
	unrated := answer(t, e, e.open, 1, `{"text":"x"}`, nil)
	if _, err := e.svc.Rate(ctx, human(e.user), unrated.Attempt.ID, 3); err != nil {
		t.Fatal(err)
	}
	e.cards.calls = nil
	_, err := e.svc.Rate(ctx, human(e.user), unrated.Attempt.ID, 4)
	wantProblem(t, err, http.StatusConflict)
	if len(e.cards.calls) != 0 {
		t.Errorf("a rejected second rating ran the card hook: %v", e.cards.calls)
	}
	if stored, _ := e.repo.Get(ctx, e.user, unrated.Attempt.ID); *stored.Rating != 3 || *stored.Score != 0.67 {
		t.Errorf("stored = %+v; the first rating must stand", stored)
	}

	// An attempt that was rated when it was answered cannot be rated again.
	rated := answer(t, e, e.open, 1, `{"text":"y"}`, ptr(4))
	_, err = e.svc.Rate(ctx, human(e.user), rated.Attempt.ID, 1)
	wantProblem(t, err, http.StatusConflict)
}

func TestRateWindow(t *testing.T) {
	e, clock := rateEnv(t)
	ctx := context.Background()

	t.Run("at the edge of the window is allowed", func(t *testing.T) {
		a := answer(t, e, e.open, 1, `{"text":"x"}`, nil)
		clock.t = now.Add(RatingWindow)
		if _, err := e.svc.Rate(ctx, human(e.user), a.Attempt.ID, 3); err != nil {
			t.Fatalf("rating exactly 15 minutes later: %v", err)
		}
	})
	t.Run("after the window is refused and nothing changes", func(t *testing.T) {
		clock.t = now
		a := answer(t, e, e.open, 1, `{"text":"x"}`, nil)
		clock.t = now.Add(RatingWindow + time.Second)
		e.cards.calls = nil
		_, err := e.svc.Rate(ctx, human(e.user), a.Attempt.ID, 3)
		wantProblem(t, err, http.StatusConflict)
		stored, _ := e.repo.Get(ctx, e.user, a.Attempt.ID)
		if stored.Rating != nil || stored.Score != nil || len(e.cards.calls) != 0 {
			t.Errorf("stored = %+v cards=%v; an expired rating must change nothing", stored, e.cards.calls)
		}
	})
}

func TestRateOpenStaysUnscheduledUntilRated(t *testing.T) {
	e, clock := rateEnv(t)
	ctx := context.Background()
	a := answer(t, e, e.open, 1, `{"text":"x"}`, nil)
	clock.t = now.Add(time.Hour)
	if _, err := e.svc.Rate(ctx, human(e.user), a.Attempt.ID, 3); err == nil {
		t.Fatal("rated after the window")
	}
	// Past the window the open attempt is still unrated, unscored and never
	// reached the card.
	stored, _ := e.repo.Get(ctx, e.user, a.Attempt.ID)
	if stored.Rating != nil || stored.Score != nil || len(e.cards.calls) != 0 {
		t.Errorf("stored = %+v cards=%v", stored, e.cards.calls)
	}
}

func TestRateAutoGradedRules(t *testing.T) {
	e, _ := rateEnv(t)
	ctx := context.Background()

	right := answer(t, e, e.mcq, 2, `{"selected":["b"]}`, nil)
	_, err := e.svc.Rate(ctx, human(e.user), right.Attempt.ID, 1)
	if !errors.Is(err, grading.ErrInvalidRating) { // the handler renders it as a 422 on rating
		t.Fatalf("err = %v, want ErrInvalidRating", err)
	}
	if stored, _ := e.repo.Get(ctx, e.user, right.Attempt.ID); stored.Rating != nil {
		t.Fatal("an invalid rating was stored")
	}
	e.cards.calls = nil
	res, err := e.svc.Rate(ctx, human(e.user), right.Attempt.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if *res.Grade.Rating != 4 || *res.Grade.Score != 1 || !*res.Grade.Correct || len(e.cards.calls) != 1 || e.cards.calls[0] != 4 {
		t.Errorf("res = %+v cards=%v", res.Grade, e.cards.calls)
	}

	wrong := answer(t, e, e.mcq, 2, `{"selected":["a"]}`, nil)
	e.cards.calls = nil
	res, err = e.svc.Rate(ctx, human(e.user), wrong.Attempt.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if *res.Grade.Rating != 1 || *res.Grade.Score != 0 || len(e.cards.calls) != 1 || e.cards.calls[0] != 1 {
		t.Errorf("a wrong answer is Again whatever is sent: %+v cards=%v", res.Grade, e.cards.calls)
	}
}

func TestRateRefusals(t *testing.T) {
	e, _ := rateEnv(t)
	ctx := context.Background()
	a := answer(t, e, e.open, 1, `{"text":"x"}`, nil)

	if _, err := e.svc.Rate(ctx, domain.Actor{Kind: domain.ActorAgent, UserID: e.user}, a.Attempt.ID, 3); err == nil {
		t.Error("an agent rated an attempt")
	}
	for _, n := range []int{0, 5, -1} {
		_, err := e.svc.Rate(ctx, human(e.user), a.Attempt.ID, n)
		wantProblem(t, err, http.StatusUnprocessableEntity)
	}
	_, err := e.svc.Rate(ctx, human(e.user), domain.NewIDGenerator(domain.SystemClock{}).New(), 3)
	wantProblem(t, err, http.StatusNotFound)
	// Another user's attempt looks missing.
	other := domain.NewIDGenerator(domain.SystemClock{}).New()
	_, err = e.svc.Rate(ctx, human(other), a.Attempt.ID, 3)
	wantProblem(t, err, http.StatusNotFound)
	if stored, _ := e.repo.Get(ctx, e.user, a.Attempt.ID); stored.Rating != nil {
		t.Error("a refused request stored a rating")
	}
}

func TestRateCardFailureRollsBack(t *testing.T) {
	e, _ := rateEnv(t)
	ctx := context.Background()
	a := answer(t, e, e.open, 1, `{"text":"x"}`, nil)
	e.cards.failed = errors.New("boom")
	if _, err := e.svc.Rate(ctx, human(e.user), a.Attempt.ID, 3); err == nil {
		t.Fatal("card failure was swallowed")
	}
	if stored, _ := e.repo.Get(ctx, e.user, a.Attempt.ID); stored.Rating != nil {
		t.Errorf("rating survived a failed card update: %+v", stored)
	}
}

func TestRateHandler(t *testing.T) {
	e, _ := rateEnv(t)
	a := answer(t, e, e.open, 1, `{"text":"x"}`, nil)
	do := func(kind auth.Kind, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Kind: kind, UserID: e.user}))
		req.SetPathValue("id", a.Attempt.ID.String())
		w := httptest.NewRecorder()
		e.h.Rate(w, req)
		return w
	}
	p := "/attempts/" + a.Attempt.ID.String() + "/rating"
	if w := do(auth.KindAgent, p, `{"rating":3}`); w.Code != http.StatusForbidden {
		t.Errorf("agent = %d", w.Code)
	}
	if w := do(auth.KindHuman, p, `{}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("missing rating = %d", w.Code)
	}
	if w := do(auth.KindHuman, p, `{"rating":9}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad rating = %d", w.Code)
	}
	if w := do(auth.KindHuman, p, `{"rating":3}`); w.Code != http.StatusOK {
		t.Fatalf("rate = %d (%s)", w.Code, w.Body)
	}
	if w := do(auth.KindHuman, p, `{"rating":3}`); w.Code != http.StatusConflict {
		t.Errorf("second rate = %d", w.Code)
	}
}
