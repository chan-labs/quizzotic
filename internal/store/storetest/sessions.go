// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
)

// countRecorder counts sessions served.
type countRecorder struct{ n int }

func (c *countRecorder) RecordSessionServed() { c.n++ }

// runSessions runs the session repo and the session service over the real
// stores, and the card query that backs GET /cards.
func runSessions(t *testing.T, newStore NewStore) {
	t.Run("repo", func(t *testing.T) { runSessionRepo(t, newStore) })
	t.Run("service", func(t *testing.T) { runSessionService(t, newStore) })
	t.Run("card query", func(t *testing.T) { runCardQuery(t, newStore) })
}

func runSessionRepo(t *testing.T, newStore NewStore) {
	f := newCardFixture(t, newStore)
	ids := domain.NewIDGenerator(f.clock)
	k := pkinds[0]
	p1, p2 := f.newProblem(k, problems.StatusLive), f.newProblem(k, problems.StatusLive)
	p3 := f.newProblem(k, problems.StatusLive)
	repo := f.s.Sessions()

	other := newUser(ids, "bob")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}

	mk := func() review.Session {
		return review.Session{ID: ids.New(), UserID: f.user, Kind: review.SessionReview, CreatedAt: t0,
			Items: []review.Item{{ProblemID: p1.ID, Epoch: 1, Kind: review.ItemReview}, {ProblemID: p2.ID, Epoch: 2, Kind: review.ItemNew}}}
	}

	t.Run("round trip", func(t *testing.T) {
		s := mk()
		if err := repo.Create(f.ctx, f.user, s); err != nil {
			t.Fatal(err)
		}
		got, err := repo.Get(f.ctx, f.user, s.ID)
		if err != nil || !reflect.DeepEqual(got, s) {
			t.Fatalf("Get = %+v, %v; want %+v", got, err, s)
		}
		empty := review.Session{ID: ids.New(), UserID: f.user, Kind: review.SessionReview, CreatedAt: t0}
		if err := repo.Create(f.ctx, f.user, empty); err != nil {
			t.Fatal(err)
		}
		if got, err := repo.Get(f.ctx, f.user, empty.ID); err != nil || len(got.Items) != 0 || got.CompletedAt != nil {
			t.Errorf("empty session = %+v, %v", got, err)
		}
	})

	t.Run("append is guarded by the length the caller read", func(t *testing.T) {
		s := mk()
		if err := repo.Create(f.ctx, f.user, s); err != nil {
			t.Fatal(err)
		}
		add := []review.Item{{ProblemID: p3.ID, Epoch: 1, Kind: review.ItemReview}}
		if err := repo.AppendItems(f.ctx, f.user, s.ID, 2, add); err != nil {
			t.Fatal(err)
		}
		// A second appender that still believes the list has 2 items loses.
		if err := repo.AppendItems(f.ctx, f.user, s.ID, 2, add); !errors.Is(err, review.ErrSessionChanged) {
			t.Errorf("stale append = %v, want ErrSessionChanged", err)
		}
		got, _ := repo.Get(f.ctx, f.user, s.ID)
		if want := append(append([]review.Item{}, s.Items...), add...); !reflect.DeepEqual(got.Items, want) {
			t.Errorf("items = %+v, want %+v", got.Items, want)
		}
		if err := repo.AppendItems(f.ctx, f.user, ids.New(), 0, add); !errors.Is(err, review.ErrSessionNotFound) {
			t.Errorf("append to unknown session = %v", err)
		}
	})

	t.Run("complete keeps the first time", func(t *testing.T) {
		s := mk()
		if err := repo.Create(f.ctx, f.user, s); err != nil {
			t.Fatal(err)
		}
		first, second := t0.Add(time.Hour), t0.Add(2*time.Hour)
		if err := repo.Complete(f.ctx, f.user, s.ID, first); err != nil {
			t.Fatal(err)
		}
		if err := repo.Complete(f.ctx, f.user, s.ID, second); err != nil {
			t.Fatal(err)
		}
		got, _ := repo.Get(f.ctx, f.user, s.ID)
		if got.CompletedAt == nil || !got.CompletedAt.Equal(first) {
			t.Errorf("completed_at = %v, want %v", got.CompletedAt, first)
		}
		if err := repo.Complete(f.ctx, f.user, ids.New(), first); !errors.Is(err, review.ErrSessionNotFound) {
			t.Errorf("complete unknown = %v", err)
		}
	})

	t.Run("owner scoping", func(t *testing.T) {
		s := mk()
		if err := repo.Create(f.ctx, f.user, s); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Get(f.ctx, other.ID, s.ID); !errors.Is(err, review.ErrSessionNotFound) {
			t.Errorf("other's Get = %v", err)
		}
		if err := repo.Complete(f.ctx, other.ID, s.ID, t0); !errors.Is(err, review.ErrSessionNotFound) {
			t.Errorf("other's Complete = %v", err)
		}
		if err := repo.AppendItems(f.ctx, other.ID, s.ID, 2, nil); !errors.Is(err, review.ErrSessionNotFound) {
			t.Errorf("other's AppendItems = %v", err)
		}
		if got, _ := repo.Get(f.ctx, f.user, s.ID); got.CompletedAt != nil || len(got.Items) != 2 {
			t.Errorf("session changed by another owner: %+v", got)
		}
		// A session may only list the owner's problems and quizzes.
		foreign := review.Session{ID: ids.New(), UserID: other.ID, Kind: review.SessionReview, CreatedAt: t0,
			Items: []review.Item{{ProblemID: p1.ID, Epoch: 1, Kind: review.ItemNew}}}
		if err := f.s.WithTx(f.ctx, func(tx store.Store) error { return tx.Sessions().Create(f.ctx, other.ID, foreign) }); !errors.Is(err, review.ErrNotFound) {
			t.Errorf("foreign problem = %v, want ErrNotFound", err)
		}
		if _, err := repo.Get(f.ctx, other.ID, foreign.ID); !errors.Is(err, review.ErrSessionNotFound) {
			t.Errorf("a refused session left a row: %v", err)
		}
		foreignQuiz := review.Session{ID: ids.New(), UserID: other.ID, Kind: review.SessionQuiz, QuizID: ids.New(), CreatedAt: t0}
		if err := repo.Create(f.ctx, other.ID, foreignQuiz); !errors.Is(err, review.ErrNotFound) {
			t.Errorf("unknown quiz = %v, want ErrNotFound", err)
		}
	})
}

