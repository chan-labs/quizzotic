// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

// itMetricsReader is an agent client that may read metrics and nothing else.
const itMetricsReader = "quizzotic-metrics-reader"

func init() {
	// Every metrics endpoint answers from the caller's own cards, tags and
	// snapshots: user B must see none of user A's tag names, prompts or IDs.
	ownerScope.Resource(ownerscope.Resource{
		Name: "metrics",
		Create: func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
			suffix := domain.NewIDGenerator(domain.SystemClock{}).New().String()
			name := "metricssecret" + suffix
			r := c.Do("POST", "/tags", map[string]string{"name": name})
			if r.Code != http.StatusCreated {
				t.Fatalf("create tag = %d (%s)", r.Code, r.Body)
			}
			var tg tagJSON
			if err := json.Unmarshal(r.Body, &tg); err != nil {
				t.Fatal(err)
			}
			prompt := "Metrics owner-scope prompt " + suffix
			body := openBody(prompt)
			body["status"] = "live"
			r = c.Do("POST", "/problems", body)
			if r.Code != http.StatusCreated {
				t.Fatalf("create problem = %d (%s)", r.Code, r.Body)
			}
			var v problems.View
			if err := json.Unmarshal(r.Body, &v); err != nil {
				t.Fatal(err)
			}
			if r = c.Do("PUT", "/problems/"+v.ID.String()+"/tags", map[string]any{"tags": []string{tg.ID}}); r.Code != http.StatusOK {
				t.Fatalf("tag problem = %d (%s)", r.Code, r.Body)
			}
			if r = c.Do("POST", "/attempts", map[string]any{"problem_id": v.ID.String(), "problem_version": 1,
				"latency_ms": 10, "rating": 3, "response": map[string]any{"text": "x"}}); r.Code != http.StatusCreated {
				t.Fatalf("create attempt = %d (%s)", r.Code, r.Body)
			}
			return ownerscope.Created{
				Params:  ownerscope.Params{"tag": tg.ID},
				Secrets: []string{prompt, tg.Slug, name},
			}
		},
		Ops: []ownerscope.Op{
			{Pattern: "GET /metrics/tags/{tag}/history"},
			{Pattern: "GET /metrics/overview", List: true},
			{Pattern: "GET /metrics/tags", List: true},
			{Pattern: "GET /metrics/problems", List: true},
		},
	})
}

