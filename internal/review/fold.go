// SPDX-License-Identifier: Apache-2.0

package review

import (
	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/grading"
)

// EffectiveRating is the FSRS rating that applies to a stored attempt, and
// whether the attempt is scheduled at all. attempt.Rating is what the client
// sent, so it is resolved against the outcome again rather than trusted:
//
//   - an auto-graded attempt (AutoCorrect set) that was wrong is Again whatever
//     was sent; a right one uses the stored rating when it is Hard, Good or
//     Easy and the default rating otherwise;
//   - an open attempt (AutoCorrect nil) is scheduled only once it has a valid
//     rating of 1-4; until then it is not part of the fold.
func EffectiveRating(a attempts.Attempt) (rating int, scheduled bool) {
	if a.AutoCorrect == nil {
		if a.Rating == nil {
			return 0, false
		}
		r, err := grading.ResolveRating("open", false, a.Rating)
		return r, err == nil
	}
	r, err := grading.ResolveRating("mcq", *a.AutoCorrect, a.Rating)
	if err != nil {
		r, _ = grading.ResolveRating("mcq", *a.AutoCorrect, nil)
	}
	return r, true
}

// Fold folds attempts, which must all belong to one (user, problem, epoch) and
// be in answered_at order, into the card they produce under p. ok is false
// when no attempt is scheduled: there is no card yet. The caller fills in the
// card's user, problem and epoch.
func Fold(p fsrs.Params, as []attempts.Attempt) (c Card, ok bool) {
	for _, a := range as {
		r, scheduled := EffectiveRating(a)
		if !scheduled {
			continue
		}
		if c.Card.State == fsrs.Review && r == grading.RatingAgain {
			c.Lapses++
		}
		c.Card = p.Next(c.Card, fsrs.Rating(r), a.AnsweredAt)
		c.Reps++
	}
	return c, c.Reps > 0
}
