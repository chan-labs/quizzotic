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

func runSnapshots(t *testing.T, newStore NewStore) {
	t.Run("repo", func(t *testing.T) { runSnapshotRepo(t, newStore) })
	t.Run("scheduler", func(t *testing.T) { runSnapshotScheduler(t, newStore) })
	t.Run("backfill", func(t *testing.T) { runBackfill(t, newStore) })
}

func runSnapshotRepo(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	other := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	tagA, tagB := f.newTag(f.user, "a"), f.newTag(f.user, "b")
	tagOther := f.newTag(other.ID, "a")
	repo := f.s.MasterySnapshots()
	snap := func(owner, tag domain.ID, date string, recall float64) metrics.MasterySnapshot {
		return metrics.MasterySnapshot{UserID: owner, TagID: tag, Date: date, Recall: recall, CardsSeen: 3}
	}

	t.Run("insert is idempotent and never overwrites", func(t *testing.T) {
		first := snap(f.user, tagA, "2026-01-10", 0.75)
		if ok, err := repo.InsertIfAbsent(f.ctx, f.user, first); err != nil || !ok {
			t.Fatalf("first insert = %v, %v; want true", ok, err)
		}
		changed := first
		changed.Recall, changed.CardsSeen = 0.25, 9
		if ok, err := repo.InsertIfAbsent(f.ctx, f.user, changed); err != nil || ok {
			t.Fatalf("second insert = %v, %v; want false", ok, err)
		}
		got, err := repo.List(f.ctx, f.user, tagA, "", "")
		if err != nil || !reflect.DeepEqual(got, []metrics.MasterySnapshot{first}) {
			t.Fatalf("List = %+v, %v; want the first snapshot only", got, err)
		}
	})

	t.Run("list is ordered and bounded by date", func(t *testing.T) {
		for _, d := range []string{"2026-01-12", "2026-01-11"} {
			if _, err := repo.InsertIfAbsent(f.ctx, f.user, snap(f.user, tagA, d, 0.5)); err != nil {
				t.Fatal(err)
			}
		}
		dates := func(from, to string) []string {
			l, err := repo.List(f.ctx, f.user, tagA, from, to)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, s := range l {
				out = append(out, s.Date)
			}
			return out
		}
		if got, want := dates("", ""), []string{"2026-01-10", "2026-01-11", "2026-01-12"}; !reflect.DeepEqual(got, want) {
			t.Errorf("all = %v, want %v", got, want)
		}
		if got, want := dates("2026-01-11", "2026-01-11"), []string{"2026-01-11"}; !reflect.DeepEqual(got, want) {
			t.Errorf("single day = %v, want %v", got, want)
		}
		if got, want := dates("2026-01-11", ""), []string{"2026-01-11", "2026-01-12"}; !reflect.DeepEqual(got, want) {
			t.Errorf("open end = %v, want %v", got, want)
		}
		if got, want := dates("", "2026-01-10"), []string{"2026-01-10"}; !reflect.DeepEqual(got, want) {
			t.Errorf("open start = %v, want %v", got, want)
		}
	})

	t.Run("list by date", func(t *testing.T) {
		if _, err := repo.InsertIfAbsent(f.ctx, f.user, snap(f.user, tagB, "2026-01-11", 0.5)); err != nil {
			t.Fatal(err)
		}
		l, err := repo.ListDate(f.ctx, f.user, "2026-01-11")
		if err != nil || len(l) != 2 {
			t.Fatalf("ListDate = %+v, %v; want 2 snapshots", l, err)
		}
		if compareIDs(l[0].TagID, l[1].TagID) >= 0 {
			t.Errorf("ListDate is not ordered by tag ID: %+v", l)
		}
	})

	t.Run("owner scoping", func(t *testing.T) {
		if _, err := repo.InsertIfAbsent(f.ctx, other.ID, snap(other.ID, tagA, "2026-01-10", 0.5)); !errors.Is(err, metrics.ErrNotFound) {
			t.Errorf("insert for another owner's tag = %v, want ErrNotFound", err)
		}
		if _, err := repo.InsertIfAbsent(f.ctx, f.user, snap(other.ID, tagA, "2026-01-20", 0.5)); err == nil {
			t.Error("insert with a mismatched user ID succeeded")
		}
		if ok, err := repo.InsertIfAbsent(f.ctx, other.ID, snap(other.ID, tagOther, "2026-01-10", 0.5)); err != nil || !ok {
			t.Fatalf("own insert = %v, %v", ok, err)
		}
		if l, _ := repo.List(f.ctx, other.ID, tagA, "", ""); len(l) != 0 {
			t.Errorf("another owner's List saw %d snapshots", len(l))
		}
		if l, _ := repo.ListDate(f.ctx, other.ID, "2026-01-11"); len(l) != 0 {
			t.Errorf("another owner's ListDate saw %d snapshots", len(l))
		}
		if l, _ := repo.List(f.ctx, f.user, tagOther, "", ""); len(l) != 0 {
			t.Errorf("List of another owner's tag returned %d snapshots", len(l))
		}
	})

	t.Run("user ids", func(t *testing.T) {
		ids, err := repo.UserIDs(f.ctx)
		if err != nil || len(ids) != 2 {
			t.Fatalf("UserIDs = %v, %v; want both users", ids, err)
		}
	})
}

