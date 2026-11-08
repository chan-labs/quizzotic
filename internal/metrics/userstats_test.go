// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// The fixtures below are hand-computed. The user lives in New York with the
// day rolling over at 04:00, and "now" is 2026-06-10 10:00 local, which is in
// the study day 2026-06-10 = [06-10 04:00, 06-11 04:00).
var (
	ny       = mustNY()
	rollover = 4
	now      = time.Date(2026, 6, 10, 10, 0, 0, 0, ny)
)

func mustNY() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}

func local(d, h, minute int) time.Time { return time.Date(2026, time.June, d, h, minute, 0, 0, ny) }

func TestReplayStates(t *testing.T) {
	ids := domain.NewIDGenerator(domain.SystemClock{})
	p1, p2 := ids.New(), ids.New()
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	yes, no := true, false
	rate := func(r int) *int { return &r }
	att := func(p domain.ID, epoch int, at time.Time, correct *bool, rating *int) attempts.Attempt {
		return attempts.Attempt{ProblemID: p, CardEpoch: epoch, AnsweredAt: at, AutoCorrect: correct, Rating: rating}
	}
	log := []attempts.Attempt{
		att(p1, 1, t0, &yes, rate(4)),                              // new card, Easy: graduates straight to review
		att(p1, 1, t0.Add(3*24*time.Hour), &yes, nil),              // review, default rating Good
		att(p1, 1, t0.Add(13*24*time.Hour), &no, rate(4)),          // review, wrong: Again whatever was sent
		att(p1, 1, t0.Add(13*24*time.Hour+time.Minute), &yes, nil), // after a lapse the card is relearning
		att(p1, 2, t0.Add(20*24*time.Hour), nil, nil),              // new epoch, open and unrated: not scheduled
		att(p1, 2, t0.Add(21*24*time.Hour), nil, rate(2)),          // open, rated Hard: first scheduled answer is a new card
		att(p2, 1, t0, &yes, nil),
	}
	tags := map[domain.ID][]domain.ID{p1: {ids.New()}}
	ev := Replay(fsrs.Default(), log, tags)

	type want struct {
		first, scheduled bool
		rating           int
		state            fsrs.State
	}
	wants := []want{
		{true, true, 4, fsrs.New},
		{false, true, 3, fsrs.Review},
		{false, true, 1, fsrs.Review},
		{false, true, 3, fsrs.Relearning},
		{true, false, 0, ""},
		{false, true, 2, fsrs.New},
		{true, true, 3, fsrs.New},
	}
	if len(ev) != len(wants) {
		t.Fatalf("%d events, want %d", len(ev), len(wants))
	}
	for i, w := range wants {
		g := ev[i]
		if g.First != w.first || g.Scheduled != w.scheduled || g.Rating != w.rating || g.StateBefore != w.state {
			t.Errorf("event %d = first %v scheduled %v rating %d state %q, want %+v", i, g.First, g.Scheduled, g.Rating, g.StateBefore, w)
		}
	}
	if !reflect.DeepEqual(ev[0].TagIDs, tags[p1]) || len(ev[6].TagIDs) != 0 {
		t.Errorf("tags not attached by problem: %v / %v", ev[0].TagIDs, ev[6].TagIDs)
	}
}

func TestTrueRetention(t *testing.T) {
	tagA, tagB := domain.ID{1}, domain.ID{2}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	day := 24 * time.Hour
	rev := func(at time.Time, rating int, tags ...domain.ID) AnswerEvent {
		return AnswerEvent{AnsweredAt: at, TagIDs: tags, Scheduled: true, Rating: rating, StateBefore: fsrs.Review}
	}
	learning := rev(ago(day), 3, tagA)
	learning.StateBefore = fsrs.Learning
	ev := []AnswerEvent{
		rev(ago(day), 3, tagA, tagB),                      // kept: Good
		rev(ago(5*day), 1, tagA),                          // kept: Again, forgotten
		rev(ago(29*day), 2, tagB),                         // kept: Hard counts as remembered
		rev(ago(30*day), 3, tagA),                         // kept: exactly at the window start
		rev(ago(31*day), 4, tagA),                         // before the window
		rev(now.Add(time.Hour), 3, tagA),                  // after now
		learning,                                          // not in the review state
		{AnsweredAt: ago(day), TagIDs: []domain.ID{tagA}}, // unscheduled
	}
	all, byTag := TrueRetention(ev, now, 30*day)

	// Four counted reviews, three remembered: 3/4.
	if all != (Retention{Reviews: 4, Retained: 3}) {
		t.Errorf("all = %+v, want 3 of 4", all)
	}
	if rate, ok := all.Rate(); !ok || rate != 0.75 {
		t.Errorf("rate = %v, %v; want 0.75", rate, ok)
	}
	// Tag A: Good, Again, Good = 2/3. Tag B: Good, Hard = 2/2.
	if byTag[tagA] != (Retention{Reviews: 3, Retained: 2}) || byTag[tagB] != (Retention{Reviews: 2, Retained: 2}) {
		t.Errorf("by tag = %+v", byTag)
	}
	// A shorter window drops what is older: 7 days keeps the 1 and 5 day old ones, 1/2.
	if short, _ := TrueRetention(ev, now, 7*day); short != (Retention{Reviews: 2, Retained: 1}) {
		t.Errorf("7 day window = %+v, want 1 of 2", short)
	}
	// No reviews: no rate rather than a rate of zero.
	if none, _ := TrueRetention(nil, now, 30*day); none != (Retention{}) {
		t.Errorf("none = %+v", none)
	} else if _, ok := none.Rate(); ok {
		t.Error("rate of no reviews reported as defined")
	}
}

