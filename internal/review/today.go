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
	// Due is how many cards the session builder would consider due at now
	// (due <= now, live problems only), before the daily review cap. Note GET
	// /cards?due_before=now is strictly before, so it excludes a card due
	// exactly at now.
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
	live, err := b.liveEpochs(ctx, userID)
	if err != nil {
		return TodayView{}, err
	}
	cards, err := b.src.Cards.List(ctx, userID)
	if err != nil {
		return TodayView{}, err
	}
	due := 0
	for _, c := range cards {
		// Same selection as Build: live problem at the card's epoch, due <= now.
		if e, ok := live[c.ProblemID]; ok && e == c.Epoch && isDue(c, now) {
			due++
		}
	}
	return TodayView{
		Due:         due,
		ReviewsDone: used.Reviews, NewDone: used.NewCards,
		MaxReviews: cfg.MaxReviewsPerDay, NewPerDay: cfg.NewPerDay,
		ReviewsLeft: max(0, cfg.MaxReviewsPerDay-used.Reviews),
		NewLeft:     max(0, cfg.NewPerDay-used.NewCards),
		Day:         day,
	}, nil
}
