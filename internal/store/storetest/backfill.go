// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/settings"
	"github.com/chan-labs/quizzotic/internal/store"
)

func runBackfill(t *testing.T, newStore NewStore) {
	t.Run("repo", func(t *testing.T) { runBackfillRepo(t, newStore) })
	t.Run("equals the live snapshots", func(t *testing.T) { runBackfillReplay(t, newStore) })
}

// runBackfillRepo covers the repo methods history rewriting is built on.
func runBackfillRepo(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	bob := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, bob); err != nil {
		t.Fatal(err)
	}
	tagA, tagB := f.newTag(f.user, "a"), f.newTag(f.user, "b")
	tagBob := f.newTag(bob.ID, "a")
	repo := f.s.MasterySnapshots()
	snap := func(owner, tag domain.ID, date string, recall float64, seen int) metrics.MasterySnapshot {
		return metrics.MasterySnapshot{UserID: owner, TagID: tag, Date: date, Recall: recall, CardsSeen: seen}
	}

	t.Run("put inserts and replaces", func(t *testing.T) {
		if err := repo.Put(f.ctx, f.user, snap(f.user, tagA, "2026-01-10", 0.75, 3)); err != nil {
			t.Fatal(err)
		}
		want := snap(f.user, tagA, "2026-01-10", 0.5, 4)
		if err := repo.Put(f.ctx, f.user, want); err != nil {
			t.Fatal(err)
		}
		got, err := repo.List(f.ctx, f.user, tagA, "", "")
		if err != nil || !reflect.DeepEqual(got, []metrics.MasterySnapshot{want}) {
			t.Fatalf("List = %+v, %v; want only the replacement %+v", got, err, want)
		}
	})

	t.Run("put is owner scoped", func(t *testing.T) {
		if err := repo.Put(f.ctx, bob.ID, snap(bob.ID, tagA, "2026-01-10", 0.5, 1)); !errors.Is(err, metrics.ErrNotFound) {
			t.Errorf("put on another owner's tag = %v, want ErrNotFound", err)
		}
		if err := repo.Put(f.ctx, f.user, snap(bob.ID, tagA, "2026-01-11", 0.5, 1)); err == nil {
			t.Error("put with a mismatched user ID succeeded")
		}
	})

	t.Run("delete tags removes only the owner's snapshots of those tags", func(t *testing.T) {
		for _, s := range []metrics.MasterySnapshot{
			snap(f.user, tagA, "2026-01-11", 0.5, 1), snap(f.user, tagB, "2026-01-10", 0.5, 1),
			snap(bob.ID, tagBob, "2026-01-10", 0.5, 1)} {
			if err := repo.Put(f.ctx, s.UserID, s); err != nil {
				t.Fatal(err)
			}
		}
		if err := repo.DeleteTags(f.ctx, f.user, nil); err != nil {
			t.Fatal(err)
		}
		if l, _ := repo.List(f.ctx, f.user, tagA, "", ""); len(l) != 2 {
			t.Fatalf("deleting no tags removed rows: %d left, want 2", len(l))
		}
		// Bob names ada's tag and his own: only his own has anything to remove.
		if err := repo.DeleteTags(f.ctx, bob.ID, []domain.ID{tagA, tagBob}); err != nil {
			t.Fatal(err)
		}
		if l, _ := repo.List(f.ctx, bob.ID, tagBob, "", ""); len(l) != 0 {
			t.Errorf("bob's own snapshots survived: %+v", l)
		}
		if l, _ := repo.List(f.ctx, f.user, tagA, "", ""); len(l) != 2 {
			t.Errorf("another owner's delete touched ada's snapshots: %+v", l)
		}
		if err := repo.DeleteTags(f.ctx, f.user, []domain.ID{tagA}); err != nil {
			t.Fatal(err)
		}
		if l, _ := repo.List(f.ctx, f.user, tagA, "", ""); len(l) != 0 {
			t.Errorf("tag A snapshots survived: %+v", l)
		}
		if l, _ := repo.List(f.ctx, f.user, tagB, "", ""); len(l) != 1 {
			t.Errorf("tag B snapshots were removed too: %+v", l)
		}
	})

	t.Run("history reads", func(t *testing.T) {
		live := f.newProblem(pkinds[0], problems.StatusLive)
		gone := f.newProblem(pkinds[1], problems.StatusLive)
		bobProblem, err := f.probs.Create(f.ctx, domain.Actor{Kind: domain.ActorHuman, UserID: bob.ID},
			problems.CreateInput{Type: pkinds[0].typ, Prompt: pkinds[0].prompt, Payload: []byte(pkinds[0].payload),
				Status: problems.StatusLive})
		if err != nil {
			t.Fatal(err)
		}
		f.tag(f.user, live.ID, tagA, tagB)
		f.tag(f.user, gone.ID, tagB)
		f.clock.Advance(time.Hour)
		f.edit(live.ID, problems.ChangeMajor)
		f.clock.Advance(time.Hour)
		f.edit(live.ID, problems.ChangeMinor)
		if _, err := f.answer(live.ID, pkinds[0].right, nil); err != nil {
			t.Fatal(err)
		}
		f.remove(gone.ID)

		m := f.s.Mastery()
		probs, err := m.HistoryProblems(f.ctx, f.user)
		if err != nil || len(probs) != 2 {
			t.Fatalf("HistoryProblems = %+v, %v; want ada's two problems, deleted included", probs, err)
		}
		by := map[domain.ID]metrics.HistoryProblem{}
		for _, p := range probs {
			by[p.ID] = p
		}
		l, g := by[live.ID], by[gone.ID]
		if l.Status != problems.StatusLive || !l.DeletedAt.IsZero() || !l.CreatedAt.Equal(t0) ||
			!reflect.DeepEqual(l.TagIDs, sortedIDs(tagA, tagB)) {
			t.Errorf("live problem = %+v", l)
		}
		// Version 1 is major, 2 is major, 3 is minor: two major versions.
		if len(l.MajorAt) != 2 || !l.MajorAt[0].Equal(t0) || !l.MajorAt[1].Equal(t0.Add(time.Hour)) {
			t.Errorf("MajorAt = %v, want creation and the first edit", l.MajorAt)
		}
		if got := l.EpochAt(t0.Add(30 * time.Minute)); got != 1 {
			t.Errorf("epoch before the major edit = %d, want 1", got)
		}
		if got := l.EpochAt(t0.Add(2 * time.Hour)); got != 2 {
			t.Errorf("epoch after the major edit = %d, want 2", got)
		}
		if g.DeletedAt.IsZero() || !g.LiveAt(t0) || g.LiveAt(g.DeletedAt) {
			t.Errorf("deleted problem = %+v; want live before its deletion and not after", g)
		}
		if foreign, _ := m.HistoryProblems(f.ctx, bob.ID); len(foreign) != 1 || foreign[0].ID != bobProblem.ID {
			t.Errorf("bob sees %+v, want only his own problem", foreign)
		}

		log, err := m.HistoryAttempts(f.ctx, f.user)
		if err != nil || len(log) != 1 || log[0].ProblemID != live.ID {
			t.Fatalf("HistoryAttempts = %+v, %v; want ada's one attempt", log, err)
		}
		if foreign, _ := m.HistoryAttempts(f.ctx, bob.ID); len(foreign) != 0 {
			t.Errorf("bob sees %d of ada's attempts", len(foreign))
		}

		tags, err := m.ActiveTags(f.ctx, f.user)
		if err != nil || len(tags) != 2 || !tags[0].CreatedAt.Equal(t0) {
			t.Fatalf("ActiveTags = %+v, %v; want ada's two tags created at t0", tags, err)
		}
		if foreign, _ := m.ActiveTags(f.ctx, bob.ID); len(foreign) != 1 || foreign[0].ID != tagBob {
			t.Errorf("bob sees tags %+v", foreign)
		}
	})
}