// sessionFixture drives the session service the way the API does.
type sessionFixture struct {
	*cardFixture
	svc *review.SessionService
	rec *countRecorder
	ids *domain.IDGenerator
}

func newSessionFixture(t *testing.T, newStore NewStore) *sessionFixture {
	t.Helper()
	f := &sessionFixture{cardFixture: newCardFixture(t, newStore), rec: &countRecorder{}}
	f.ids = domain.NewIDGenerator(f.clock)
	f.svc = review.NewSessionService(f.cards.SessionTx(f.s), f.probs, f.rec, f.clock, f.ids)
	return f
}

// answerIn answers a problem as part of a session.
func (f *sessionFixture) answerIn(session domain.ID, problem domain.ID, response string) attempts.Result {
	f.t.Helper()
	v, err := f.probs.Get(f.ctx, f.user, problem)
	if err != nil {
		f.t.Fatal(err)
	}
	res, err := f.atts.Answer(f.ctx, f.actor, attempts.AnswerInput{ProblemID: problem, ProblemVersion: v.Version,
		Response: json.RawMessage(response), LatencyMS: 5, SessionID: session.String()})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func kinds(v review.SessionView) map[review.ItemKind]int {
	m := map[review.ItemKind]int{}
	for _, it := range v.Items {
		m[it.Kind]++
	}
	return m
}

func runSessionService(t *testing.T, newStore NewStore) {
	k := pkinds[0] // mcq

	t.Run("review session mixes due and new, serves in order and appends learning cards", func(t *testing.T) {
		f := newSessionFixture(t, newStore)
		var due []problems.View
		for i := 0; i < 3; i++ {
			p := f.newProblem(k, problems.StatusLive)
			if _, err := f.answer(p.ID, k.right, nil); err != nil {
				t.Fatal(err)
			}
			due = append(due, p)
		}
		f.clock.Advance(30 * 24 * time.Hour) // all three are now due
		var fresh []problems.View
		for i := 0; i < 2; i++ {
			fresh = append(fresh, f.newProblem(k, problems.StatusLive))
		}
		f.newProblem(k, problems.StatusDraft) // never enters review

		v, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionReview})
		if err != nil {
			t.Fatal(err)
		}
		if got := kinds(v); got[review.ItemReview] != 3 || got[review.ItemNew] != 2 || len(v.Items) != 5 {
			t.Fatalf("mix = %v items=%+v, want 3 due + 2 new", got, v.Items)
		}
		if f.rec.n != 1 {
			t.Errorf("sessions served = %d, want 1", f.rec.n)
		}
		dueIDs := map[domain.ID]bool{due[0].ID: true, due[1].ID: true, due[2].ID: true}
		for _, it := range v.Items {
			if it.Kind == review.ItemReview && !dueIDs[it.ProblemID] {
				t.Errorf("review item %v is not a due card", it.ProblemID)
			}
			if it.Answered {
				t.Errorf("fresh session has an answered item: %+v", it)
			}
		}

		// The list was built with the session's own ID: building again with
		// that ID over the same data reproduces it.
		plan, err := f.cards.Builder(f.s).Build(f.ctx, f.user, v.ID, v.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		var rebuilt []domain.ID
		for _, it := range plan.Items {
			rebuilt = append(rebuilt, it.ProblemID)
		}
		var frozen []domain.ID
		for _, it := range v.Items {
			frozen = append(frozen, it.ProblemID)
		}
		if !reflect.DeepEqual(rebuilt, frozen) {
			t.Errorf("rebuilt from the session ID = %v, frozen = %v", rebuilt, frozen)
		}

		// The frozen list is what is stored, and is unchanged by new data.
		f.newProblem(k, problems.StatusLive)
		got, err := f.svc.Get(f.ctx, f.user, v.ID)
		if err != nil || !reflect.DeepEqual(got, v) {
			t.Fatalf("Get = %+v, %v; want %+v", got, err, v)
		}

		// Serve and answer everything in order; the first answer is wrong so
		// that its card re-enters learning.
		var learning domain.ID
		for i := range v.Items {
			n, err := f.svc.Next(f.ctx, f.user, v.ID)
			if err != nil {
				t.Fatal(err)
			}
			if n.Item == nil || n.Item.Position != i || n.Item.Problem.ID != v.Items[i].ProblemID || n.Remaining != len(v.Items)-i {
				t.Fatalf("next #%d = %+v, want position %d with %d remaining", i, n, i, len(v.Items)-i)
			}
			if n.Item.Kind != v.Items[i].Kind {
				t.Errorf("next #%d kind = %v, want %v", i, n.Item.Kind, v.Items[i].Kind)
			}
			resp := k.right
			if i == 0 {
				resp, learning = k.wrong, n.Item.Problem.ID
			}
			f.answerIn(v.ID, n.Item.Problem.ID, resp)
		}
		done, err := f.svc.Next(f.ctx, f.user, v.ID)
		if err != nil || done.Item != nil || done.Remaining != 0 {
			t.Fatalf("after the last answer next = %+v, %v; want nothing", done, err)
		}
		if c, _ := f.card(learning); c.State != fsrs.Learning && c.State != fsrs.Relearning {
			t.Fatalf("wrong answer left card in state %v", c.State)
		}
		if g, _ := f.svc.Get(f.ctx, f.user, v.ID); g.Answered != 5 || len(g.Items) != 5 {
			t.Errorf("after answering: answered = %d items = %d", g.Answered, len(g.Items))
		}

		// Once the learning step has elapsed the card is appended and served.
		f.clock.Advance(2 * time.Minute)
		n, err := f.svc.Next(f.ctx, f.user, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n.Item == nil || n.Item.Problem.ID != learning || n.Item.Position != 5 || n.Item.Kind != review.ItemReview || n.Remaining != 1 {
			t.Fatalf("after the step next = %+v, want the learning card at position 5", n)
		}
		// Calling again does not append it twice.
		if again, _ := f.svc.Next(f.ctx, f.user, v.ID); again.Remaining != 1 || again.Item == nil || again.Item.Position != 5 {
			t.Errorf("second next = %+v", again)
		}
		if g, _ := f.svc.Get(f.ctx, f.user, v.ID); len(g.Items) != 6 || g.Answered != 5 {
			t.Errorf("after append: items = %d answered = %d", len(g.Items), g.Answered)
		}

		// Answering it (right) completes the work; completing is idempotent.
		f.answerIn(v.ID, learning, k.right)
		if g, _ := f.svc.Get(f.ctx, f.user, v.ID); g.Answered != 6 {
			t.Errorf("answered = %d, want 6", g.Answered)
		}
		first, err := f.svc.Complete(f.ctx, f.user, v.ID)
		if err != nil || first.CompletedAt == nil {
			t.Fatalf("complete = %+v, %v", first, err)
		}
		f.clock.Advance(time.Hour)
		second, err := f.svc.Complete(f.ctx, f.user, v.ID)
		if err != nil || !second.CompletedAt.Equal(*first.CompletedAt) {
			t.Errorf("second complete = %+v, %v; want the first time kept", second, err)
		}
		var pb *platform.Problem
		if _, err := f.svc.Next(f.ctx, f.user, v.ID); !errors.As(err, &pb) || pb.Status != 409 {
			t.Errorf("next after complete = %v, want 409", err)
		}
		_ = fresh
	})

	t.Run("an item whose problem is retired is skipped", func(t *testing.T) {
		f := newSessionFixture(t, newStore)
		a, b := f.newProblem(k, problems.StatusLive), f.newProblem(k, problems.StatusLive)
		v, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionReview})
		if err != nil || len(v.Items) != 2 {
			t.Fatalf("create = %+v, %v", v, err)
		}
		f.archive(v.Items[0].ProblemID)
		n, err := f.svc.Next(f.ctx, f.user, v.ID)
		if err != nil || n.Item == nil || n.Item.Problem.ID != v.Items[1].ProblemID || n.Remaining != 1 {
			t.Fatalf("next = %+v, %v; want the second item with 1 remaining", n, err)
		}
		f.archive(v.Items[1].ProblemID)
		if n, err = f.svc.Next(f.ctx, f.user, v.ID); err != nil || n.Item != nil || n.Remaining != 0 {
			t.Errorf("next with everything retired = %+v, %v", n, err)
		}
		_, _ = a, b
	})

	t.Run("an empty review session is valid", func(t *testing.T) {
		f := newSessionFixture(t, newStore)
		v, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionReview})
		if err != nil || len(v.Items) != 0 || v.Kind != review.SessionReview || v.QuizID != nil {
			t.Fatalf("create = %+v, %v", v, err)
		}
		if n, err := f.svc.Next(f.ctx, f.user, v.ID); err != nil || n.Item != nil || n.Remaining != 0 {
			t.Errorf("next = %+v, %v", n, err)
		}
		if f.rec.n != 1 {
			t.Errorf("an empty session still counts as served; got %d", f.rec.n)
		}
	})

	t.Run("quiz sessions", func(t *testing.T) {
		f := newSessionFixture(t, newStore)
		live := []problems.View{f.newProblem(k, problems.StatusLive), f.newProblem(k, problems.StatusLive), f.newProblem(k, problems.StatusLive)}
		mkQuiz := func(status quizzes.Status, items ...domain.ID) domain.ID {
			q := quizzes.Quiz{ID: f.ids.New(), OwnerID: f.user, Title: "q", Status: status, CreatedBy: f.actor, CreatedAt: t0, UpdatedAt: t0}
			if err := f.s.Quizzes().Create(f.ctx, f.user, q); err != nil {
				t.Fatal(err)
			}
			if err := f.s.WithTx(f.ctx, func(tx store.Store) error {
				return tx.Quizzes().SetItems(f.ctx, f.user, q.ID, items, t0)
			}); err != nil {
				t.Fatal(err)
			}
			return q.ID
		}
		order := []domain.ID{live[2].ID, live[0].ID, live[1].ID}
		qid := mkQuiz(quizzes.StatusLive, order...)

		v, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionQuiz, QuizID: qid.String()})
		if err != nil {
			t.Fatal(err)
		}
		if v.QuizID == nil || *v.QuizID != qid || len(v.Items) != 3 {
			t.Fatalf("quiz session = %+v", v)
		}
		for i, it := range v.Items {
			if it.ProblemID != order[i] || it.Kind != review.ItemQuiz {
				t.Errorf("item %d = %+v, want %v as quiz", i, it, order[i])
			}
		}
		// Quiz answers are ordinary attempts: they update cards and are tied
		// to the session.
		n, _ := f.svc.Next(f.ctx, f.user, v.ID)
		res := f.answerIn(v.ID, n.Item.Problem.ID, k.right)
		if res.NextDue == nil {
			t.Error("quiz answer scheduled no card")
		}
		if _, ok := f.card(n.Item.Problem.ID); !ok {
			t.Error("quiz answer created no card")
		}
		if g, _ := f.svc.Get(f.ctx, f.user, v.ID); g.Answered != 1 || !g.Items[0].Answered {
			t.Errorf("after one answer: %+v", g)
		}

		// Shuffle is reproducible from the session ID and keeps every problem.
		sh, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionQuiz, QuizID: qid.String(), Shuffle: true})
		if err != nil || len(sh.Items) != 3 {
			t.Fatalf("shuffled = %+v, %v", sh, err)
		}
		plan, err := f.cards.QuizBuilder(f.s).Build(f.ctx, f.user, sh.ID, qid, true)
		if err != nil {
			t.Fatal(err)
		}
		for i, it := range plan.Items {
			if sh.Items[i].ProblemID != it.ProblemID {
				t.Errorf("shuffled item %d = %v, rebuild from the session ID gives %v", i, sh.Items[i].ProblemID, it.ProblemID)
			}
		}

		// A quiz session never appends learning cards: a wrong answer stays
		// answered.
		w, _ := f.svc.Next(f.ctx, f.user, sh.ID)
		f.answerIn(sh.ID, w.Item.Problem.ID, k.wrong)
		f.clock.Advance(2 * time.Minute)
		if g, _ := f.svc.Next(f.ctx, f.user, sh.ID); g.Remaining != 2 {
			t.Errorf("remaining = %d, want 2 (no learning append in a quiz)", g.Remaining)
		}
		if g, _ := f.svc.Get(f.ctx, f.user, sh.ID); len(g.Items) != 3 {
			t.Errorf("quiz list grew to %d", len(g.Items))
		}

		// Validation and ownership.
		var pb *platform.Problem
		for name, in := range map[string]review.CreateSessionInput{
			"draft quiz":       {Kind: review.SessionQuiz, QuizID: mkQuiz(quizzes.StatusDraft, order...).String()},
			"archived quiz":    {Kind: review.SessionQuiz, QuizID: mkQuiz(quizzes.StatusArchived, order...).String()},
			"quiz without id":  {Kind: review.SessionQuiz},
			"bad quiz id":      {Kind: review.SessionQuiz, QuizID: "nope"},
			"review with quiz": {Kind: review.SessionReview, QuizID: qid.String()},
			"review shuffled":  {Kind: review.SessionReview, Shuffle: true},
			"unknown kind":     {Kind: "other"},
		} {
			if _, err := f.svc.Create(f.ctx, f.user, in); !errors.As(err, &pb) || pb.Status != 422 {
				t.Errorf("%s: err = %v, want 422", name, err)
			}
		}
		if _, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionQuiz, QuizID: f.ids.New().String()}); !errors.As(err, &pb) || pb.Status != 404 {
			t.Errorf("unknown quiz: err = %v, want 404", err)
		}
		// Nothing but the two real sessions was counted.
		if f.rec.n != 2 {
			t.Errorf("sessions served = %d, want 2", f.rec.n)
		}

		// An empty quiz is an empty session.
		empty := mkQuiz(quizzes.StatusLive)
		if e, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionQuiz, QuizID: empty.String()}); err != nil || len(e.Items) != 0 {
			t.Errorf("empty quiz session = %+v, %v", e, err)
		}
	})

	t.Run("another user's session is not found", func(t *testing.T) {
		f := newSessionFixture(t, newStore)
		f.newProblem(k, problems.StatusLive)
		v, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionReview})
		if err != nil {
			t.Fatal(err)
		}
		other := auth.User{ID: f.ids.New(), DisplayName: "bob", Email: "bob@example.com", CreatedAt: t0}
		if err := f.s.Users().Create(f.ctx, other); err != nil {
			t.Fatal(err)
		}
		var pb *platform.Problem
		if _, err := f.svc.Get(f.ctx, other.ID, v.ID); !errors.As(err, &pb) || pb.Status != 404 {
			t.Errorf("Get = %v, want 404", err)
		}
		if _, err := f.svc.Next(f.ctx, other.ID, v.ID); !errors.As(err, &pb) || pb.Status != 404 {
			t.Errorf("Next = %v, want 404", err)
		}
		if _, err := f.svc.Complete(f.ctx, other.ID, v.ID); !errors.As(err, &pb) || pb.Status != 404 {
			t.Errorf("Complete = %v, want 404", err)
		}
		if g, _ := f.svc.Get(f.ctx, f.user, v.ID); g.CompletedAt != nil {
			t.Error("another user completed the session")
		}
	})

	t.Run("concurrent identical answers record one attempt", func(t *testing.T) {
		f := newSessionFixture(t, newStore)
		p := f.newProblem(k, problems.StatusLive)
		v, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionReview})
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Items) != 1 || v.Items[0].ProblemID != p.ID {
			t.Fatalf("items = %+v, want the one new problem", v.Items)
		}
		pv, err := f.probs.Get(f.ctx, f.user, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		const n = 8
		errs := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = f.atts.Answer(f.ctx, f.actor, attempts.AnswerInput{ProblemID: p.ID, ProblemVersion: pv.Version,
					Response: json.RawMessage(k.right), LatencyMS: 5, SessionID: v.ID.String()})
			}()
		}
		close(start)
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case !errors.Is(err, attempts.ErrDuplicate):
				t.Errorf("unexpected error: %v", err)
			}
		}
		if ok != 1 {
			t.Errorf("%d answers succeeded, want exactly 1", ok)
		}
		log, err := f.s.Attempts().List(f.ctx, f.user, attempts.ListFilter{SessionID: v.ID})
		if err != nil || len(log) != 1 {
			t.Fatalf("attempts in session = %d, %v; want 1", len(log), err)
		}
		// A later replay is a duplicate too.
		if _, err := f.atts.Answer(f.ctx, f.actor, attempts.AnswerInput{ProblemID: p.ID, ProblemVersion: pv.Version,
			Response: json.RawMessage(k.right), LatencyMS: 5, SessionID: v.ID.String()}); !errors.Is(err, attempts.ErrDuplicate) {
			t.Errorf("replay err = %v, want ErrDuplicate", err)
		}
	})

	t.Run("a learning-step repeat is not a duplicate", func(t *testing.T) {
		f := newSessionFixture(t, newStore)
		p := f.newProblem(k, problems.StatusLive)
		v, err := f.svc.Create(f.ctx, f.user, review.CreateSessionInput{Kind: review.SessionReview})
		if err != nil {
			t.Fatal(err)
		}
		// A wrong answer leaves the card in learning; the session lists it again.
		f.answerIn(v.ID, p.ID, k.wrong)
		f.clock.Advance(2 * time.Minute)
		n, err := f.svc.Next(f.ctx, f.user, v.ID)
		if err != nil || n.Item == nil || n.Item.Problem.ID != p.ID {
			t.Fatalf("Next = %+v, %v; want the learning card again", n, err)
		}
		f.answerIn(v.ID, p.ID, k.wrong) // the same response, a second presentation
		log, err := f.s.Attempts().List(f.ctx, f.user, attempts.ListFilter{SessionID: v.ID})
		if err != nil || len(log) != 2 {
			t.Fatalf("attempts = %d, %v; want 2", len(log), err)
		}
	})
}

