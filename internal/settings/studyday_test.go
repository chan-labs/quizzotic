// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// at builds an instant from a local wall clock reading in loc.
func at(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, loc)
}

func TestStudyDayRolloverEdges(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	tests := []struct {
		name      string
		now       time.Time
		hour      int
		wantDate  string
		wantStart time.Time
		wantEnd   time.Time
	}{
		{"just before rollover is previous day", at(ny, 2026, 6, 10, 3, 59), 4, "2026-06-09", at(ny, 2026, 6, 9, 4, 0), at(ny, 2026, 6, 10, 4, 0)},
		{"exactly at rollover starts the new day", at(ny, 2026, 6, 10, 4, 0), 4, "2026-06-10", at(ny, 2026, 6, 10, 4, 0), at(ny, 2026, 6, 11, 4, 0)},
		{"one nanosecond before rollover", at(ny, 2026, 6, 10, 4, 0).Add(-time.Nanosecond), 4, "2026-06-09", at(ny, 2026, 6, 9, 4, 0), at(ny, 2026, 6, 10, 4, 0)},
		{"last minute of the day", at(ny, 2026, 6, 10, 23, 59), 4, "2026-06-10", at(ny, 2026, 6, 10, 4, 0), at(ny, 2026, 6, 11, 4, 0)},
		{"just after midnight is still yesterday", at(ny, 2026, 6, 11, 0, 1), 4, "2026-06-10", at(ny, 2026, 6, 10, 4, 0), at(ny, 2026, 6, 11, 4, 0)},
		{"rollover 0 is calendar midnight", at(ny, 2026, 6, 10, 0, 0), 0, "2026-06-10", at(ny, 2026, 6, 10, 0, 0), at(ny, 2026, 6, 11, 0, 0)},
		{"rollover 0 just before midnight", at(ny, 2026, 6, 9, 23, 59), 0, "2026-06-09", at(ny, 2026, 6, 9, 0, 0), at(ny, 2026, 6, 10, 0, 0)},
		{"rollover 23 late evening", at(ny, 2026, 6, 10, 23, 0), 23, "2026-06-10", at(ny, 2026, 6, 10, 23, 0), at(ny, 2026, 6, 11, 23, 0)},
		{"rollover 23 before it", at(ny, 2026, 6, 10, 22, 59), 23, "2026-06-09", at(ny, 2026, 6, 9, 23, 0), at(ny, 2026, 6, 10, 23, 0)},
		{"year boundary", at(ny, 2027, 1, 1, 2, 0), 4, "2026-12-31", at(ny, 2026, 12, 31, 4, 0), at(ny, 2027, 1, 1, 4, 0)},
		{"leap day", at(ny, 2028, 3, 1, 3, 0), 4, "2028-02-29", at(ny, 2028, 2, 29, 4, 0), at(ny, 2028, 3, 1, 4, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StudyDayAt(tc.now, ny, tc.hour)
			if got.Date != tc.wantDate || !got.Start.Equal(tc.wantStart) || !got.End.Equal(tc.wantEnd) {
				t.Fatalf("got %s [%s, %s), want %s [%s, %s)", got.Date, got.Start, got.End,
					tc.wantDate, tc.wantStart.UTC(), tc.wantEnd.UTC())
			}
			if !got.Contains(tc.now) {
				t.Fatalf("window does not contain now")
			}
		})
	}
}

