// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
)

func TestNiceCeil(t *testing.T) {
	for _, c := range []struct{ max, top, step int }{
		{0, 1, 1}, {4, 4, 1}, {5, 6, 2}, {8, 8, 2}, {9, 10, 5}, {20, 20, 5}, {21, 30, 10}, {97, 100, 50}, {1234, 1500, 500},
	} {
		if top, step := niceCeil(c.max); top != c.top || step != c.step {
			t.Errorf("niceCeil(%d) = %d,%d, want %d,%d", c.max, top, step, c.top, c.step)
		}
	}
}

func TestBarChartScalesToTheTallestBar(t *testing.T) {
	c := buildBarChart("x", "T", "D", []string{"2026-03-01", "2026-03-02"}, []string{"n"}, [][]int{{0, 8}}, []string{"Day", "N"})
	if len(c.Bars) != 2 || c.Bars[0].H != 0 || c.Bars[1].H != c.Bottom-c.Top {
		t.Errorf("bars = %+v, want an empty bar and a full-height one", c.Bars)
	}
	if c.Bars[1].Tip != "2026-03-02: 8 n" || len(c.Table.Rows) != 2 || c.Table.Rows[1][1] != "8" {
		t.Errorf("tip/table = %q %v", c.Bars[1].Tip, c.Table.Rows)
	}
}

func TestRecallLinePositionsFollowDates(t *testing.T) {
	c := buildRecallLine("h", "T", "D", []historySample{
		{"2026-03-01", 0, 1}, {"2026-03-02", 0.5, 2}, {"2026-03-11", 1, 3},
	})
	if len(c.Points) != 3 {
		t.Fatalf("points = %+v", c.Points)
	}
	// One day of ten, so the second point is a tenth of the way across.
	if want := r1(c.Left + (c.Right-c.Left)/10); c.Points[1].X != want {
		t.Errorf("x = %v, want %v", c.Points[1].X, want)
	}
	if c.Points[0].Y != c.Bottom || c.Points[2].Y != c.Top {
		t.Errorf("y = %v..%v, want the axis bottom and top", c.Points[0].Y, c.Points[2].Y)
	}
	if !strings.HasPrefix(c.Path, "M") || strings.Count(c.Path, "L") != 2 {
		t.Errorf("path = %q", c.Path)
	}
}

func TestRecallLineWithOnePointOrNone(t *testing.T) {
	one := buildRecallLine("h", "T", "D", []historySample{{"2026-03-01", 0.9, 4}})
	if len(one.Points) != 1 || one.Points[0].X != r1((one.Left+one.Right)/2) || len(one.XLabels) != 1 {
		t.Errorf("one point = %+v labels %+v", one.Points, one.XLabels)
	}
	none := buildRecallLine("h", "T", "D", nil)
	if len(none.Points) != 0 || none.Path != "" {
		t.Errorf("no points = %+v", none)
	}
}
