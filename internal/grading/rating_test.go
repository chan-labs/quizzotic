// SPDX-License-Identifier: Apache-2.0

package grading

import (
	"errors"
	"testing"
)

func ratingPtr(i int) *int { return &i }

func TestOpenScore(t *testing.T) {
	want := map[int]float64{1: 0, 2: 0.33, 3: 0.67, 4: 1}
	for r, w := range want {
		got, err := OpenScore(r)
		if err != nil || got != w {
			t.Errorf("OpenScore(%d) = %v, %v; want %v", r, got, err, w)
		}
	}
	for _, r := range []int{0, 5, -1} {
		if _, err := OpenScore(r); !errors.Is(err, ErrInvalidRating) {
			t.Errorf("OpenScore(%d) err = %v", r, err)
		}
	}
}

func TestResolveRatingAutoGraded(t *testing.T) {
	for _, typ := range []string{"mcq", "cloze"} {
		t.Run(typ, func(t *testing.T) {
			// Wrong forces Again, even over a chosen rating.
			for _, c := range []*int{nil, ratingPtr(4), ratingPtr(3)} {
				if got, err := ResolveRating(typ, false, c); err != nil || got != RatingAgain {
					t.Errorf("wrong: got %d, %v", got, err)
				}
			}
			// Right defaults to Good.
			if got, err := ResolveRating(typ, true, nil); err != nil || got != RatingGood {
				t.Errorf("right default: got %d, %v", got, err)
			}
			// Right honours Hard, Good, Easy.
			for _, r := range []int{2, 3, 4} {
				if got, err := ResolveRating(typ, true, ratingPtr(r)); err != nil || got != r {
					t.Errorf("right chosen %d: got %d, %v", r, got, err)
				}
			}
			// Right cannot be rated Again or out of range.
			for _, r := range []int{0, 1, 5} {
				if _, err := ResolveRating(typ, true, ratingPtr(r)); !errors.Is(err, ErrInvalidRating) {
					t.Errorf("right chosen %d: err = %v", r, err)
				}
			}
		})
	}
}

func TestResolveRatingOpen(t *testing.T) {
	// correct is ignored: no auto-grade.
	for _, correct := range []bool{true, false} {
		if _, err := ResolveRating("open", correct, nil); !errors.Is(err, ErrRatingRequired) {
			t.Errorf("missing rating err = %v", err)
		}
		for r := 1; r <= 4; r++ {
			if got, err := ResolveRating("open", correct, ratingPtr(r)); err != nil || got != r {
				t.Errorf("open %d: got %d, %v", r, got, err)
			}
		}
		for _, r := range []int{0, 5} {
			if _, err := ResolveRating("open", correct, ratingPtr(r)); !errors.Is(err, ErrInvalidRating) {
				t.Errorf("open %d err = %v", r, err)
			}
		}
	}
}

func TestResolveRatingUnknownType(t *testing.T) {
	if _, err := ResolveRating("essay", true, nil); !errors.Is(err, ErrUnknownProblemType) {
		t.Errorf("err = %v", err)
	}
}
