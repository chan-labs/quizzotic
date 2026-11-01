// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// Defaults for the user-level metrics (spec: Mastery metrics).
const (
	// DefaultRetentionDays is the default true-retention window.
	DefaultRetentionDays = 30
	// DueLoadDays is how many days after today the due load covers.
	DueLoadDays = 7
	// DefaultActivityDays is the default number of study days of activity.
	DefaultActivityDays = 30
)

// AnswerEvent is one stored answer replayed against its card: what the card
// looked like just before the answer, and how the answer was rated.
type AnswerEvent struct {
	ProblemID  domain.ID
	Epoch      int
	AnsweredAt time.Time
	// TagIDs are the active tags on the problem now.
	TagIDs []domain.ID
	// First is true for the first answer of the card (problem and epoch): the
	// introduction of a new card.
	First bool
	// Scheduled is false for an open answer that has no rating yet; it did not
	// touch the card and has no Rating or StateBefore.
	Scheduled bool
	// Rating is the effective FSRS rating (1-4) of a scheduled answer.
	Rating int
	// StateBefore is the card's state when it was answered.
	StateBefore fsrs.State
}

// Replay folds the attempt log into one AnswerEvent per attempt. log must be
// ordered by problem, card epoch, then answered_at (ties by ID), so that each
// card's attempts are contiguous and in order; tags maps a problem to its
// active tags. It is pure and uses the same scheduler and rating rules as the
// card projection, so the states it reports are the ones the card went through.
func Replay(p fsrs.Params, log []attempts.Attempt, tags map[domain.ID][]domain.ID) []AnswerEvent {
	out := make([]AnswerEvent, 0, len(log))
	var card fsrs.Card
	for i, a := range log {
		first := i == 0 || log[i-1].ProblemID != a.ProblemID || log[i-1].CardEpoch != a.CardEpoch
		if first {
			card = fsrs.Card{}
		}
		e := AnswerEvent{ProblemID: a.ProblemID, Epoch: a.CardEpoch, AnsweredAt: a.AnsweredAt,
			TagIDs: tags[a.ProblemID], First: first}
		if r, ok := review.EffectiveRating(a); ok {
			e.Scheduled, e.Rating = true, r
			e.StateBefore = card.State
			if e.StateBefore == "" {
				e.StateBefore = fsrs.New
			}
			card = p.Next(card, fsrs.Rating(r), a.AnsweredAt)
		}
		out = append(out, e)
	}
	return out
}

// Retention counts reviews of cards that were in the review state and how many
// of them were remembered (rated Hard or better).
type Retention struct {
	Reviews, Retained int
}

// Rate is Retained / Reviews. ok is false when there were no reviews, in which
// case there is nothing to compare with desired retention.
func (r Retention) Rate() (rate float64, ok bool) {
	if r.Reviews == 0 {
		return 0, false
	}
	return float64(r.Retained) / float64(r.Reviews), true
}

// TrueRetention computes the share of reviews, answered within the window
// ending at now, of cards in the review state that were rated Hard or better,
// for the user as a whole and for each tag on the reviewed problems. The window
// is [now-window, now].
func TrueRetention(ev []AnswerEvent, now time.Time, window time.Duration) (all Retention, byTag map[domain.ID]Retention) {
	byTag = map[domain.ID]Retention{}
	from := now.Add(-window)
	for _, e := range ev {
		if !e.Scheduled || e.StateBefore != fsrs.Review || e.AnsweredAt.Before(from) || e.AnsweredAt.After(now) {
			continue
		}
		pass := 0
		if e.Rating >= int(fsrs.Hard) {
			pass = 1
		}
		all.Reviews++
		all.Retained += pass
		for _, id := range e.TagIDs {
			r := byTag[id]
			r.Reviews++
			r.Retained += pass
			byTag[id] = r
		}
	}
	return all, byTag
}

// DueDay is the number of cards due in one study day.
type DueDay struct {
	// Date names the study day (YYYY-MM-DD, the date it starts on).
	Date       string
	Start, End time.Time
	Cards      int
}

// DueLoad counts the cards due today and in each of the next DueLoadDays study
// days, in the user's day boundaries. Cards already overdue count as due today.
// due holds the due instants of the user's cards.
func DueLoad(due []time.Time, today settings.StudyDay, loc *time.Location, rolloverHour int) []DueDay {
	days := make([]DueDay, 0, DueLoadDays+1)
	d := today
	for i := 0; i <= DueLoadDays; i++ {
		days = append(days, DueDay{Date: d.Date, Start: d.Start, End: d.End})
		d = settings.StudyDayAt(d.End, loc, rolloverHour)
	}
	for _, t := range due {
		for i := range days {
			if t.Before(days[i].End) {
				days[i].Cards++
				break
			}
		}
	}
	return days
}

// DayActivity is what was done in one study day. Reviews counts answers to
// cards that already existed and NewCards counts cards whose first answer fell
// in the day, as the session builder counts them against the daily limits.
type DayActivity struct {
	Date              string
	Start, End        time.Time
	Reviews, NewCards int
}

// Activity returns the activity of the given number of study days ending with
// today, oldest first, with empty days present as zeros. ev must include the
// whole history of every card answered in those days, so that a card's first
// answer is recognised.
func Activity(ev []AnswerEvent, today settings.StudyDay, loc *time.Location, rolloverHour, days int) []DayActivity {
	if days < 1 {
		days = 1
	}
	out := make([]DayActivity, days)
	d := today
	for i := days - 1; i >= 0; i-- {
		out[i] = DayActivity{Date: d.Date, Start: d.Start, End: d.End}
		d = settings.StudyDayAt(d.Start.Add(-time.Nanosecond), loc, rolloverHour)
	}
	for _, e := range ev {
		for i := range out {
			if e.AnsweredAt.Before(out[i].End) {
				if !e.AnsweredAt.Before(out[i].Start) {
					if e.First {
						out[i].NewCards++
					} else {
						out[i].Reviews++
					}
				}
				break
			}
		}
	}
	return out
}
