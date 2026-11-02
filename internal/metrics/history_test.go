// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"slices"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/settings"
)

var h0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// hdays are n consecutive 24-hour study days starting at h0.
func hdays(n int) []settings.StudyDay {
	out := make([]settings.StudyDay, n)
	for i := range out {
		s := h0.Add(time.Duration(i) * 24 * time.Hour)
		out[i] = settings.StudyDay{Date: s.Format("2006-01-02"), Start: s, End: s.Add(24 * time.Hour)}
	}
	return out
}

func right(user, problem domain.ID, epoch int, at time.Time) attempts.Attempt {
	ok := true
	return attempts.Attempt{UserID: user, ProblemID: problem, CardEpoch: epoch, AutoCorrect: &ok, AnsweredAt: at}
}

func TestHistoryProblemLiveAt(t *testing.T) {
	created, later := h0, h0.Add(48*time.Hour)
	for name, tc := range map[string]struct {
		p    HistoryProblem
		at   time.Time
		want bool
	}{
		"before creation":          {HistoryProblem{Status: problems.StatusLive, CreatedAt: created}, created.Add(-time.Second), false},
		"at creation":              {HistoryProblem{Status: problems.StatusLive, CreatedAt: created}, created, true},
		"live":                     {HistoryProblem{Status: problems.StatusLive, CreatedAt: created}, later, true},
		"never live (draft)":       {HistoryProblem{Status: problems.StatusDraft, CreatedAt: created}, later, false},
		"archived, before archive": {HistoryProblem{Status: problems.StatusArchived, CreatedAt: created, UpdatedAt: later}, later.Add(-time.Second), true},
		"archived, at archive":     {HistoryProblem{Status: problems.StatusArchived, CreatedAt: created, UpdatedAt: later}, later, false},
		"deleted, before":          {HistoryProblem{Status: problems.StatusLive, CreatedAt: created, DeletedAt: later}, later.Add(-time.Second), true},
		"deleted, at":              {HistoryProblem{Status: problems.StatusLive, CreatedAt: created, DeletedAt: later}, later, false},
	} {
		if got := tc.p.LiveAt(tc.at); got != tc.want {
			t.Errorf("%s: LiveAt = %v, want %v", name, got, tc.want)
		}
	}
}

func TestHistoryProblemEpochAt(t *testing.T) {
	p := HistoryProblem{MajorAt: []time.Time{h0, h0.Add(time.Hour)}}
	for at, want := range map[time.Time]int{
		h0.Add(-time.Hour):     1, // before the first version: still at least 1
		h0:                     1,
		h0.Add(time.Hour):      2,
		h0.Add(24 * time.Hour): 2,
	} {
		if got := p.EpochAt(at); got != want {
			t.Errorf("EpochAt(%v) = %d, want %d", at, got, want)
		}
	}
}

