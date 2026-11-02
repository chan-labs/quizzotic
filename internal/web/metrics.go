// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// MetricsReader is what the Metrics and Tags pages read from the mastery
// metrics. *metrics.QueryService implements it, the same service the
// /metrics API endpoints use.
type MetricsReader interface {
	Overview(ctx context.Context, userID domain.ID) (metrics.OverviewView, error)
	Tags(ctx context.Context, userID domain.ID, cursor string, limit int) (platform.PageResult[metrics.TagMasteryView], error)
	TagHistory(ctx context.Context, userID domain.ID, ref, from, to string) (metrics.TagHistoryView, error)
}

// UseMetrics enables the Metrics and Tags pages. Call it before Register.
func (w *Web) UseMetrics(m MetricsReader) { w.metrics = m }

func (w *Web) parseMetricsTemplates(layout *template.Template) error {
	for _, name := range []string{"metrics", "tags", "tag"} {
		t, err := layout.Clone()
		if err != nil {
			return err
		}
		if _, err := t.ParseFS(templateFS, "templates/"+name+".html", "templates/charts.html"); err != nil {
			return err
		}
		w.tmpl[name] = t
	}
	return nil
}

func (w *Web) registerMetrics(mux *http.ServeMux) {
	if w.metrics == nil {
		return
	}
	mux.Handle("GET /stats", w.gate(http.HandlerFunc(w.metricsPage)))
	mux.Handle("GET /tags", w.gate(http.HandlerFunc(w.tagsPage)))
	mux.Handle("GET /tags/{tag}", w.gate(http.HandlerFunc(w.tagPage)))
}

// ---- Metrics ----

type metricsView struct {
	StudyDay                   string
	Recall                     string
	CardsStudied               int
	Tags, TagsStudied          int
	Retention                  string // "–" when there was no review in the window
	RetentionReviews           int
	WindowDays                 int
	DesiredRetention           string
	Due                        barChart
	Activity                   barChart
	DueTotal, ReviewsInWindow  int
	NewCardsInWindow           int
	ActivityDays, ForecastDays int
}

func newMetricsView(o metrics.OverviewView) metricsView {
	v := metricsView{
		StudyDay: o.StudyDay, Recall: optPct0(o.Recall), CardsStudied: o.CardsStudied,
		Tags: o.Tags, TagsStudied: o.TagsStudied,
		Retention:        optPct0(o.TrueRetention.Rate),
		RetentionReviews: o.TrueRetention.Reviews, WindowDays: o.TrueRetention.WindowDays,
		DesiredRetention: pct0(o.TrueRetention.DesiredRetention),
		ActivityDays:     len(o.Activity), ForecastDays: len(o.DueLoad) - 1,
	}
	var dates []string
	var due []int
	for _, d := range o.DueLoad {
		dates, due = append(dates, d.Date), append(due, d.Cards)
		v.DueTotal += d.Cards
	}
	v.Due = buildBarChart("due", "Cards due per day",
		fmt.Sprintf("Bar chart of cards due on each of %d study days, starting today, %s. %d cards in total.", len(dates), o.StudyDay, v.DueTotal),
		dates, []string{"cards due"}, [][]int{due}, []string{"Study day", "Cards due"})
	dates = nil
	var reviews, fresh []int
	for _, a := range o.Activity {
		dates, reviews, fresh = append(dates, a.Date), append(reviews, a.Reviews), append(fresh, a.NewCards)
		v.ReviewsInWindow += a.Reviews
		v.NewCardsInWindow += a.NewCards
	}
	v.Activity = buildBarChart("activity", "Activity per day",
		fmt.Sprintf("Bar chart of reviews and new cards on each of the last %d study days, ending %s. %d reviews and %d new cards in total.",
			len(dates), o.StudyDay, v.ReviewsInWindow, v.NewCardsInWindow),
		dates, []string{"reviews", "new cards"}, [][]int{reviews, fresh}, []string{"Study day", "Reviews", "New cards"})
	return v
}

