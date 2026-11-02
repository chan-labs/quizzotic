// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
)

// webAPI is a browser session: it reads pages and calls the API with the
// session cookie and its CSRF token, the way the website's own requests do.
type webAPI struct {
	e      *itEnv
	cookie string
	csrf   string
}

func (e *itEnv) webSession(t *testing.T) *webAPI {
	t.Helper()
	cookie := e.webLogin(t)
	sum := sha256.Sum256([]byte(cookie))
	ws, err := e.st.WebSessions().GetByTokenHash(context.Background(), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return &webAPI{e: e, cookie: cookie, csrf: ws.CSRFToken}
}

func (w *webAPI) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, w.e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-CSRF-Token", w.csrf)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: w.cookie})
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (w *webAPI) mustJSON(t *testing.T, method, path string, body any, want int, out any) {
	t.Helper()
	code, b := w.call(t, method, path, body)
	if code != want {
		t.Fatalf("%s %s = %d, want %d (%s)", method, path, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *webAPI) page(t *testing.T, path string) (int, string) {
	t.Helper()
	r := w.e.do(t, "GET", path, nil, w.cookie)
	return r.code, string(r.body)
}

// TestMetricsAndTagsPages drives the real server: a browser session reads the
// Metrics and Tags pages over data answered through the API, and finds the
// recall values and history points the metrics endpoints report.
func TestMetricsAndTagsPages(t *testing.T) {
	e := newITEnv(t)
	ctx := context.Background()
	web := e.webSession(t)
	uid, _ := e.userFor(t, "web-sub")
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatal(err)
	}

	tag := func(name string) string {
		var tg tagJSON
		web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": name}, http.StatusCreated, &tg)
		return tg.ID
	}
	problem := func(prompt, tagID string) problems.View {
		body := openBody(prompt)
		body["status"] = "live"
		var p problems.View
		web.mustJSON(t, "POST", "/api/v1/problems", body, http.StatusCreated, &p)
		web.mustJSON(t, "PUT", "/api/v1/problems/"+p.ID.String()+"/tags", map[string]any{"tags": []string{tagID}}, http.StatusOK, nil)
		return p
	}
	fresh, stale, untouched := tag("fresh-topic"), tag("stale-topic"), tag("untouched-topic")
	pFresh, pStale := problem("Fresh problem", fresh), problem("Stale problem", stale)
	problem("Never answered", untouched)
	for _, p := range []problems.View{pFresh, pStale} {
		web.mustJSON(t, "POST", "/api/v1/attempts", attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3}), http.StatusCreated, nil)
	}
	// Age the stale card so its recall is far below the fresh one's.
	c, err := e.st.Cards().Get(ctx, owner, pStale.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.LastReview, c.Stability = time.Now().Add(-365*24*time.Hour), 1
	if err := e.st.Cards().Put(ctx, owner, c); err != nil {
		t.Fatal(err)
	}

	// History: today's snapshot from the daily job plus three earlier days.
	snap := store.NewMastery(store.DefaultCards(), domain.SystemClock{}).Snapshotter(e.st, nil)
	if err := snap.SnapshotUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	freshID, _ := domain.ParseID(fresh)
	var overview overviewJSON
	web.mustJSON(t, "GET", "/api/v1/metrics/overview", nil, http.StatusOK, &overview)
	today, _ := time.Parse("2006-01-02", overview.StudyDay)
	for i, recall := range []float64{0.5, 0.625, 0.75} {
		day := today.AddDate(0, 0, -(len(overview.Activity)/10 + 3 - i)).Format("2006-01-02")
		if err := e.st.MasterySnapshots().Put(ctx, owner, metrics.MasterySnapshot{UserID: owner, TagID: freshID, Date: day, Recall: recall, CardsSeen: i + 1}); err != nil {
			t.Fatal(err)
		}
	}

	var tags tagMasteryPage
	web.mustJSON(t, "GET", "/api/v1/metrics/tags", nil, http.StatusOK, &tags)
	var history historyJSON
	web.mustJSON(t, "GET", "/api/v1/metrics/tags/"+fresh+"/history", nil, http.StatusOK, &history)
	if len(history.Items) != 4 {
		t.Fatalf("seeded history = %+v, want 4 snapshots", history.Items)
	}
	byName := map[string]tagMasteryJSON{}
	for _, it := range tags.Items {
		byName[it.Name] = it
	}

	t.Run("anonymous browsers are sent to log in", func(t *testing.T) {
		for _, p := range []string{"/stats", "/tags", "/tags/" + fresh} {
			r := e.do(t, "GET", p, nil, "")
			if r.code != http.StatusSeeOther || !strings.HasPrefix(r.header.Get("Location"), "/auth/login") {
				t.Errorf("GET %s anonymous = %d -> %q", p, r.code, r.header.Get("Location"))
			}
		}
	})

	t.Run("tags page shows each tag's recall", func(t *testing.T) {
		code, body := web.page(t, "/tags")
		if code != http.StatusOK {
			t.Fatalf("GET /tags = %d (%s)", code, body)
		}
		for name, it := range byName {
			row := tagRowHTML(t, body, it.TagID)
			if !strings.Contains(row, name) {
				t.Errorf("row of %s lacks its name: %s", name, row)
			}
			want := "–"
			if it.Recall != nil {
				want = fmt.Sprintf("%.0f%%", *it.Recall*100)
			}
			if got := regexp.MustCompile(`class="recall-value">([^<]*)<`).FindStringSubmatch(row); got == nil || got[1] != want {
				t.Errorf("recall of %s = %v, want %s (api recall %v)", name, got, want, it.Recall)
			}
		}
		if f, s := byName["fresh-topic"], byName["stale-topic"]; *f.Recall < 0.9 || *s.Recall > 0.5 {
			t.Fatalf("fixture recalls = %v / %v", *f.Recall, *s.Recall)
		}
		// Weakest studied tag first, never-studied tags last.
		if i, j, k := strings.Index(body, "stale-topic"), strings.Index(body, "fresh-topic"), strings.Index(body, "untouched-topic"); !(i < j && j < k) {
			t.Errorf("tag order = stale %d, fresh %d, untouched %d, want weakest first", i, j, k)
		}
		for _, want := range []string{`href="/tags/` + fresh + `"`, `aria-current="page"`, `<nav`, `/static/metrics.css`} {
			if !strings.Contains(body, want) {
				t.Errorf("tags page lacks %q", want)
			}
		}
		if strings.Contains(body, "<script") {
			t.Error("tags page needs no script")
		}
	})

	t.Run("tag page draws the history", func(t *testing.T) {
		code, body := web.page(t, "/tags/"+fresh)
		if code != http.StatusOK {
			t.Fatalf("GET /tags/%s = %d (%s)", fresh, code, body)
		}
		if !strings.Contains(body, "<h1>fresh-topic</h1>") || !strings.Contains(body, "<svg") || !strings.Contains(body, `role="img"`) ||
			!strings.Contains(body, `aria-labelledby="history-title history-desc"`) {
			t.Errorf("tag page lacks the heading or an accessible chart: %s", body)
		}
		if n := strings.Count(body, `<circle class="dot"`); n != len(history.Items) {
			t.Errorf("chart has %d points, want %d", n, len(history.Items))
		}
		if !strings.Contains(body, `<path class="line"`) {
			t.Error("chart has no line")
		}
		for _, p := range history.Items {
			tip := fmt.Sprintf("%s: recall %.1f%%, %d cards seen", p.Date, p.Recall*100, p.CardsSeen)
			if !strings.Contains(body, "<title>"+tip+"</title>") {
				t.Errorf("point %q missing from the chart", tip)
			}
			row := fmt.Sprintf("<th scope=\"row\">%s</th><td>%.1f%%</td><td>%d</td>", p.Date, p.Recall*100, p.CardsSeen)
			if !strings.Contains(body, row) {
				t.Errorf("data table lacks %q", row)
			}
		}
		if f := byName["fresh-topic"]; !strings.Contains(body, fmt.Sprintf(`<dd id="recall">%.0f%%</dd>`, *f.Recall*100)) {
			t.Errorf("tag page lacks the current recall %v", *f.Recall)
		}
		// A tag with no snapshots says so rather than drawing an empty chart.
		_, empty := web.page(t, "/tags/"+untouched)
		if !strings.Contains(empty, "No history yet") || strings.Contains(empty, "<svg") {
			t.Errorf("empty history page = %s", empty)
		}
		// By slug too; an unknown tag is a 404.
		if code, _ := web.page(t, "/tags/fresh-topic"); code != http.StatusOK {
			t.Errorf("GET /tags/fresh-topic = %d", code)
		}
		if code, _ := web.page(t, "/tags/no-such-tag"); code != http.StatusNotFound {
			t.Errorf("unknown tag = %d, want 404", code)
		}
	})

	t.Run("metrics page shows the overview, forecast and activity", func(t *testing.T) {
		code, body := web.page(t, "/stats")
		if code != http.StatusOK {
			t.Fatalf("GET /stats = %d (%s)", code, body)
		}
		wantRecall := fmt.Sprintf(`<dd id="recall">%.0f%%</dd>`, *overview.Recall*100)
		if !strings.Contains(body, wantRecall) {
			t.Errorf("metrics page lacks %s", wantRecall)
		}
		if want := fmt.Sprintf(`<dd id="tags-studied">%d <span`, overview.TagsStudied); !strings.Contains(body, want) {
			t.Errorf("metrics page lacks %s", want)
		}
		for _, d := range overview.DueLoad {
			if !strings.Contains(body, fmt.Sprintf("<title>%s: %d cards due</title>", d.Date, d.Cards)) {
				t.Errorf("forecast lacks %s with %d cards", d.Date, d.Cards)
			}
		}
		last := overview.Activity[len(overview.Activity)-1]
		if want := fmt.Sprintf("%s: %d new cards", last.Date, last.NewCards); last.NewCards != 2 || !strings.Contains(body, "<title>"+want+"</title>") {
			t.Errorf("activity lacks %q (today = %+v)", want, last)
		}
		if n := strings.Count(body, `class="bar s0"`); n != len(overview.DueLoad)+len(overview.Activity) {
			t.Errorf("series-0 bars = %d, want %d", n, len(overview.DueLoad)+len(overview.Activity))
		}
		for _, want := range []string{`aria-labelledby="due-title due-desc"`, `aria-labelledby="activity-title activity-desc"`, "Show data as a table"} {
			if !strings.Contains(body, want) {
				t.Errorf("metrics page lacks %q", want)
			}
		}
		if strings.Contains(body, "<script") {
			t.Error("metrics page needs no script")
		}
	})
}

// tagRowHTML is the table row of the tag with the given ID.
func tagRowHTML(t *testing.T, body, id string) string {
	t.Helper()
	i := strings.Index(body, `href="/tags/`+id+`"`)
	if i < 0 {
		t.Fatalf("no row for tag %s in %s", id, body)
	}
	end := strings.Index(body[i:], "</tr>")
	return body[i : i+end]
}