func TestStudyDayDST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08: spring forward, 02:00 EST -> 03:00 EDT.
	// 2026-11-01: fall back, 02:00 EDT -> 01:00 EST.
	t.Run("study day spanning spring forward is 23 hours at rollover 4", func(t *testing.T) {
		got := StudyDayAt(at(ny, 2026, 3, 8, 1, 0), ny, 4) // 01:00 EST, before the gap
		if got.Date != "2026-03-07" {
			t.Fatalf("date = %s", got.Date)
		}
		if d := got.End.Sub(got.Start); d != 23*time.Hour {
			t.Fatalf("length = %s, want 23h", d)
		}
		if !got.Start.Equal(at(ny, 2026, 3, 7, 4, 0)) || !got.End.Equal(at(ny, 2026, 3, 8, 4, 0)) {
			t.Fatalf("window %s - %s", got.Start, got.End)
		}
		next := StudyDayAt(at(ny, 2026, 3, 8, 12, 0), ny, 4)
		if next.Date != "2026-03-08" || next.End.Sub(next.Start) != 24*time.Hour {
			t.Fatalf("next: %s %s", next.Date, next.End.Sub(next.Start))
		}
	})

	t.Run("previous day spans the spring-forward gap", func(t *testing.T) {
		// 03:30 EDT on the 8th is before the 04:00 rollover.
		got := StudyDayAt(at(ny, 2026, 3, 8, 3, 30), ny, 4)
		if got.Date != "2026-03-07" || got.End.Sub(got.Start) != 23*time.Hour {
			t.Fatalf("got %s length %s", got.Date, got.End.Sub(got.Start))
		}
	})

	t.Run("study day spanning fall back is 25 hours at rollover 4", func(t *testing.T) {
		got := StudyDayAt(at(ny, 2026, 11, 1, 12, 0).Add(-12*time.Hour), ny, 4) // 00:00 EDT
		if got.Date != "2026-10-31" {
			t.Fatalf("date = %s", got.Date)
		}
		if d := got.End.Sub(got.Start); d != 25*time.Hour {
			t.Fatalf("length = %s, want 25h", d)
		}
	})

	t.Run("repeated hour on fall back stays in one study day", func(t *testing.T) {
		first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)  // 01:30 EDT
		second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC) // 01:30 EST
		a, b := StudyDayAt(first, ny, 4), StudyDayAt(second, ny, 4)
		if a.Date != "2026-10-31" || b.Date != "2026-10-31" || !a.Start.Equal(b.Start) {
			t.Fatalf("a=%s b=%s", a.Date, b.Date)
		}
	})

	t.Run("rollover inside the spring-forward gap is the transition instant", func(t *testing.T) {
		transition := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC) // 02:00 EST == 03:00 EDT
		before := StudyDayAt(transition.Add(-time.Second), ny, 2)
		after := StudyDayAt(transition, ny, 2)
		if before.Date != "2026-03-07" || !before.End.Equal(transition) {
			t.Fatalf("before: %s end %s", before.Date, before.End)
		}
		if after.Date != "2026-03-08" || !after.Start.Equal(transition) {
			t.Fatalf("after: %s start %s", after.Date, after.Start)
		}
		if d := after.End.Sub(after.Start); d != 23*time.Hour {
			t.Fatalf("length = %s, want 23h (02:00 EDT next day)", d)
		}
	})

	t.Run("rollover inside the fall-back overlap is the first occurrence", func(t *testing.T) {
		firstOne := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC) // 01:00 EDT
		got := StudyDayAt(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), ny, 1)
		if got.Date != "2026-11-01" || !got.Start.Equal(firstOne) {
			t.Fatalf("got %s start %s, want start %s", got.Date, got.Start, firstOne)
		}
		before := StudyDayAt(firstOne.Add(-time.Second), ny, 1)
		if before.Date != "2026-10-31" {
			t.Fatalf("before = %s", before.Date)
		}
	})

	t.Run("southern hemisphere and half-hour zones", func(t *testing.T) {
		syd := mustLoc(t, "Australia/Sydney") // DST starts first Sunday of October
		got := StudyDayAt(at(syd, 2026, 10, 4, 1, 0), syd, 4)
		if got.Date != "2026-10-03" || got.End.Sub(got.Start) != 23*time.Hour {
			t.Fatalf("sydney: %s %s", got.Date, got.End.Sub(got.Start))
		}
		lh := mustLoc(t, "Australia/Lord_Howe") // 30-minute DST shift
		got = StudyDayAt(at(lh, 2026, 10, 4, 1, 0), lh, 4)
		if got.Date != "2026-10-03" || got.End.Sub(got.Start) != 23*time.Hour+30*time.Minute {
			t.Fatalf("lord howe: %s %s", got.Date, got.End.Sub(got.Start))
		}
	})

	t.Run("zone without DST", func(t *testing.T) {
		tokyo := mustLoc(t, "Asia/Tokyo")
		got := StudyDayAt(at(tokyo, 2026, 3, 8, 3, 0), tokyo, 4)
		if got.Date != "2026-03-07" || got.End.Sub(got.Start) != 24*time.Hour {
			t.Fatalf("got %s %s", got.Date, got.End.Sub(got.Start))
		}
	})

	t.Run("UTC", func(t *testing.T) {
		got := StudyDayAt(time.Date(2026, 5, 5, 3, 0, 0, 0, time.UTC), time.UTC, 4)
		if got.Date != "2026-05-04" {
			t.Fatalf("got %s", got.Date)
		}
	})
}

// Every minute across both 2026 New York transitions must land in a window
// that contains it, and consecutive windows must tile with no gap or overlap.
func TestStudyDayTilesAcrossTransitions(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	for _, hour := range []int{0, 1, 2, 3, 4, 12, 23} {
		prev := StudyDayAt(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), ny, hour)
		for now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC); now.Before(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)); now = now.Add(7 * time.Minute) {
			sd := StudyDayAt(now, ny, hour)
			if !sd.Contains(now) {
				t.Fatalf("hour %d: %s not in [%s, %s)", hour, now, sd.Start, sd.End)
			}
			if sd.Date != prev.Date {
				if !sd.Start.Equal(prev.End) {
					t.Fatalf("hour %d: gap or overlap between %s and %s", hour, prev.Date, sd.Date)
				}
				prev = sd
			}
		}
	}
}
