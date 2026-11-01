// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// TagMastery is the current mastery of one user's tag. It is a projection of
// the card projection (and so of the attempt log): never a source of truth, so
// it can be dropped and recomputed at any time.
//
// Recall is a prediction as of the moment the row was last computed; it decays
// as time passes, so readers that need "right now" recompute.
type TagMastery struct {
	UserID domain.ID
	TagID  domain.ID
	// Recall is the mean FSRS retrievability of the studied cards under the tag.
	// It is 0 when CardsSeen is 0; callers must treat that as "no data" (the
	// scheduler uses 0.5), not as a recall of zero.
	Recall float64
	// StabilityMedian is the median card stability, in days, over the studied
	// cards; 0 when there are none.
	StabilityMedian float64
	// CardsTotal is the number of live problems carrying the tag. CardsSeen is
	// how many of them have been studied (have a reviewed card).
	CardsTotal, CardsSeen int
	// Reviews is the total review count and Lapses the total lapse count over
	// the studied cards.
	Reviews, Lapses int
	// LastAttemptAt is the time of the latest attempt on any problem carrying
	// the tag; zero when there has been none.
	LastAttemptAt time.Time
}

// Coverage is the share of the tag's live problems that have been studied:
// CardsSeen / CardsTotal, or 0 for a tag with no live problems.
func (m TagMastery) Coverage() float64 {
	if m.CardsTotal == 0 {
		return 0
	}
	return float64(m.CardsSeen) / float64(m.CardsTotal)
}

// LapseRate is total lapses over total reviews, or 0 before any review.
func (m TagMastery) LapseRate() float64 {
	if m.Reviews == 0 {
		return 0
	}
	return float64(m.Lapses) / float64(m.Reviews)
}
