// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// TodayView is what the Today page shows: the user's workload for the current
// study day, measured the same way the session builder measures it.
type TodayView struct {
	// Due is how many cards are due before now: the count GET /cards with
	// due_before=now would list.
	Due int
	// ReviewsDone and NewDone are the answers already given in the study day.
	ReviewsDone, NewDone int
	// MaxReviews and NewPerDay are the day's limits.
	MaxReviews, NewPerDay int
	// ReviewsLeft and NewLeft are what a session started now may still serve:
	// the limit minus what was done, never below zero.
	ReviewsLeft, NewLeft int
	Day                  settings.StudyDay
}

// Today summarises the user's study day at now. It only reads.
func (b *Builder) Today(ctx context.Context, userID domain.ID, now time.Time) (TodayView, error) {
	cfg, err := b.src.Settings.Get(ctx, userID)
	if err != nil {
		return TodayView{}, err
	}
	day, err := cfg.StudyDay(now)
	if err != nil {
		return TodayView{}, err
	}
	used, err := b.usage(ctx, userID, day)
	if err != nil {
		return TodayView{}, err
	}
	due, err := b.src.Cards.Query(ctx, userID, CardFilter{DueBefore: now})
	if err != nil {
		return TodayView{}, err
	}
	return TodayView{
		Due:         len(due),
		ReviewsDone: used.Reviews, NewDone: used.NewCards,
		MaxReviews: cfg.MaxReviewsPerDay, NewPerDay: cfg.NewPerDay,
		ReviewsLeft: max(0, cfg.MaxReviewsPerDay-used.Reviews),
		NewLeft:     max(0, cfg.NewPerDay-used.NewCards),
		Day:         day,
	}, nil
}
