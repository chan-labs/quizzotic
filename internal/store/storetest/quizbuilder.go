// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
)

// runQuizBuilder runs the quiz session builder over the real stores, and
// answers its items through the normal attempt path to prove cards update.
func runQuizBuilder(t *testing.T, newStore NewStore) {
	f := newCardFixture(t, newStore)
	ids := domain.NewIDGenerator(f.clock)
	k := pkinds[0]

	live := []problems.View{f.newProblem(k, problems.StatusLive), f.newProblem(k, problems.StatusLive),
		f.newProblem(k, problems.StatusLive), f.newProblem(k, problems.StatusLive)}
	draft := f.newProblem(k, problems.StatusDraft)
	gone := f.newProblem(k, problems.StatusLive)
	f.archive(gone.ID)

	// Authored order deliberately differs from creation order.
	authored := []domain.ID{live[2].ID, draft.ID, live[0].ID, gone.ID, live[3].ID, live[1].ID}
	q := quizzes.Quiz{ID: ids.New(), OwnerID: f.user, Title: "t", Status: quizzes.StatusLive,
		CreatedBy: f.actor, CreatedAt: t0, UpdatedAt: t0}
	if err := f.s.Quizzes().Create(f.ctx, f.user, q); err != nil {
		t.Fatal(err)
	}
	if err := f.s.WithTx(f.ctx, func(tx store.Store) error {
		return tx.Quizzes().SetItems(f.ctx, f.user, q.ID, authored, t0)
	}); err != nil {
		t.Fatal(err)
	}

	b := f.cards.QuizBuilder(f.s)
	session := ids.New()
	plan, err := b.Build(f.ctx, f.user, session, q.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var order []domain.ID
	for _, it := range plan.Items {
		if it.Kind != review.ItemQuiz || it.Epoch != 1 {
			t.Errorf("item %+v", it)
		}
		order = append(order, it.ProblemID)
	}
	if want := []domain.ID{live[2].ID, live[0].ID, live[3].ID, live[1].ID}; !reflect.DeepEqual(order, want) {
		t.Fatalf("authored order = %v, want live problems only: %v", order, want)
	}

	// Shuffle: a permutation of the same live problems, reproducible per session.
	s1, err := b.Build(f.ctx, f.user, session, q.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := b.Build(f.ctx, f.user, session, q.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s1, s2) || len(s1.Items) != 4 {
		t.Fatalf("shuffle %+v vs %+v, want the same 4 items", s1.Items, s2.Items)
	}

	// Answering the served items through the attempt path creates their cards.
	if len(f.list()) != 0 {
		t.Fatal("building a session must not create cards")
	}
	for _, it := range plan.Items {
		res, err := f.answer(it.ProblemID, k.right, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.NextDue == nil {
			t.Errorf("answer to %v scheduled no card", it.ProblemID)
		}
		c, ok := f.card(it.ProblemID)
		if !ok || c.Reps != 1 || c.Epoch != it.Epoch {
			t.Errorf("card after quiz answer = %+v, %v", c, ok)
		}
	}
	// A second pass of the quiz updates the existing cards (early review).
	f.clock.Advance(time.Hour)
	first := plan.Items[0].ProblemID
	if _, err := f.answer(first, k.right, nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.card(first); c.Reps != 2 {
		t.Errorf("repeat quiz answer reps = %d, want 2", c.Reps)
	}

	// Owner scoping: another user cannot build a session from this quiz.
	other := newUser(ids, "bob")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(f.ctx, other.ID, session, q.ID, false); !isReviewNotFound(err) {
		t.Errorf("other user's build = %v, want review.ErrNotFound", err)
	}

	// A deleted quiz is gone.
	if err := f.s.Quizzes().SoftDelete(f.ctx, f.user, q.ID, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(f.ctx, f.user, session, q.ID, false); !isReviewNotFound(err) {
		t.Errorf("deleted quiz build = %v, want review.ErrNotFound", err)
	}
}

func isReviewNotFound(err error) bool { return errors.Is(err, review.ErrNotFound) }