func TestReplaySnapshots(t *testing.T) {
	p := fsrs.Default()
	ids := domain.NewIDGenerator(domain.SystemClock{})
	user, tagA, tagB := ids.New(), ids.New(), ids.New()
	p1, p2 := ids.New(), ids.New()
	days := hdays(4)

	// p1 is answered during day 0 and again during day 2; p2 is created in
	// day 1 and never answered. Both carry tag A.
	log := []attempts.Attempt{
		right(user, p1, 1, h0.Add(2*time.Hour)),
		right(user, p1, 1, h0.Add(50*time.Hour)),
	}
	probs := []HistoryProblem{
		{ID: p1, Status: problems.StatusLive, CreatedAt: h0.Add(-time.Hour), MajorAt: []time.Time{h0.Add(-time.Hour)}, TagIDs: []domain.ID{tagA}},
		{ID: p2, Status: problems.StatusLive, CreatedAt: h0.Add(30 * time.Hour), MajorAt: []time.Time{h0.Add(30 * time.Hour)}, TagIDs: []domain.ID{tagA}},
	}
	tags := []HistoryTag{{ID: tagA, CreatedAt: h0.Add(-time.Hour)}}
	snaps := ReplaySnapshots(p, user, days, tags, probs, log)

	if len(snaps) != 4 {
		t.Fatalf("got %d snapshots, want one per day: %+v", len(snaps), snaps)
	}
	// Day 0 starts before the first answer, so nothing is studied yet; day 1
	// sees it; day 3 sees the second answer too.
	if snaps[0].CardsSeen != 0 || snaps[0].Recall != 0 {
		t.Errorf("day 0 = %+v, want nothing studied", snaps[0])
	}
	first := p.Next(fsrs.Card{}, fsrs.Good, log[0].AnsweredAt)
	if want := p.Retrievability(first, days[1].Start); snaps[1].CardsSeen != 1 || snaps[1].Recall != want {
		t.Errorf("day 1 = %+v, want recall %v over 1 card", snaps[1], want)
	}
	second := p.Next(first, fsrs.Good, log[1].AnsweredAt)
	if want := p.Retrievability(second, days[3].Start); snaps[3].Recall != want {
		t.Errorf("day 3 recall = %v, want %v (card refolded with the second answer)", snaps[3].Recall, want)
	}
	for i, s := range snaps {
		if s.Date != days[i].Date || s.TagID != tagA || s.UserID != user {
			t.Errorf("snapshot %d = %+v, want the day %s of tag A", i, s, days[i].Date)
		}
	}

	t.Run("a card answered at the rollover instant counts", func(t *testing.T) {
		l := []attempts.Attempt{right(user, p1, 1, days[1].Start)}
		got := ReplaySnapshots(p, user, days, tags, probs, l)
		if got[0].CardsSeen != 0 || got[1].CardsSeen != 1 {
			t.Errorf("coverage by day = %d, %d; want 0 then 1", got[0].CardsSeen, got[1].CardsSeen)
		}
	})

	t.Run("archived and deleted problems drop out from the day they leave", func(t *testing.T) {
		gone := slices.Clone(probs)
		gone[0].Status, gone[0].UpdatedAt = problems.StatusArchived, h0.Add(26*time.Hour)
		got := ReplaySnapshots(p, user, days, tags, gone, log)
		if got[1].CardsSeen != 1 || got[2].CardsSeen != 0 {
			t.Errorf("coverage by day = %d then %d; want studied until the archive, none after", got[1].CardsSeen, got[2].CardsSeen)
		}
		del := slices.Clone(probs)
		del[0].DeletedAt = h0.Add(26 * time.Hour)
		if d := ReplaySnapshots(p, user, days, tags, del, log); d[1].CardsSeen != 1 || d[2].CardsSeen != 0 {
			t.Errorf("deleted: coverage by day = %d then %d", d[1].CardsSeen, d[2].CardsSeen)
		}
	})

	t.Run("a major edit starts a new card", func(t *testing.T) {
		edited := slices.Clone(probs)
		edited[0].MajorAt = []time.Time{h0.Add(-time.Hour), h0.Add(10 * time.Hour)}
		got := ReplaySnapshots(p, user, days, tags, edited, log)
		// Both logged answers belong to epoch 1, so from the edit on the
		// problem has no studied card: day 1 shows nothing.
		if got[1].CardsSeen != 0 {
			t.Errorf("day 1 coverage = %d, want 0 after the major edit", got[1].CardsSeen)
		}
		e2 := []attempts.Attempt{log[0], log[1], right(user, p1, 2, h0.Add(30*time.Hour))}
		got = ReplaySnapshots(p, user, days, tags, edited, e2)
		if got[1].CardsSeen != 0 || got[2].CardsSeen != 1 {
			t.Errorf("coverage by day = %d then %d, want the epoch 2 card studied from day 2", got[1].CardsSeen, got[2].CardsSeen)
		}
	})

	t.Run("tags follow the current mapping and exist from creation or first problem", func(t *testing.T) {
		// Tag B is created on day 2 but its problem has existed (and been
		// studied) since before day 0: a split gives it that history.
		withB := slices.Clone(probs)
		withB[0].TagIDs = []domain.ID{tagA, tagB}
		born := []HistoryTag{tags[0], {ID: tagB, CreatedAt: h0.Add(50 * time.Hour)}}
		got := ReplaySnapshots(p, user, days, born, withB, log)
		byTag := map[domain.ID][]MasterySnapshot{}
		for _, s := range got {
			byTag[s.TagID] = append(byTag[s.TagID], s)
		}
		if len(byTag[tagB]) != 4 || byTag[tagB][1].CardsSeen != 1 {
			t.Errorf("tag B snapshots = %+v, want all 4 days with day 1 studied", byTag[tagB])
		}

		// A tag with no problem starts on the study day it was created.
		empty := []HistoryTag{{ID: tagB, CreatedAt: h0.Add(50 * time.Hour)}}
		got = ReplaySnapshots(p, user, days, empty, nil, nil)
		if len(got) != 2 || got[0].Date != days[2].Date {
			t.Errorf("empty tag snapshots = %+v, want days 2 and 3 only", got)
		}
	})

	t.Run("only the requested tags are computed", func(t *testing.T) {
		got := ReplaySnapshots(p, user, days, []HistoryTag{{ID: tagB, CreatedAt: h0.Add(-time.Hour)}}, probs, log)
		for _, s := range got {
			if s.TagID != tagB || s.CardsSeen != 0 {
				t.Errorf("snapshot %+v leaked from another tag", s)
			}
		}
	})

	t.Run("the order of the input problems does not matter", func(t *testing.T) {
		l := []attempts.Attempt{
			right(user, p1, 1, h0.Add(time.Hour)), right(user, p2, 1, h0.Add(40*time.Hour)),
		}
		slices.SortFunc(l, func(a, b attempts.Attempt) int { return compareID(a.ProblemID, b.ProblemID) })
		a := ReplaySnapshots(p, user, days, tags, probs, l)
		b := ReplaySnapshots(p, user, days, tags, []HistoryProblem{probs[1], probs[0]}, l)
		if !slices.Equal(a, b) {
			t.Errorf("order of problems changed the result:\n%+v\n%+v", a, b)
		}
	})
}
