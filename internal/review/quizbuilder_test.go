// SPDX-License-Identifier: Apache-2.0

package review_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/review"
)

type quizList map[domain.ID][]domain.ID

func (q quizList) QuizProblems(_ context.Context, _, quiz domain.ID) (review.QuizContents, error) {
	items, ok := q[quiz]
	if !ok {
		return review.QuizContents{}, review.ErrNotFound
	}
	return review.QuizContents{Live: true, Problems: items}, nil
}

func quizBuild(t *testing.T, w *world, q quizList, session, quiz domain.ID, shuffle bool) []review.Item {
	t.Helper()
	b := review.NewQuizBuilder(q, problemList{w})
	p, err := b.Build(context.Background(), w.user, session, quiz, shuffle)
	if err != nil {
		t.Fatal(err)
	}
	return p.Items
}

func TestQuizBuildAuthoredOrderServesOnlyLiveProblems(t *testing.T) {
	w := newWorld()
	a, b, c, d := w.problem(), w.problem(), w.problem(), w.problem()
	delete(w.live, b) // draft, archived or deleted: not live
	w.live[d] = 3
	quiz := w.ids.New()
	q := quizList{quiz: {d, b, a, c}}

	got := quizBuild(t, w, q, w.ids.New(), quiz, false)
	want := []review.Item{
		{ProblemID: d, Epoch: 3, Kind: review.ItemQuiz},
		{ProblemID: a, Epoch: 1, Kind: review.ItemQuiz},
		{ProblemID: c, Epoch: 1, Kind: review.ItemQuiz},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %+v, want %+v", got, want)
	}
	// The authored order does not depend on the session.
	if again := quizBuild(t, w, q, w.ids.New(), quiz, false); !reflect.DeepEqual(again, want) {
		t.Errorf("another session changed the order: %+v", again)
	}
}

func TestQuizBuildShuffleIsReproduciblePerSession(t *testing.T) {
	w := newWorld()
	var authored []domain.ID
	for range 20 {
		authored = append(authored, w.problem())
	}
	quiz := w.ids.New()
	q := quizList{quiz: authored}
	s1, s2 := w.ids.New(), w.ids.New()

	a := quizBuild(t, w, q, s1, quiz, true)
	if b := quizBuild(t, w, q, s1, quiz, true); !reflect.DeepEqual(a, b) {
		t.Fatalf("same session ID shuffled differently:\n%v\n%v", ids(a), ids(b))
	}
	if reflect.DeepEqual(ids(a), authored) {
		t.Error("shuffle left the authored order (20 items)")
	}
	if c := quizBuild(t, w, q, s2, quiz, true); reflect.DeepEqual(ids(a), ids(c)) {
		t.Error("different session IDs shuffled identically (20 items)")
	}
	// A shuffle is a permutation: nothing lost, nothing duplicated.
	seen := map[domain.ID]bool{}
	for _, it := range a {
		if seen[it.ProblemID] {
			t.Fatalf("duplicate %v", it.ProblemID)
		}
		seen[it.ProblemID] = true
	}
	if len(seen) != len(authored) {
		t.Errorf("shuffle returned %d of %d problems", len(seen), len(authored))
	}
	// Shuffling only the live subset is still a pure function of the session.
	delete(w.live, authored[0])
	if x, y := quizBuild(t, w, q, s1, quiz, true), quizBuild(t, w, q, s1, quiz, true); len(x) != 19 || !reflect.DeepEqual(x, y) {
		t.Errorf("live-only shuffle: %d items, reproducible=%v", len(x), reflect.DeepEqual(x, y))
	}
}

func TestQuizBuildMissingQuizAndEmpty(t *testing.T) {
	w := newWorld()
	b := review.NewQuizBuilder(quizList{}, problemList{w})
	if _, err := b.Build(context.Background(), w.user, w.ids.New(), w.ids.New(), false); !errors.Is(err, review.ErrNotFound) {
		t.Errorf("missing quiz = %v, want ErrNotFound", err)
	}
	quiz := w.ids.New()
	if got := quizBuild(t, w, quizList{quiz: nil}, w.ids.New(), quiz, true); len(got) != 0 {
		t.Errorf("empty quiz served %+v", got)
	}
}
