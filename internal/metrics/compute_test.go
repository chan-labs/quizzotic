// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/review"
)

func TestCompute(t *testing.T) {
	p := fsrs.Default()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ids := domain.NewIDGenerator(domain.SystemClock{})
	user, tag := ids.New(), ids.New()

	card := func(stability float64, ago time.Duration, reps, lapses int) *review.Card {
		return &review.Card{Reps: reps, Lapses: lapses,
			Card: fsrs.Card{State: fsrs.Review, Stability: stability, Difficulty: 5, LastReview: now.Add(-ago)}}
	}
	a, b := card(10, 10*24*time.Hour, 4, 1), card(30, 5*24*time.Hour, 6, 0)
	last := now.Add(-time.Hour)

	got := Compute(p, now, user, tag, []TagCard{
		{Card: a}, {Card: nil}, {Card: b}, {Card: &review.Card{}}, // never studied: new card, and no card
	}, last)

	wantRecall := (p.Retrievability(a.Card, now) + p.Retrievability(b.Card, now)) / 2
	if math.Abs(got.Recall-wantRecall) > 1e-12 {
		t.Errorf("recall = %v, want mean of the studied cards %v", got.Recall, wantRecall)
	}
	if got.CardsTotal != 4 || got.CardsSeen != 2 || got.Coverage() != 0.5 {
		t.Errorf("total %d seen %d coverage %v, want 4, 2, 0.5", got.CardsTotal, got.CardsSeen, got.Coverage())
	}
	if got.StabilityMedian != 20 {
		t.Errorf("median stability = %v, want 20", got.StabilityMedian)
	}
	if got.Reviews != 10 || got.Lapses != 1 || got.LapseRate() != 0.1 {
		t.Errorf("reviews %d lapses %d rate %v, want 10, 1, 0.1", got.Reviews, got.Lapses, got.LapseRate())
	}
	if !got.LastAttemptAt.Equal(last) || got.UserID != user || got.TagID != tag {
		t.Errorf("identity/last = %+v", got)
	}

	// An odd count takes the middle stability.
	c := card(5, time.Hour, 1, 0)
	if m := Compute(p, now, user, tag, []TagCard{{Card: a}, {Card: b}, {Card: c}}, time.Time{}).StabilityMedian; m != 10 {
		t.Errorf("odd median = %v, want 10", m)
	}

	// No studied cards: recall and stability are zero, coverage is zero.
	none := Compute(p, now, user, tag, []TagCard{{Card: nil}}, time.Time{})
	if none.Recall != 0 || none.StabilityMedian != 0 || none.CardsSeen != 0 || none.CardsTotal != 1 ||
		none.Coverage() != 0 || none.LapseRate() != 0 {
		t.Errorf("unstudied tag = %+v", none)
	}
	if empty := Compute(p, now, user, tag, nil, time.Time{}); empty.Coverage() != 0 || empty.CardsTotal != 0 {
		t.Errorf("empty tag = %+v", empty)
	}
}
