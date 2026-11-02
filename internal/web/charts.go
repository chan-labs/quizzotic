// SPDX-License-Identifier: Apache-2.0

package web

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Server-rendered SVG charts. The builders below turn numbers into plain
// geometry (rounded to a tenth of a unit) that charts.html draws; there is no
// script and no inline style, so the pages work without JS under the strict
// Content-Security-Policy. Every chart carries a title, a description and an
// equivalent data table so it is usable without seeing the graphic.

const (
	chartW      = 640.0
	chartH      = 220.0
	chartPadL   = 44.0
	chartPadR   = 12.0
	chartPadT   = 12.0
	chartPadB   = 28.0
	maxXLabels  = 6
	barGapRatio = 0.25
)

// dataTable is the text equivalent of a chart.
type dataTable struct {
	Head []string
	Rows [][]string
}

type chartTick struct {
	Y     float64
	Label string
}

type chartXLabel struct {
	X      float64
	Text   string
	Anchor string // SVG text-anchor
}

type chartBar struct {
	X, Y, W, H float64
	Series     int
	Tip        string
}

// chartFrame is what every chart shares: its box and axes.
type chartFrame struct {
	ID, Title, Desc string
	W, H            float64
	Left, Right     float64 // plot area x range
	Top, Bottom     float64 // plot area y range
	YTicks          []chartTick
	XLabels         []chartXLabel
	Table           dataTable
}

type barChart struct {
	chartFrame
	Bars   []chartBar
	Legend []string // series names, in series order; empty for one series
}

type chartPoint struct {
	X, Y float64
	Tip  string
}

type lineChart struct {
	chartFrame
	Path   string
	Points []chartPoint
}

func r1(v float64) float64 { return math.Round(v*10) / 10 }

func newFrame(id, title, desc string) chartFrame {
	return chartFrame{ID: id, Title: title, Desc: desc, W: chartW, H: chartH,
		Left: chartPadL, Right: chartW - chartPadR, Top: chartPadT, Bottom: chartH - chartPadB}
}

// niceCeil returns a round axis maximum >= max and the tick step to use, with
// at most four intervals.
func niceCeil(max int) (top, step int) {
	if max < 1 {
		max = 1
	}
	for mag := 1; ; mag *= 10 {
		for _, m := range []int{1, 2, 5} {
			if (max+m*mag-1)/(m*mag) <= 4 {
				step = m * mag
				return (max + step - 1) / step * step, step
			}
		}
	}
}

// buildBarChart draws one group of bars per label, one bar per series.
// series[s][i] is the value of series s at label i; names names the series.
func buildBarChart(id, title, desc string, labels []string, names []string, series [][]int, tableHead []string) barChart {
	c := barChart{chartFrame: newFrame(id, title, desc)}
	maxV := 0
	for _, s := range series {
		for _, v := range s {
			if v > maxV {
				maxV = v
			}
		}
	}
	top, step := niceCeil(maxV)
	h := c.Bottom - c.Top
	y := func(v float64) float64 { return r1(c.Bottom - v/float64(top)*h) }
	for v := 0; v <= top; v += step {
		c.YTicks = append(c.YTicks, chartTick{Y: y(float64(v)), Label: fmt.Sprint(v)})
	}
	n := len(labels)
	c.Table = dataTable{Head: tableHead}
	if n == 0 {
		return c
	}
	slot := (c.Right - c.Left) / float64(n)
	group := slot * (1 - barGapRatio)
	bw := group / float64(len(series))
	for i, lab := range labels {
		x0 := c.Left + slot*float64(i) + (slot-group)/2
		row := []string{lab}
		for s := range series {
			v := series[s][i]
			c.Bars = append(c.Bars, chartBar{
				X: r1(x0 + bw*float64(s)), Y: y(float64(v)), W: r1(bw), H: r1(c.Bottom - y(float64(v))), Series: s,
				Tip: fmt.Sprintf("%s: %d %s", lab, v, names[s]),
			})
			row = append(row, fmt.Sprint(v))
		}
		c.Table.Rows = append(c.Table.Rows, row)
	}
	every := (n + maxXLabels - 1) / maxXLabels
	for i, lab := range labels {
		if i%every == 0 {
			c.XLabels = append(c.XLabels, chartXLabel{X: r1(c.Left + slot*(float64(i)+0.5)), Text: shortDate(lab), Anchor: "middle"})
		}
	}
	if len(series) > 1 {
		c.Legend = names
	}
	return c
}

// shortDate drops the year of a YYYY-MM-DD label; other labels pass through.
func shortDate(s string) string {
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		return s[5:]
	}
	return s
}

// historySample is one dated recall value.
type historySample struct {
	Date      string
	Recall    float64
	CardsSeen int
}

// buildRecallLine draws recall (0..1) over time. The x position follows the
// real dates, so a gap in the snapshots shows as a gap.
func buildRecallLine(id, title, desc string, samples []historySample) lineChart {
	c := lineChart{chartFrame: newFrame(id, title, desc)}
	h := c.Bottom - c.Top
	y := func(v float64) float64 { return r1(c.Bottom - math.Max(0, math.Min(1, v))*h) }
	for _, p := range []int{0, 25, 50, 75, 100} {
		c.YTicks = append(c.YTicks, chartTick{Y: y(float64(p) / 100), Label: fmt.Sprintf("%d%%", p)})
	}
	c.Table = dataTable{Head: []string{"Date", "Recall", "Cards seen"}}
	n := len(samples)
	if n == 0 {
		return c
	}
	days := make([]float64, n)
	for i, s := range samples {
		if t, err := time.Parse("2006-01-02", s.Date); err == nil {
			days[i] = float64(t.Unix()) / 86400
		} else {
			days[i] = float64(i)
		}
	}
	span := days[n-1] - days[0]
	x := func(i int) float64 {
		if span <= 0 {
			return r1((c.Left + c.Right) / 2)
		}
		return r1(c.Left + (days[i]-days[0])/span*(c.Right-c.Left))
	}
	var path strings.Builder
	for i, s := range samples {
		p := chartPoint{X: x(i), Y: y(s.Recall), Tip: fmt.Sprintf("%s: recall %s, %d cards seen", s.Date, pct1(s.Recall), s.CardsSeen)}
		c.Points = append(c.Points, p)
		if i == 0 {
			fmt.Fprintf(&path, "M%v,%v", p.X, p.Y)
		} else {
			fmt.Fprintf(&path, " L%v,%v", p.X, p.Y)
		}
		c.Table.Rows = append(c.Table.Rows, []string{s.Date, pct1(s.Recall), fmt.Sprint(s.CardsSeen)})
	}
	c.Path = path.String()
	if n == 1 {
		c.XLabels = []chartXLabel{{X: c.Points[0].X, Text: samples[0].Date, Anchor: "middle"}}
	} else {
		c.XLabels = []chartXLabel{
			{X: c.Left, Text: samples[0].Date, Anchor: "start"},
			{X: c.Right, Text: samples[n-1].Date, Anchor: "end"},
		}
	}
	return c
}

func pct0(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }
func pct1(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

func optPct0(v *float64) string {
	if v == nil {
		return "–"
	}
	return pct0(*v)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