// runCardQuery covers the filters behind GET /cards and the attempt filter
// behind a session's answered items.
func runCardQuery(t *testing.T, newStore NewStore) {
	f := newCardFixture(t, newStore)
	k := pkinds[0]
	var ps []problems.View
	for i := 0; i < 4; i++ {
		p := f.newProblem(k, problems.StatusLive)
		if _, err := f.answer(p.ID, k.right, nil); err != nil {
			t.Fatal(err)
		}
		ps = append(ps, p)
		f.clock.Advance(24 * time.Hour)
	}
	all, err := f.s.Cards().Query(f.ctx, f.user, review.CardFilter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("Query() = %d cards, %v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if string(all[i-1].ProblemID[:]) >= string(all[i].ProblemID[:]) {
			t.Errorf("cards not ordered by problem ID")
		}
	}
	byProblem := map[domain.ID]review.Card{}
	for _, c := range all {
		byProblem[c.ProblemID] = c
	}

	// due_before is strict: a card due exactly then is excluded.
	cut := byProblem[ps[1].ID].Due
	early, err := f.s.Cards().Query(f.ctx, f.user, review.CardFilter{DueBefore: cut})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range early {
		if !c.Due.Before(cut) {
			t.Errorf("card due %v is not before %v", c.Due, cut)
		}
	}
	later, _ := f.s.Cards().Query(f.ctx, f.user, review.CardFilter{DueBefore: cut.Add(time.Millisecond)})
	if len(later) != len(early)+countDue(all, cut) {
		t.Errorf("DueBefore not strict: %d vs %d", len(later), len(early))
	}

	// Keyset paging.
	var seen []domain.ID
	var after domain.ID
	for {
		page, err := f.s.Cards().Query(f.ctx, f.user, review.CardFilter{After: after, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page {
			seen = append(seen, c.ProblemID)
		}
		if len(page) < 3 {
			break
		}
		after = page[len(page)-1].ProblemID
	}
	if len(seen) != 4 {
		t.Errorf("paged %d cards, want 4", len(seen))
	}

	// Tag filter, and another owner sees nothing.
	mf := &masteryFixture{cardFixture: f, ids: domain.NewIDGenerator(f.clock)}
	tag := mf.newTag(f.user, "t")
	for _, p := range ps[:2] {
		if err := f.s.Tags().SetProblemTags(f.ctx, f.user, p.ID, []domain.ID{tag}); err != nil {
			t.Fatal(err)
		}
	}
	tagged, err := f.s.Cards().Query(f.ctx, f.user, review.CardFilter{TagID: tag})
	if err != nil || len(tagged) != 2 {
		t.Fatalf("tag filter = %d cards, %v; want 2", len(tagged), err)
	}
	other := newUser(domain.NewIDGenerator(f.clock), "carol")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.Cards().Query(f.ctx, other.ID, review.CardFilter{}); err != nil || len(got) != 0 {
		t.Errorf("other owner's Query = %d cards, %v", len(got), err)
	}

	// The attempt filter behind "answered": only the session's attempts.
	sid := domain.NewIDGenerator(f.clock).New()
	v, _ := f.probs.Get(f.ctx, f.user, ps[0].ID)
	if _, err := f.atts.Answer(f.ctx, f.actor, attempts.AnswerInput{ProblemID: ps[0].ID, ProblemVersion: v.Version,
		Response: json.RawMessage(k.right), LatencyMS: 1, SessionID: sid.String()}); err != nil {
		t.Fatal(err)
	}
	inSession, err := f.s.Attempts().List(f.ctx, f.user, attempts.ListFilter{SessionID: sid})
	if err != nil || len(inSession) != 1 || inSession[0].ProblemID != ps[0].ID {
		t.Errorf("attempts in session = %+v, %v", inSession, err)
	}
	if got, _ := f.s.Attempts().List(f.ctx, other.ID, attempts.ListFilter{SessionID: sid}); len(got) != 0 {
		t.Errorf("other owner sees the session's attempts: %+v", got)
	}
}

func countDue(cards []review.Card, at time.Time) int {
	n := 0
	for _, c := range cards {
		if c.Due.Equal(at) {
			n++
		}
	}
	return n
}
