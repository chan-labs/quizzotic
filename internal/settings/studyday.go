// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"time"
	_ "time/tzdata" // zone data is embedded so lookups work on minimal images
)

// StudyDay is one study day: the half-open window [Start, End) between two
// consecutive rollover instants in the user's time zone.
type StudyDay struct {
	// Date is the calendar date (YYYY-MM-DD, in the user's zone) on which the
	// window starts. It names the study day.
	Date string
	// Start is the rollover instant that opens the day (inclusive).
	Start time.Time
	// End is the next rollover instant (exclusive).
	End time.Time
}

// Contains reports whether t falls inside the study day.
func (d StudyDay) Contains(t time.Time) bool {
	return !t.Before(d.Start) && t.Before(d.End)
}

// StudyDayAt returns the study day containing now for a user in loc whose day
// rolls over at rolloverHour (0-23) local time. With rollover 04:00, 03:59
// local still belongs to the previous calendar day's study day.
//
// DST rules: a day is measured between actual rollover instants, so it is 23 h
// or 25 h long across a transition. If the rollover wall time does not exist
// (spring-forward gap) the rollover is the transition instant itself; if it
// occurs twice (fall-back overlap) the first occurrence is the rollover.
func StudyDayAt(now time.Time, loc *time.Location, rolloverHour int) StudyDay {
	local := now.In(loc)
	y, m, d := local.Date()
	day := time.Date(y, m, d, 12, 0, 0, 0, time.UTC) // calendar-date carrier

	if now.Before(rolloverInstant(day, rolloverHour, loc)) {
		day = day.AddDate(0, 0, -1)
	}
	start := rolloverInstant(day, rolloverHour, loc)
	end := rolloverInstant(day.AddDate(0, 0, 1), rolloverHour, loc)
	// Pathological zones (rollover skipped over a whole date) could leave now
	// at or past end; step forward once so Contains(now) always holds.
	if !now.Before(end) {
		day = day.AddDate(0, 0, 1)
		start, end = end, rolloverInstant(day.AddDate(0, 0, 1), rolloverHour, loc)
	}
	return StudyDay{Date: day.Format("2006-01-02"), Start: start.UTC(), End: end.UTC()}
}

// rolloverInstant returns the instant at which hour:00 local wall time on the
// calendar date carried by day (its Y-M-D) first occurs in loc, resolving DST
// gaps and overlaps as documented on StudyDayAt.
func rolloverInstant(day time.Time, hour int, loc *time.Location) time.Time {
	y, m, d := day.Date()
	wall := time.Date(y, m, d, hour, 0, 0, 0, time.UTC) // wall clock labelled UTC

	// Offsets in force a day before and a day after the wall time bracket any
	// single transition near it.
	_, offBefore := wall.Add(-24 * time.Hour).In(loc).Zone()
	_, offAfter := wall.Add(24 * time.Hour).In(loc).Zone()

	var best time.Time
	for _, off := range [2]int{offBefore, offAfter} {
		c := wall.Add(-time.Duration(off) * time.Second)
		if _, got := c.In(loc).Zone(); got == off && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if !best.IsZero() {
		return best
	}
	// Gap: the wall time never happens. The candidate computed with the
	// pre-transition offset lies just after the transition; its zone starts at
	// the transition instant, which is the first moment at or after hour:00.
	c := wall.Add(-time.Duration(offBefore) * time.Second)
	start, _ := c.In(loc).ZoneBounds()
	if start.IsZero() {
		return c
	}
	return start
}
