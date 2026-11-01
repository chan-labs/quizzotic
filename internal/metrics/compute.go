// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"sort"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/review"
)

// Compute derives one tag's mastery from its live problems' cards at now. It
// is pure: both the incremental update and the full recompute call it, which
// is what keeps them in agreement. cards holds one entry per live problem
// carrying the tag, in problem-ID order (the order fixes the floating-point
// summation); an entry with a nil Card is a problem never studied. last is the
// tag's latest attempt time, or the zero time.
func Compute(p fsrs.Params, now time.Time, userID, tagID domain.ID, cards []TagCard, last time.Time) TagMastery {
	m := TagMastery{UserID: userID, TagID: tagID, CardsTotal: len(cards), LastAttemptAt: last}
	var sum float64
	var stab []float64
	for _, tc := range cards {
		c := tc.Card
		if c == nil || !Studied(*c) {
			continue
		}
		m.CardsSeen++
		m.Reviews += c.Reps
		m.Lapses += c.Lapses
		sum += p.Retrievability(c.Card, now)
		stab = append(stab, c.Stability)
	}
	if m.CardsSeen > 0 {
		m.Recall = sum / float64(m.CardsSeen)
		m.StabilityMedian = median(stab)
	}
	return m
}

// Studied reports whether the card has been reviewed at least once. Unstudied
// cards count toward a tag's coverage denominator only.
func Studied(c review.Card) bool { return c.Reps > 0 && !c.LastReview.IsZero() }

func median(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