// runSnapshotScheduler drives the snapshotter with a fake clock across days.
func runSnapshotScheduler(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	bob := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, bob); err != nil {
		t.Fatal(err)
	}
	// Bob studies in Tokyo with a midnight rollover: his days turn over nine
	// hours before the default New York 04:00 ones.
	tz, hour := "Asia/Tokyo", 0
	if err := f.s.Settings().Replace(f.ctx, bob.ID, settings.Overrides{Timezone: &tz, RolloverHour: &hour}); err != nil {
		t.Fatal(err)
	}

	tagA, tagB := f.newTag(f.user, "a"), f.newTag(f.user, "b")
	tagBob := f.newTag(bob.ID, "a")
	p := f.newProblem(pkinds[0], problems.StatusLive)
	f.tag(f.user, p.ID, tagA)

	// 08:59 UTC is 03:59 in New York (still 9 Jan) and 17:59 in Tokyo (10 Jan).
	clock := &stepClock{t: time.Date(2026, 1, 10, 8, 59, 0, 0, time.UTC)}
	snapper := store.NewMastery(f.cards, clock).Snapshotter(f.s, nil)
	run := func() {
		t.Helper()
		if err := snapper.RunOnce(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	dates := func(owner, tag domain.ID) []string {
		t.Helper()
		l, err := f.s.MasterySnapshots().List(f.ctx, owner, tag, "", "")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range l {
			out = append(out, s.Date)
		}
		return out
	}

	run()
	run() // a second pass in the same study day writes nothing
	if got, want := dates(f.user, tagA), []string{"2026-01-09"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before the rollover: %v, want %v", got, want)
	}

	clock.Advance(time.Minute) // 09:00 UTC: 04:00 in New York, the rollover
	run()
	clock.Advance(6 * time.Hour)
	run()
	clock.Advance(18 * time.Hour) // 09:00 UTC the next day
	run()
	run()

	want := []string{"2026-01-09", "2026-01-10", "2026-01-11"}
	for _, tag := range []domain.ID{tagA, tagB} {
		if got := dates(f.user, tag); !reflect.DeepEqual(got, want) {
			t.Errorf("tag %v snapshots = %v, want exactly %v", tag, got, want)
		}
	}
	// Tokyo: 17:59 on the 10th, then midnight (15:00 UTC) starts the 11th,
	// which the run at 09:00 UTC on the 11th (18:00 in Tokyo) is still in.
	if got, want := dates(bob.ID, tagBob), []string{"2026-01-10", "2026-01-11"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tokyo snapshots = %v, want %v", got, want)
	}

	t.Run("snapshot carries recall and coverage", func(t *testing.T) {
		l, err := f.s.MasterySnapshots().ListDate(f.ctx, f.user, "2026-01-11")
		if err != nil || len(l) != 2 {
			t.Fatalf("ListDate = %+v, %v", l, err)
		}
		for _, s := range l {
			if s.CardsSeen != 0 || s.Recall != 0 {
				t.Errorf("snapshot of a tag with no studied cards = %+v", s)
			}
		}
	})

	t.Run("a tag created mid-day is picked up by the next pass", func(t *testing.T) {
		tagC := f.newTag(f.user, "c")
		run()
		if got := dates(f.user, tagC); !reflect.DeepEqual(got, []string{"2026-01-11"}) {
			t.Errorf("new tag snapshots = %v, want only today", got)
		}
		if got := dates(f.user, tagA); !reflect.DeepEqual(got, want) {
			t.Errorf("existing tag snapshots changed: %v", got)
		}
	})
}