func TestDueLoad(t *testing.T) {
	today := settings.StudyDayAt(now, ny, rollover)
	due := []time.Time{
		local(1, 9, 0),   // long overdue: due today
		local(10, 3, 0),  // due before today's rollover: overdue, due today
		local(11, 3, 59), // last minute of today
		local(11, 4, 0),  // first minute of tomorrow
		local(12, 10, 0), // in two days
		local(18, 3, 59), // last minute of day 7
		local(18, 4, 0),  // day 8: beyond the horizon
	}
	got := DueLoad(due, today, ny, rollover)

	wantDates := []string{"2026-06-10", "2026-06-11", "2026-06-12", "2026-06-13", "2026-06-14", "2026-06-15", "2026-06-16", "2026-06-17"}
	wantCards := []int{3, 1, 1, 0, 0, 0, 0, 1}
	if len(got) != DueLoadDays+1 {
		t.Fatalf("%d days, want %d", len(got), DueLoadDays+1)
	}
	for i := range got {
		if got[i].Date != wantDates[i] || got[i].Cards != wantCards[i] {
			t.Errorf("day %d = %s: %d cards, want %s: %d", i, got[i].Date, got[i].Cards, wantDates[i], wantCards[i])
		}
	}
	if !got[7].End.Equal(local(18, 4, 0)) {
		t.Errorf("horizon ends %v, want 2026-06-18 04:00 local", got[7].End)
	}
	if empty := DueLoad(nil, today, ny, rollover); empty[0].Cards != 0 || len(empty) != 8 {
		t.Errorf("empty load = %+v", empty)
	}
}

func TestActivity(t *testing.T) {
	today := settings.StudyDayAt(now, ny, rollover)
	answer := func(at time.Time, first, scheduled bool) AnswerEvent {
		return AnswerEvent{AnsweredAt: at, First: first, Scheduled: scheduled}
	}
	ev := []AnswerEvent{
		answer(local(7, 12, 0), false, true),  // before the range
		answer(local(9, 3, 59), true, true),   // study day 06-08: new card
		answer(local(8, 10, 0), false, false), // study day 06-08: unrated answer still counts as a review
		answer(local(9, 4, 0), false, true),   // study day 06-09 (rollover instant)
		answer(local(9, 20, 0), true, true),   // 06-09: new card
		answer(local(10, 3, 59), false, true), // 06-09
		answer(local(10, 4, 0), false, true),  // 06-10
		answer(local(10, 9, 0), true, true),   // 06-10: new card
		answer(local(10, 9, 30), false, true), // 06-10
		answer(local(11, 4, 0), false, true),  // tomorrow: after the range
	}
	got := Activity(ev, today, ny, rollover, 3)

	want := []struct {
		date             string
		reviews, newCard int
	}{{"2026-06-08", 1, 1}, {"2026-06-09", 2, 1}, {"2026-06-10", 2, 1}}
	if len(got) != len(want) {
		t.Fatalf("%d days, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Date != w.date || got[i].Reviews != w.reviews || got[i].NewCards != w.newCard {
			t.Errorf("day %d = %s: %d reviews %d new, want %s: %d reviews %d new",
				i, got[i].Date, got[i].Reviews, got[i].NewCards, w.date, w.reviews, w.newCard)
		}
	}

	// Days with no answers are present as zeros, oldest first.
	quiet := Activity(nil, today, ny, rollover, 2)
	if len(quiet) != 2 || quiet[0].Date != "2026-06-09" || quiet[1].Date != "2026-06-10" || quiet[0].Reviews+quiet[1].NewCards != 0 {
		t.Errorf("quiet = %+v", quiet)
	}
}