// metrics response shapes.
type (
	rateJSON struct {
		Reviews  int      `json:"reviews"`
		Retained int      `json:"retained"`
		Rate     *float64 `json:"rate"`
	}
	overviewJSON struct {
		AsOf          string   `json:"as_of"`
		StudyDay      string   `json:"study_day"`
		Tags          int      `json:"tags"`
		TagsStudied   int      `json:"tags_studied"`
		CardsStudied  int      `json:"cards_studied"`
		Recall        *float64 `json:"recall"`
		TrueRetention struct {
			rateJSON
			WindowDays       int     `json:"window_days"`
			DesiredRetention float64 `json:"desired_retention"`
		} `json:"true_retention"`
		DueLoad []struct {
			Date  string `json:"date"`
			Cards int    `json:"cards"`
		} `json:"due_load"`
		Activity []struct {
			Date     string `json:"date"`
			Reviews  int    `json:"reviews"`
			NewCards int    `json:"new_cards"`
		} `json:"activity"`
	}
	tagMasteryJSON struct {
		TagID      string   `json:"tag_id"`
		Slug       string   `json:"slug"`
		Name       string   `json:"name"`
		Recall     *float64 `json:"recall"`
		Coverage   float64  `json:"coverage"`
		LapseRate  float64  `json:"lapse_rate"`
		CardsTotal int      `json:"cards_total"`
		CardsSeen  int      `json:"cards_seen"`
		Reviews    int      `json:"reviews"`
	}
	tagMasteryPage struct {
		Items      []tagMasteryJSON `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	historyJSON struct {
		TagID string  `json:"tag_id"`
		Slug  string  `json:"slug"`
		From  *string `json:"from"`
		To    *string `json:"to"`
		Items []struct {
			Date      string  `json:"date"`
			Recall    float64 `json:"recall"`
			CardsSeen int     `json:"cards_seen"`
		} `json:"items"`
	}
	weakJSON struct {
		Items []struct {
			ProblemID string   `json:"problem_id"`
			Prompt    string   `json:"prompt"`
			Recall    float64  `json:"recall"`
			Lapses    int      `json:"lapses"`
			Tags      []string `json:"tags"`
		} `json:"items"`
	}
)

// metricsFixture is an author with three tags: two studied (one of them left
// to decay for a year) and one with only a new problem.
type metricsFixture struct {
	e                       *probEnv
	owner                   domain.ID
	fresh, stale, untouched domain.ID
	pFresh, pStale          problems.View
}

func newMetricsFixture(t *testing.T) *metricsFixture {
	t.Helper()
	e := newProbEnv(t)
	ctx := context.Background()
	e.req(t, "GET", "/me", e.human, nil, nil) // provision the user
	uid, _ := e.userFor(t, "author-sub")
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatal(err)
	}
	f := &metricsFixture{e: e, owner: owner}
	f.fresh, f.stale, f.untouched = e.newTag(t, "fresh-topic"), e.newTag(t, "stale-topic"), e.newTag(t, "untouched-topic")
	f.pFresh = e.tagged(t, "Fresh problem", f.fresh.String())
	f.pStale = e.tagged(t, "Stale problem", f.stale.String())
	e.tagged(t, "Never answered", f.untouched.String())
	for _, p := range []problems.View{f.pFresh, f.pStale} {
		if r := e.answer(t, e.human, attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})); r.code != http.StatusCreated {
			t.Fatalf("answer = %d (%s)", r.code, r.body)
		}
	}
	// The stale card was last reviewed a year ago with a one-day stability, so
	// its predicted recall is far below the fresh card's. The endpoints predict
	// recall at request time, so no rebuild is needed.
	c, err := e.st.Cards().Get(ctx, owner, f.pStale.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.LastReview, c.Stability = time.Now().Add(-365*24*time.Hour), 1
	if err := e.st.Cards().Put(ctx, owner, c); err != nil {
		t.Fatal(err)
	}
	return f
}

func getMetrics[T any](t *testing.T, e *probEnv, tok, path string) T {
	t.Helper()
	r := e.req(t, "GET", path, tok, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET %s = %d (%s)", path, r.code, r.body)
	}
	return decodeReply[T](t, r)
}

func TestMetricsOverview(t *testing.T) {
	f := newMetricsFixture(t)
	o := getMetrics[overviewJSON](t, f.e, f.e.human, "/metrics/overview")
	if o.Tags != 3 || o.TagsStudied != 2 || o.CardsStudied != 2 {
		t.Errorf("tags/studied/cards = %d/%d/%d, want 3/2/2", o.Tags, o.TagsStudied, o.CardsStudied)
	}
	// One card at ~1 and one decayed far below it: the mean is well inside (0.3, 0.8).
	if o.Recall == nil || *o.Recall < 0.3 || *o.Recall > 0.8 {
		t.Errorf("recall = %v, want the mean of a fresh and a decayed card", o.Recall)
	}
	if o.TrueRetention.WindowDays != 30 || o.TrueRetention.DesiredRetention != 0.9 || o.TrueRetention.Rate != nil {
		t.Errorf("true retention = %+v, want a 30 day window, 0.9 desired and no rate before a review-state review", o.TrueRetention)
	}
	if len(o.DueLoad) != 8 || o.DueLoad[0].Date != o.StudyDay {
		t.Errorf("due load = %+v, want 8 days starting at the study day %s", o.DueLoad, o.StudyDay)
	}
	due := 0
	for _, d := range o.DueLoad {
		due += d.Cards
	}
	// Two answered cards exist (unanswered problems have none), both due soon.
	if due != 2 {
		t.Errorf("cards due over the week = %d, want 2", due)
	}
	if n := len(o.Activity); n != 30 || o.Activity[n-1].Date != o.StudyDay || o.Activity[n-1].NewCards != 2 {
		t.Errorf("activity = %+v, want 30 days ending %s with 2 new cards today", o.Activity, o.StudyDay)
	}
}

func TestMetricsTags(t *testing.T) {
	f := newMetricsFixture(t)
	page := getMetrics[tagMasteryPage](t, f.e, f.e.human, "/metrics/tags")
	if len(page.Items) != 3 || page.NextCursor != nil {
		t.Fatalf("tags = %+v", page)
	}
	by := map[string]tagMasteryJSON{}
	for _, it := range page.Items {
		by[it.Slug] = it
	}
	fresh, stale, none := by["fresh-topic"], by["stale-topic"], by["untouched-topic"]
	if fresh.TagID != f.fresh.String() || fresh.Name != "fresh-topic" {
		t.Errorf("fresh = %+v", fresh)
	}
	if fresh.Recall == nil || *fresh.Recall < 0.9 || fresh.CardsSeen != 1 || fresh.CardsTotal != 1 || fresh.Coverage != 1 || fresh.Reviews != 1 {
		t.Errorf("fresh = %+v, want recall >= 0.9 and one of one card seen", fresh)
	}
	if stale.Recall == nil || *stale.Recall > 0.5 {
		t.Errorf("stale = %+v, want a decayed recall", stale)
	}
	if none.Recall != nil || none.CardsSeen != 0 || none.CardsTotal != 1 || none.Coverage != 0 {
		t.Errorf("untouched = %+v, want no recall, coverage 0 of 1", none)
	}

	// Paging walks the same three tags in ID order.
	var seen []string
	cursor := ""
	for i := 0; i < 5; i++ {
		q := "/metrics/tags?limit=2"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		p := getMetrics[tagMasteryPage](t, f.e, f.e.human, q)
		for _, it := range p.Items {
			seen = append(seen, it.TagID)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	if len(seen) != 3 || seen[0] >= seen[1] || seen[1] >= seen[2] {
		t.Errorf("paged tags = %v, want the 3 tags once each in ID order", seen)
	}
	for _, q := range []string{"?limit=0", "?limit=201", "?cursor=!!"} {
		if r := f.e.req(t, "GET", "/metrics/tags"+q, f.e.human, nil, nil); r.code != http.StatusUnprocessableEntity && r.code != http.StatusBadRequest {
			t.Errorf("GET /metrics/tags%s = %d, want a client error (%s)", q, r.code, r.body)
		}
	}
}

func TestMetricsTagHistory(t *testing.T) {
	f := newMetricsFixture(t)
	ctx := context.Background()
	snap := store.NewMastery(store.DefaultCards(), domain.SystemClock{}).Snapshotter(f.e.st, nil)
	if err := snap.SnapshotUser(ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	today := getMetrics[overviewJSON](t, f.e, f.e.human, "/metrics/overview").StudyDay

	for _, ref := range []string{"fresh-topic", f.fresh.String()} {
		h := getMetrics[historyJSON](t, f.e, f.e.human, "/metrics/tags/"+ref+"/history")
		if h.TagID != f.fresh.String() || h.Slug != "fresh-topic" || h.From != nil || h.To != nil {
			t.Errorf("history(%s) = %+v", ref, h)
		}
		// The snapshot is filed under the study day the overview reports.
		if len(h.Items) != 1 || h.Items[0].Date != today || h.Items[0].CardsSeen != 1 || h.Items[0].Recall < 0.9 {
			t.Errorf("history(%s) items = %+v, want one snapshot for %s", ref, h.Items, today)
		}
	}
	stale := getMetrics[historyJSON](t, f.e, f.e.human, "/metrics/tags/stale-topic/history")
	if len(stale.Items) != 1 || stale.Items[0].Recall > 0.5 {
		t.Errorf("stale history = %+v", stale.Items)
	}

	day, _ := time.Parse("2006-01-02", today)
	next, prev := day.AddDate(0, 0, 1).Format("2006-01-02"), day.AddDate(0, 0, -1).Format("2006-01-02")
	for _, c := range []struct {
		q    string
		want int
	}{
		{"?from=" + today + "&to=" + today, 1},
		{"?from=" + prev, 1},
		{"?to=" + prev, 0},
		{"?from=" + next, 0},
	} {
		h := getMetrics[historyJSON](t, f.e, f.e.human, "/metrics/tags/fresh-topic/history"+c.q)
		if len(h.Items) != c.want {
			t.Errorf("history%s = %d items, want %d", c.q, len(h.Items), c.want)
		}
	}
	h := getMetrics[historyJSON](t, f.e, f.e.human, "/metrics/tags/fresh-topic/history?from="+prev+"&to="+next)
	if h.From == nil || *h.From != prev || h.To == nil || *h.To != next {
		t.Errorf("history bounds = %v..%v", h.From, h.To)
	}

	for _, q := range []string{"?from=yesterday", "?to=2026-13-01", "?from=" + next + "&to=" + prev, "?from=2026-1-1"} {
		if r := f.e.req(t, "GET", "/metrics/tags/fresh-topic/history"+q, f.e.human, nil, nil); r.code != http.StatusUnprocessableEntity {
			t.Errorf("history%s = %d, want 422 (%s)", q, r.code, r.body)
		}
	}
	for _, ref := range []string{"no-such-tag", domain.NewIDGenerator(domain.SystemClock{}).New().String()} {
		if r := f.e.req(t, "GET", "/metrics/tags/"+ref+"/history", f.e.human, nil, nil); r.code != http.StatusNotFound {
			t.Errorf("history of %s = %d, want 404", ref, r.code)
		}
	}
	// Another user's tag is just as unknown.
	if r := f.e.req(t, "GET", "/metrics/tags/fresh-topic/history", f.e.other, nil, nil); r.code != http.StatusNotFound {
		t.Errorf("other user's history = %d, want 404", r.code)
	}
}

func TestMetricsWeakestProblems(t *testing.T) {
	f := newMetricsFixture(t)
	w := getMetrics[weakJSON](t, f.e, f.e.human, "/metrics/problems?sort=recall&limit=10")
	if len(w.Items) != 2 {
		t.Fatalf("weakest = %+v, want the two studied problems (not the unanswered one)", w.Items)
	}
	if w.Items[0].ProblemID != f.pStale.ID.String() || w.Items[1].ProblemID != f.pFresh.ID.String() {
		t.Errorf("order = %s, %s; want the decayed problem first", w.Items[0].ProblemID, w.Items[1].ProblemID)
	}
	if w.Items[0].Recall >= w.Items[1].Recall || w.Items[1].Recall < 0.9 {
		t.Errorf("recalls = %v, %v", w.Items[0].Recall, w.Items[1].Recall)
	}
	if w.Items[0].Prompt != "Stale problem" || len(w.Items[0].Tags) != 1 || w.Items[0].Tags[0] != "stale-topic" {
		t.Errorf("weakest = %+v", w.Items[0])
	}
	if one := getMetrics[weakJSON](t, f.e, f.e.human, "/metrics/problems?limit=1"); len(one.Items) != 1 || one.Items[0].ProblemID != f.pStale.ID.String() {
		t.Errorf("limit=1 = %+v", one.Items)
	}
	if def := getMetrics[weakJSON](t, f.e, f.e.human, "/metrics/problems"); len(def.Items) != 2 {
		t.Errorf("default = %+v", def.Items)
	}
	for _, q := range []string{"?sort=name", "?limit=0", "?limit=201", "?limit=x"} {
		if r := f.e.req(t, "GET", "/metrics/problems"+q, f.e.human, nil, nil); r.code != http.StatusUnprocessableEntity {
			t.Errorf("problems%s = %d, want 422 (%s)", q, r.code, r.body)
		}
	}
	// A user with nothing studied gets an empty list, not null.
	r := f.e.req(t, "GET", "/metrics/problems", f.e.other, nil, nil)
	if r.code != http.StatusOK || !strings.Contains(string(r.body), `"items":[]`) {
		t.Errorf("other user = %d %s", r.code, r.body)
	}
}

func TestMetricsScopes(t *testing.T) {
	f := newMetricsFixture(t)
	reader := f.e.idp.access(itMetricsReader, "author-sub")
	paths := []string{"/metrics/overview", "/metrics/tags", "/metrics/tags/fresh-topic/history", "/metrics/problems"}
	for _, p := range paths {
		if r := f.e.req(t, "GET", p, reader, nil, nil); r.code != http.StatusOK {
			t.Errorf("metrics:read agent GET %s = %d (%s)", p, r.code, r.body)
		}
		if r := f.e.req(t, "GET", p, f.e.human, nil, nil); r.code != http.StatusOK {
			t.Errorf("human GET %s = %d", p, r.code)
		}
		// The ordinary problems agent was never granted metrics:read.
		if r := f.e.req(t, "GET", p, f.e.agent, nil, nil); r.code != http.StatusForbidden {
			t.Errorf("agent without metrics:read GET %s = %d, want 403", p, r.code)
		}
		if r := f.e.req(t, "GET", p, "", nil, nil); r.code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d, want 401", p, r.code)
		}
	}
}

// TestMetricsOverMCP is the task's agent check: an agent holding metrics:read
// lists and calls all four metrics tools through the generated MCP server and
// gets the same figures as the REST API.
func TestMetricsOverMCP(t *testing.T) {
	f := newMetricsFixture(t)
	cs := mcpSession(t, f.e, f.e.idp.access(itMetricsReader, "author-sub"))
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, name := range []string{"metrics_overview", "metrics_tags", "metrics_tagHistory", "metrics_problems"} {
		if !have[name] {
			t.Errorf("tools/list lacks %s", name)
		}
	}

	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %s", name, err, resultText(t, res))
		}
		return resultText(t, res)
	}
	var o overviewJSON
	if err := json.Unmarshal([]byte(call("metrics_overview", map[string]any{})), &o); err != nil || o.Tags != 3 || o.CardsStudied != 2 {
		t.Errorf("metrics_overview = %+v (%v)", o, err)
	}
	var tg tagMasteryPage
	if err := json.Unmarshal([]byte(call("metrics_tags", map[string]any{"limit": 10})), &tg); err != nil || len(tg.Items) != 3 {
		t.Errorf("metrics_tags = %+v (%v)", tg, err)
	}
	var h historyJSON
	if err := json.Unmarshal([]byte(call("metrics_tagHistory", map[string]any{"tag": "fresh-topic", "from": "2000-01-01"})), &h); err != nil || h.Slug != "fresh-topic" {
		t.Errorf("metrics_tagHistory = %+v (%v)", h, err)
	}
	var w weakJSON
	if err := json.Unmarshal([]byte(call("metrics_problems", map[string]any{"sort": "recall", "limit": 1})), &w); err != nil || len(w.Items) != 1 || w.Items[0].ProblemID != f.pStale.ID.String() {
		t.Errorf("metrics_problems = %+v (%v)", w, err)
	}

	// An agent without the scope sees the API's 403 as a tool error.
	denied := mcpSession(t, f.e, f.e.agent)
	res, err := denied.CallTool(ctx, &sdk.CallToolParams{Name: "metrics_overview", Arguments: map[string]any{}})
	if err == nil && (res == nil || !res.IsError || !strings.Contains(resultText(t, res), "403")) {
		t.Errorf("agent without metrics:read: %v %+v", err, res)
	}
}