func (w *Web) metricsPage(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	o, err := w.metrics.Overview(r.Context(), ws.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	w.render(rw, r, "metrics", page{Title: "Metrics", Data: newMetricsView(o)})
}

// ---- Tags ----

type tagRow struct {
	ID, Slug, Name      string
	Recall              string
	RecallValue         float64 // for the meter; valid when HasRecall
	HasRecall           bool
	Coverage            string
	CardsSeen, CardsAll int
	Stability           string
	LapseRate           string
	Retention           string
	RetentionReviews    int
	LastReviewed        string
	Reviews, Lapses     int
}

func newTagRow(t metrics.TagMasteryView) tagRow {
	row := tagRow{
		ID: t.TagID.String(), Slug: t.Slug, Name: t.Name,
		Recall: optPct0(t.Recall), Coverage: pct0(t.Coverage), CardsSeen: t.CardsSeen, CardsAll: t.CardsTotal,
		Stability: fmt.Sprintf("%.1f days", t.StabilityMedian), LapseRate: pct0(t.LapseRate),
		Retention: optPct0(t.TrueRetention.Rate), RetentionReviews: t.TrueRetention.Reviews,
		LastReviewed: "–", Reviews: t.Reviews, Lapses: t.Lapses,
	}
	if t.CardsSeen == 0 {
		row.Stability = "–"
	}
	if t.Recall != nil {
		row.HasRecall, row.RecallValue = true, *t.Recall
	}
	if t.LastAttemptAt != nil {
		row.LastReviewed = t.LastAttemptAt.UTC().Format("2006-01-02")
	}
	return row
}

// allTags reads every page of the owner's tag mastery, weakest recall first;
// tags with nothing studied yet come last.
func (w *Web) allTags(ctx context.Context, userID domain.ID) ([]tagRow, error) {
	var rows []tagRow
	cursor := ""
	for {
		pg, err := w.metrics.Tags(ctx, userID, cursor, platform.MaxLimit)
		if err != nil {
			return nil, err
		}
		for _, t := range pg.Items {
			rows = append(rows, newTagRow(t))
		}
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			break
		}
		cursor = *pg.NextCursor
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.HasRecall != b.HasRecall {
			return a.HasRecall
		}
		if a.HasRecall && a.RecallValue != b.RecallValue {
			return a.RecallValue < b.RecallValue
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return rows, nil
}

func (w *Web) tagsPage(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	rows, err := w.allTags(r.Context(), ws.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	w.render(rw, r, "tags", page{Title: "Tags", Data: struct{ Rows []tagRow }{rows}})
}

type tagDetail struct {
	Tag     tagRow
	Chart   lineChart
	Samples int
}

func (w *Web) tagPage(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	h, err := w.metrics.TagHistory(r.Context(), ws.UserID, r.PathValue("tag"), "", "")
	if err != nil {
		w.fail(rw, err)
		return
	}
	rows, err := w.allTags(r.Context(), ws.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	d := tagDetail{Tag: tagRow{ID: h.TagID.String(), Slug: h.Slug, Name: h.Name, Recall: "–", Coverage: "–", Stability: "–",
		LapseRate: "–", Retention: "–", LastReviewed: "–"}}
	for _, t := range rows {
		if t.ID == d.Tag.ID {
			d.Tag = t
		}
	}
	// A day on which nothing in the tag had been studied has no recall to plot
	// (the snapshot job still writes a zero row for it).
	var samples []historySample
	for _, p := range h.Items {
		if p.CardsSeen > 0 {
			samples = append(samples, historySample{Date: p.Date, Recall: p.Recall, CardsSeen: p.CardsSeen})
		}
	}
	d.Samples = len(samples)
	desc := fmt.Sprintf("Line chart of the mean predicted recall of tag %s, one point per study day, from 0 to 100 percent.", h.Name)
	if n := len(samples); n > 0 {
		desc += fmt.Sprintf(" %s from %s to %s; latest %s.", plural(n, "point", "points"), samples[0].Date, samples[n-1].Date, pct1(samples[n-1].Recall))
	} else {
		desc += " No points yet."
	}
	d.Chart = buildRecallLine("history", "Recall over time", desc, samples)
	w.render(rw, r, "tag", page{Title: "Tag " + h.Name, Data: d})
}
