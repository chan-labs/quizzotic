// SPDX-License-Identifier: Apache-2.0

package review_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/review"
)

func ptr[T any](v T) *T { return &v }

var base = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func auto(correct bool, rating *int, at time.Time) attempts.Attempt {
	return attempts.Attempt{AutoCorrect: &correct, Rating: rating, AnsweredAt: at}
}

func open(rating *int, at time.Time) attempts.Attempt {
	return attempts.Attempt{Rating: rating, AnsweredAt: at}
}

func TestEffectiveRating(t *testing.T) {
	cases := []struct {
		name      string
		a         attempts.Attempt
		want      int
		scheduled bool
	}{
		{"right, none sent: default Good", auto(true, nil, base), 3, true},
		{"right, Easy sent", auto(true, ptr(4), base), 4, true},
		{"right, Hard sent", auto(true, ptr(2), base), 2, true},
		{"wrong is Again whatever was sent", auto(false, ptr(4), base), 1, true},
		{"wrong, none sent", auto(false, nil, base), 1, true},
		{"right with a stored Again falls back to the default", auto(true, ptr(1), base), 3, true},
		{"right with an out of range rating falls back to the default", auto(true, ptr(9), base), 3, true},
		{"open unrated is not scheduled", open(nil, base), 0, false},
		{"open rated", open(ptr(1), base), 1, true},
		{"open with an invalid rating is not scheduled", open(ptr(0), base), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := review.EffectiveRating(c.a)
			if got != c.want || ok != c.scheduled {
				t.Fatalf("EffectiveRating = %d, %v; want %d, %v", got, ok, c.want, c.scheduled)
			}
		})
	}
}

func TestFoldIsTheFsrsChain(t *testing.T) {
	p := fsrs.Default()
	day := 24 * time.Hour
	as := []attempts.Attempt{
		auto(true, nil, base),                   // Good: learning
		auto(true, ptr(4), base.Add(time.Hour)), // Easy: graduates
		auto(false, ptr(4), base.Add(3*day)),    // wrong: Again (a lapse)
		auto(true, ptr(2), base.Add(4*day)),     // Hard
		open(nil, base.Add(5*day)),              // unrated open: ignored
		auto(true, ptr(3), base.Add(6*day)),     // Good
	}
	got, ok := review.Fold(p, as)
	if !ok {
		t.Fatal("want a card")
	}

	var want fsrs.Card
	for _, step := range []struct {
		r  fsrs.Rating
		at time.Time
	}{{3, as[0].AnsweredAt}, {4, as[1].AnsweredAt}, {1, as[2].AnsweredAt}, {2, as[3].AnsweredAt}, {3, as[5].AnsweredAt}} {
		want = p.Next(want, step.r, step.at)
	}
	if !reflect.DeepEqual(got.Card, want) {
		t.Errorf("card\n got  %+v\n want %+v", got.Card, want)
	}
	if got.Reps != 5 {
		t.Errorf("reps = %d, want 5 (the unrated open attempt does not count)", got.Reps)
	}
	if got.Lapses != 1 {
		t.Errorf("lapses = %d, want 1", got.Lapses)
	}
}

func TestFoldWithoutScheduledAttemptsHasNoCard(t *testing.T) {
	if _, ok := review.Fold(fsrs.Default(), nil); ok {
		t.Error("no attempts must give no card")
	}
	if _, ok := review.Fold(fsrs.Default(), []attempts.Attempt{open(nil, base)}); ok {
		t.Error("an unrated open attempt must give no card")
	}
}

func TestFoldUsesDesiredRetention(t *testing.T) {
	lo, hi := fsrs.Default(), fsrs.Default()
	lo.DesiredRetention, hi.DesiredRetention = 0.80, 0.95
	as := []attempts.Attempt{
		auto(true, nil, base), auto(true, nil, base.Add(time.Hour)), auto(true, nil, base.Add(24*time.Hour)),
		auto(true, nil, base.Add(72*time.Hour)),
	}
	a, _ := review.Fold(lo, as)
	b, _ := review.Fold(hi, as)
	if !a.Due.After(b.Due) {
		t.Errorf("lower retention should schedule later: %v vs %v", a.Due, b.Due)
	}
}
