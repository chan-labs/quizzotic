// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
)

func todayInt(t *testing.T, body, id string) int {
	t.Helper()
	m := regexp.MustCompile(`id="` + id + `">(\d+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("Today page has no #%s:\n%s", id, body)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Through server.Run: the Today page is installed with the production
// builder (the one that reads tag mastery), counts the due cards of tagged
// problems and the day's budgets, and its Start review form opens a session.
func TestRunTodayPageCountsTaggedCardsAndStartsReview(t *testing.T) {
	e := newRunEnv(t)
	ctx := context.Background()
	web := e.itEnv.webSession(t)
	uid, _ := e.userFor(t, "web-sub")
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatal(err)
	}

	var tag struct {
		ID string `json:"id"`
	}
	web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": "Topic"}, http.StatusCreated, &tag)
	var ps []problems.View
	for _, prompt := range []string{"one", "two", "three"} {
		body := openBody(prompt)
		body["status"] = "live"
		var p problems.View
		web.mustJSON(t, "POST", "/api/v1/problems", body, http.StatusCreated, &p)
		web.mustJSON(t, "PUT", "/api/v1/problems/"+p.ID.String()+"/tags", map[string]any{"tags": []string{tag.ID}}, http.StatusOK, nil)
		ps = append(ps, p)
	}

	code, body := web.page(t, "/")
	if code != http.StatusOK {
		t.Fatalf("GET / = %d (%s)", code, body)
	}
	if todayInt(t, body, "due") != 0 || todayInt(t, body, "new-left") != 20 || todayInt(t, body, "reviews-left") != 200 {
		t.Fatalf("fresh Today page:\n%s", body)
	}

	// Answer two of the three; the answers spend two of the day's new cards.
	for _, p := range ps[:2] {
		web.mustJSON(t, "POST", "/api/v1/attempts", attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3}), http.StatusCreated, nil)
	}
	// The server runs on the real clock, so bring one of the two cards due by
	// moving its due date back and push the other far out.
	c, err := e.st.Cards().Get(ctx, owner, ps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Due = time.Now().Add(-time.Hour)
	if err := e.st.Cards().Put(ctx, owner, c); err != nil {
		t.Fatal(err)
	}
	c, err = e.st.Cards().Get(ctx, owner, ps[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Due = time.Now().Add(30 * 24 * time.Hour)
	if err := e.st.Cards().Put(ctx, owner, c); err != nil {
		t.Fatal(err)
	}

	_, body = web.page(t, "/")
	if got := todayInt(t, body, "due"); got != 1 {
		t.Errorf("due = %d, want 1", got)
	}
	if got := todayInt(t, body, "new-left"); got != 18 {
		t.Errorf("new left = %d, want 18", got)
	}
	if got := todayInt(t, body, "reviews-left"); got != 200 {
		t.Errorf("reviews left = %d, want 200 (first answers are new cards, not reviews)", got)
	}

	// Start review, from the page's own form.
	code, loc, out := web.formPost(t, "/review/start", url.Values{})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/review/") {
		t.Fatalf("Start review = %d %q (%s)", code, loc, out)
	}
	if code, page := web.page(t, loc); code != http.StatusOK || !strings.Contains(page, "one") {
		t.Errorf("review page %s = %d:\n%s", loc, code, page)
	}
}