func sortedIDs(ids ...domain.ID) []domain.ID {
	out := append([]domain.ID(nil), ids...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].String() < out[i].String() {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// runBackfillReplay is the task's check: replaying the attempt log rewrites
// exactly the snapshots the daily job wrote live, for a history that includes
// answers, a major edit, an archive and a delete.
func runBackfillReplay(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	bob := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, bob); err != nil {
		t.Fatal(err)
	}
	tagA, tagB := f.newTag(f.user, "a"), f.newTag(f.user, "b")
	tagBob := f.newTag(bob.ID, "a")
	if err := f.s.MasterySnapshots().Put(f.ctx, bob.ID, metrics.MasterySnapshot{UserID: bob.ID, TagID: tagBob,
		Date: "2020-01-01", Recall: 0.25, CardsSeen: 2}); err != nil {
		t.Fatal(err)
	}

	p1 := f.newProblem(pkinds[0], problems.StatusLive)
	p2 := f.newProblem(pkinds[1], problems.StatusLive)
	p3 := f.newProblem(pkinds[0], problems.StatusLive)
	p4 := f.newProblem(pkinds[1], problems.StatusLive)
	f.tag(f.user, p1.ID, tagA)
	f.tag(f.user, p2.ID, tagA, tagB)
	f.tag(f.user, p3.ID, tagB)
	f.tag(f.user, p4.ID, tagB)

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	const rolloverHour = 4 // the default
	day := settings.StudyDayAt(t0, loc, rolloverHour)
	live := store.NewMastery(f.cards, f.eval).Snapshotter(f.s, nil)

	ans := func(p problems.View, right bool) {
		t.Helper()
		k := pkinds[0]
		if p.Type == pkinds[1].typ {
			k = pkinds[1]
		}
		resp := k.right
		if !right {
			resp = k.wrong
		}
		if _, err := f.answer(p.ID, resp, nil); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d settings.StudyDay, h time.Duration) { f.clock.t = d.Start.Add(h) }

	// Seven study days. Each begins with the daily job at the rollover, then
	// the day's activity. Day 0 starts before the data is created (t0), which
	// is mid-day.
	for k := 0; k < 7; k++ {
		f.eval.t = day.Start
		if err := live.SnapshotUser(f.ctx, f.user); err != nil {
			t.Fatal(err)
		}
		if err := live.SnapshotUser(f.ctx, bob.ID); err != nil {
			t.Fatal(err)
		}
		switch k {
		case 0:
			at(day, 15*time.Hour) // after t0
			ans(p1, true)
			f.clock.Advance(time.Minute)
			ans(p2, true)
			f.clock.Advance(time.Minute)
			ans(p3, false)
		case 1:
			at(day, 2*time.Hour)
			ans(p1, true)
			f.clock.Advance(time.Hour)
			ans(p2, false)
			f.clock.Advance(time.Hour)
			ans(p4, true)
		case 2:
			at(day, time.Hour)
			f.edit(p1.ID, problems.ChangeMajor)
			f.clock.Advance(time.Hour)
			ans(p1, true)
		case 3:
			at(day, time.Hour)
			f.archive(p4.ID)
			f.clock.Advance(time.Hour)
			ans(p3, true)
		case 4:
			at(day, time.Hour)
			f.remove(p2.ID)
			f.clock.Advance(time.Hour)
			ans(p1, true)
		}
		day = settings.StudyDayAt(day.End, loc, rolloverHour)
	}
	f.eval.t = day.Start // the day after the last, as the job would run it
	if err := live.SnapshotUser(f.ctx, f.user); err != nil {
		t.Fatal(err)
	}

	read := func(owner domain.ID, tags ...domain.ID) map[domain.ID][]metrics.MasterySnapshot {
		t.Helper()
		out := map[domain.ID][]metrics.MasterySnapshot{}
		for _, tg := range tags {
			l, err := f.s.MasterySnapshots().List(f.ctx, owner, tg, "", "")
			if err != nil {
				t.Fatal(err)
			}
			out[tg] = l
		}
		return out
	}
	want := read(f.user, tagA, tagB)
	bobBefore := read(bob.ID, tagBob)
	for _, tg := range []domain.ID{tagA, tagB} {
		if len(want[tg]) != 8 {
			t.Fatalf("live run wrote %d snapshots for a tag, want 8 days", len(want[tg]))
		}
	}
	// The history must be non-trivial for equality to mean anything: studied
	// cards, a drop when the problem is edited, archived and deleted.
	seen := map[int]bool{}
	for _, tg := range []domain.ID{tagA, tagB} {
		for _, s := range want[tg] {
			seen[s.CardsSeen] = true
		}
	}
	if len(seen) < 3 {
		t.Fatalf("live snapshots have only %d distinct coverage values; the fixture is too flat: %+v", len(seen), want)
	}

	rewrite := store.NewMastery(f.cards, f.eval).Snapshotter(f.s, nil)
	check := func(label string) {
		t.Helper()
		got := read(f.user, tagA, tagB)
		for _, tg := range []domain.ID{tagA, tagB} {
			if !reflect.DeepEqual(got[tg], want[tg]) {
				t.Errorf("%s: tag %v\n got  %+v\n want %+v", label, tg, got[tg], want[tg])
			}
		}
		if got := read(bob.ID, tagBob); !reflect.DeepEqual(got, bobBefore) {
			t.Errorf("%s: another owner's snapshots changed: %+v, want %+v", label, got, bobBefore)
		}
	}

	t.Run("full rewrite from nothing", func(t *testing.T) {
		for _, tg := range []domain.ID{tagA, tagB} {
			if err := f.s.MasterySnapshots().DeleteTags(f.ctx, f.user, []domain.ID{tg}); err != nil {
				t.Fatal(err)
			}
		}
		res, err := rewrite.BackfillUser(f.ctx, f.user, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Tags != 2 || res.Days != 8 || res.Snapshots != 16 {
			t.Errorf("result = %+v, want 2 tags, 8 days, 16 snapshots", res)
		}
		check("after backfill")
	})

	t.Run("rewrite replaces stale rows and is idempotent", func(t *testing.T) {
		if err := f.s.MasterySnapshots().Put(f.ctx, f.user, metrics.MasterySnapshot{UserID: f.user, TagID: tagA,
			Date: want[tagA][3].Date, Recall: 0.999, CardsSeen: 99}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := rewrite.BackfillUser(f.ctx, f.user, nil); err != nil {
				t.Fatal(err)
			}
			check("after rewrite")
		}
	})

	t.Run("one tag leaves the others alone", func(t *testing.T) {
		stale := metrics.MasterySnapshot{UserID: f.user, TagID: tagA, Date: want[tagA][3].Date, Recall: 0.999, CardsSeen: 99}
		if err := f.s.MasterySnapshots().Put(f.ctx, f.user, stale); err != nil {
			t.Fatal(err)
		}
		if err := f.s.MasterySnapshots().Put(f.ctx, f.user, metrics.MasterySnapshot{UserID: f.user, TagID: tagB,
			Date: want[tagB][3].Date, Recall: 0.999, CardsSeen: 99}); err != nil {
			t.Fatal(err)
		}
		res, err := rewrite.BackfillUser(f.ctx, f.user, []domain.ID{tagB, tagBob})
		if err != nil {
			t.Fatal(err)
		}
		if res.Tags != 1 || !reflect.DeepEqual(res.Skipped, []domain.ID{tagBob}) {
			t.Errorf("result = %+v, want tag B rewritten and bob's tag skipped", res)
		}
		got := read(f.user, tagA, tagB)
		if !reflect.DeepEqual(got[tagB], want[tagB]) {
			t.Errorf("tag B not restored:\n got  %+v\n want %+v", got[tagB], want[tagB])
		}
		if got[tagA][3] != stale {
			t.Errorf("tag A was touched by a tag B rewrite: %+v", got[tagA][3])
		}
		if _, err := rewrite.BackfillUser(f.ctx, f.user, nil); err != nil {
			t.Fatal(err)
		}
		check("after the full rewrite")
	})

	t.Run("unknown user", func(t *testing.T) {
		if _, err := rewrite.BackfillUser(f.ctx, f.ids.New(), nil); !errors.Is(err, metrics.ErrNotFound) {
			t.Errorf("backfill of an unknown user = %v, want ErrNotFound", err)
		}
	})

	t.Run("all users", func(t *testing.T) {
		users, sum, err := rewrite.BackfillAll(f.ctx)
		if err != nil || users != 2 {
			t.Fatalf("BackfillAll = %d users, %v", users, err)
		}
		if sum.Tags != 3 {
			t.Errorf("BackfillAll rewrote %d tags, want 3 (ada's two and bob's one)", sum.Tags)
		}
		got := read(f.user, tagA, tagB)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("all-user rewrite changed ada's history:\n got  %+v\n want %+v", got, want)
		}
	})
}
