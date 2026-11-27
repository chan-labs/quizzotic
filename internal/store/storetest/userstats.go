// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// runUserStats covers the inputs of the user-level metrics (attempt log,
// problem tags, due times) and the figures the service builds from them.
func runUserStats(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	other := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	otherActor := domain.Actor{Kind: domain.ActorHuman, UserID: other.ID}
	repo := f.s.Mastery()
	at := func(d time.Duration) { f.clock.t = t0.Add(d) }
	const hour, day = time.Hour, 24 * time.Hour

	tagX, tagY := f.newTag(f.user, "x"), f.newTag(f.user, "y")
	p1 := f.newProblem(pkinds[0], problems.StatusLive)
	p2 := f.newProblem(pkinds[1], problems.StatusLive)
	gone := f.newProblem(pkinds[0], problems.StatusLive)
	f.tag(f.user, p1.ID, tagX)
	f.tag(f.user, p2.ID, tagX, tagY)
	f.tag(f.user, gone.ID, tagX)
	theirs, err := f.probs.Create(f.ctx, otherActor, problems.CreateInput{Type: pkinds[0].typ, Prompt: pkinds[0].prompt,
		Payload: []byte(pkinds[0].payload), Status: problems.StatusLive})
	if err != nil {
		t.Fatal(err)
	}

	answer := func(p problems.View, response string) {
		t.Helper()
		if _, err := f.answer(p.ID, response, nil); err != nil {
			t.Fatal(err)
		}
	}
	// p1 is answered right three times; p2 right twice, then wrong once.
	at(1 * hour)
	answer(p1, pkinds[0].right)
	at(3 * hour)
	answer(p2, pkinds[1].right)
	at(4 * hour)
	answer(gone, pkinds[0].right)
	f.remove(gone.ID)
	at(5 * hour)
	if _, err := f.atts.Answer(f.ctx, otherActor, attempts.AnswerInput{ProblemID: theirs.ID, ProblemVersion: theirs.Version,
		Response: []byte(pkinds[0].right), LatencyMS: 1}); err != nil {
		t.Fatal(err)
	}
	at(2*day + 1*hour)
	answer(p1, pkinds[0].right)
	at(2*day + 3*hour)
	answer(p2, pkinds[1].right)
	at(5*day + 1*hour)
	answer(p1, pkinds[0].right)
	at(6*day + 3*hour)
	answer(p2, pkinds[1].wrong)

	t.Run("attempt log", func(t *testing.T) {
		got, err := repo.AttemptLog(f.ctx, f.user, t0)
		if err != nil {
			t.Fatal(err)
		}
		// Everything of the owner's, bar the deleted problem and the other
		// owner's: p1 three times then p2 three times, by problem then time.
		if len(got) != 6 {
			t.Fatalf("AttemptLog = %d attempts, want 6", len(got))
		}
		for i, a := range got {
			want := p1.ID
			if i >= 3 {
				want = p2.ID
			}
			if a.ProblemID != want || a.UserID != f.user {
				t.Errorf("attempt %d is for %v, want %v", i, a.ProblemID, want)
			}
			if i%3 > 0 && !a.AnsweredAt.After(got[i-1].AnsweredAt) {
				t.Errorf("attempt %d is not after the one before", i)
			}
		}

		// Whole cards come back when any of their attempts is recent enough:
		// from day 6 only p2's last attempt is, which pulls in p2's history.
		got, err = repo.AttemptLog(f.ctx, f.user, t0.Add(6*day))
		if err != nil || len(got) != 3 || got[0].ProblemID != p2.ID {
			t.Errorf("AttemptLog since day 6 = %d attempts (%v), want p2's 3", len(got), err)
		}
		if got, err := repo.AttemptLog(f.ctx, f.user, t0.Add(7*day)); err != nil || len(got) != 0 {
			t.Errorf("AttemptLog since day 7 = %d attempts, %v; want none", len(got), err)
		}
		theirsLog, err := repo.AttemptLog(f.ctx, other.ID, t0)
		if err != nil || len(theirsLog) != 1 || theirsLog[0].UserID != other.ID {
			t.Errorf("another owner's AttemptLog = %+v, %v", theirsLog, err)
		}
	})

	t.Run("problem tags", func(t *testing.T) {
		got, err := repo.ProblemTags(f.ctx, f.user)
		if err != nil {
			t.Fatal(err)
		}
		wantY := []domain.ID{tagX, tagY}
		if compareIDs(tagY, tagX) < 0 {
			wantY = []domain.ID{tagY, tagX}
		}
		if !reflect.DeepEqual(got[p1.ID], []domain.ID{tagX}) || !reflect.DeepEqual(got[p2.ID], wantY) {
			t.Errorf("ProblemTags = %v; want p1 -> x, p2 -> %v", got, wantY)
		}
		if l, err := repo.ProblemTags(f.ctx, other.ID); err != nil || len(l) != 0 {
			t.Errorf("another owner's ProblemTags = %v, %v; want none", l, err)
		}
	})

	var dues []time.Time
	t.Run("due times", func(t *testing.T) {
		c1, _ := f.card(p1.ID)
		c2, _ := f.card(p2.ID)
		first, second := c1.Due, c2.Due
		if second.Before(first) {
			first, second = second, first
		}
		far := t0.Add(1000 * day)
		got, err := repo.DueTimes(f.ctx, f.user, far)
		if err != nil || !reflect.DeepEqual(got, []time.Time{first, second}) {
			t.Fatalf("DueTimes = %v, %v; want %v (the live problems' cards, ascending)", got, err, []time.Time{first, second})
		}
		dues = got
		if got, err := repo.DueTimes(f.ctx, f.user, second); err != nil || len(got) != 1 {
			t.Errorf("DueTimes before the later due = %v, %v; want one (the cutoff is exclusive)", got, err)
		}
		if got, err := repo.DueTimes(f.ctx, f.user, first); err != nil || len(got) != 0 {
			t.Errorf("DueTimes before the first due = %v, %v; want none", got, err)
		}
		if got, err := repo.DueTimes(f.ctx, other.ID, far); err != nil || len(got) != 1 {
			t.Errorf("another owner's DueTimes = %v, %v; want their one card", got, err)
		}
	})

	t.Run("user stats", func(t *testing.T) {
		// Seven days after the first answers, in UTC with midnight rollover.
		f.eval.t = t0.Add(7 * day)
		cfg := settings.Settings{Timezone: "UTC", RolloverHour: 0}
		st, err := f.mastery.Service(f.s).UserStats(f.ctx, f.user, cfg, metrics.StatsOptions{ActivityDays: 8})
		if err != nil {
			t.Fatal(err)
		}
		if st.Day.Date != "2026-01-09" || st.RetentionDays != 30 {
			t.Errorf("day %s retention window %d", st.Day.Date, st.RetentionDays)
		}

		// Reviews of cards in the review state: p1's third answer (right) and
		// p2's third (wrong). The earlier answers were learning-step answers.
		if st.Retention != (metrics.Retention{Reviews: 2, Retained: 1}) {
			t.Errorf("retention = %+v, want 1 of 2", st.Retention)
		}
		if st.ByTag[tagX] != (metrics.Retention{Reviews: 2, Retained: 1}) || st.ByTag[tagY] != (metrics.Retention{Reviews: 1}) || len(st.ByTag) != 2 {
			t.Errorf("by tag = %+v, want x 1 of 2 and y 0 of 1", st.ByTag)
		}

		// Eight study days from 01-02: new cards on the first day (the deleted
		// problem is not counted), reviews on days 3, 6 and 7.
		wantNew := map[string]int{"2026-01-02": 2}
		wantRev := map[string]int{"2026-01-04": 2, "2026-01-07": 1, "2026-01-08": 1}
		if len(st.Activity) != 8 || st.Activity[0].Date != "2026-01-02" || st.Activity[7].Date != "2026-01-09" {
			t.Fatalf("activity spans %+v", st.Activity)
		}
		for _, d := range st.Activity {
			if d.NewCards != wantNew[d.Date] || d.Reviews != wantRev[d.Date] {
				t.Errorf("%s = %d reviews %d new, want %d and %d", d.Date, d.Reviews, d.NewCards, wantRev[d.Date], wantNew[d.Date])
			}
		}

		// Due load: today runs to 01-10 00:00; whatever is due before then
		// (the relearning card, overdue) is today's, the rest by whole days.
		if len(st.DueLoad) != 8 {
			t.Fatalf("due load has %d days", len(st.DueLoad))
		}
		end := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
		want := make([]int, 8)
		for _, d := range dues {
			switch i := 1 + int(d.Sub(end)/day); {
			case d.Before(end):
				want[0]++
			case i <= 7:
				want[i]++
			}
		}
		for i, d := range st.DueLoad {
			if d.Cards != want[i] {
				t.Errorf("due load %s = %d, want %d", d.Date, d.Cards, want[i])
			}
		}

		// Another owner sees only their own figures.
		theirs, err := f.mastery.Service(f.s).UserStats(f.ctx, other.ID, cfg, metrics.StatsOptions{ActivityDays: 8})
		if err != nil || theirs.Retention.Reviews != 0 || theirs.Activity[0].NewCards != 1 || theirs.Activity[0].Reviews != 0 {
			t.Errorf("another owner's stats = %+v, %v", theirs, err)
		}

		// A one-day window keeps only p2's last, wrong, answer (p1's was two
		// days before).
		short, err := f.mastery.Service(f.s).UserStats(f.ctx, f.user, cfg, metrics.StatsOptions{RetentionDays: 1, ActivityDays: 1})
		if err != nil || short.Retention != (metrics.Retention{Reviews: 1}) || len(short.Activity) != 1 {
			t.Errorf("1 day window = %+v, %v; want 0 of 1", short, err)
		}
	})
}
