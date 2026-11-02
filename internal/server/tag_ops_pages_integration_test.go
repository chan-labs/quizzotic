// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// formPost submits an HTML form the way the browser does: session cookie plus
// the CSRF token as a form field.
func (w *webAPI) formPost(t *testing.T, path string, form url.Values) (int, string, string) {
	t.Helper()
	form.Set("csrf_token", w.csrf)
	req, err := http.NewRequest("POST", w.e.srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: w.cookie})
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// agentCall calls the API as the tag agent client acting for the same user as
// the browser session; it may propose operations but not apply them.
func (w *webAPI) agentCall(t *testing.T, method, path string, body any, want int, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, w.e.srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.e.idp.access(itTagAgent, "web-sub"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("agent %s %s = %d, want %d (%s)", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
}

// attemptIDs lists every attempt through the API, in order.
func (w *webAPI) attemptIDs(t *testing.T) []string {
	t.Helper()
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	w.mustJSON(t, "GET", "/api/v1/attempts?limit=200", nil, http.StatusOK, &page)
	if page.NextCursor != nil {
		t.Fatal("more attempts than one page")
	}
	var ids []string
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	return ids
}

func (w *webAPI) mastery(t *testing.T) map[string]tagMasteryJSON {
	t.Helper()
	var page tagMasteryPage
	w.mustJSON(t, "GET", "/api/v1/metrics/tags?limit=200", nil, http.StatusOK, &page)
	by := map[string]tagMasteryJSON{}
	for _, it := range page.Items {
		by[it.Slug] = it
	}
	return by
}

// TestTagSplitProposedByAgentAppliedInBrowser is the M8 exit check, through
// the real server: an agent proposes a split over the API, the human opens the
// approvals queue, sees the preview, applies it with the page's own form, and
// no attempt is added, changed or lost while mastery follows the new tags.
func TestTagSplitProposedByAgentAppliedInBrowser(t *testing.T) {
	e := newITEnv(t)
	web := e.webSession(t)

	tag := func(name string) {
		web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": name}, http.StatusCreated, nil)
	}
	tag("Networking")
	tag("Security")
	problem := func(prompt string) problems.View {
		body := openBody(prompt)
		body["status"] = "live"
		var p problems.View
		web.mustJSON(t, "POST", "/api/v1/problems", body, http.StatusCreated, &p)
		web.mustJSON(t, "PUT", "/api/v1/problems/"+p.ID.String()+"/tags", map[string]any{"tags": []string{"networking"}}, http.StatusOK, nil)
		return p
	}
	tcp, tls, dns := problem("What is TCP?"), problem("What is TLS?"), problem("What is DNS?")
	for _, p := range []problems.View{tcp, tls} {
		web.mustJSON(t, "POST", "/api/v1/attempts", attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3}), http.StatusCreated, nil)
	}
	attemptsBefore := web.attemptIDs(t)
	if len(attemptsBefore) != 2 {
		t.Fatalf("attempts before = %v", attemptsBefore)
	}
	before := web.mastery(t)
	if n := before["networking"]; n.CardsTotal != 3 || n.CardsSeen != 2 || n.Reviews != 2 {
		t.Fatalf("networking mastery before = %+v", n)
	}

	// The agent reads the tag's problems and proposes the split.
	var op opJSON
	web.agentCall(t, "POST", "/api/v1/tag-operations", map[string]any{"kind": "split", "payload": map[string]any{
		"source": "networking",
		"targets": []map[string]any{
			{"new": map[string]string{"slug": "transport", "name": "Transport"}},
			{"ref": "security"},
		},
		"assignments":   map[string][]string{tcp.ID.String(): {"transport"}, tls.ID.String(): {"security"}, dns.ID.String(): {"security"}},
		"retire_source": true,
	}}, http.StatusCreated, &op)
	if op.Status != "proposed" || op.ProposedBy.Kind != "agent" {
		t.Fatalf("proposal = %+v", op)
	}
	web.agentCall(t, "POST", "/api/v1/tag-operations/"+op.ID+"/apply", nil, http.StatusForbidden, nil)

	// The human opens the queue and sees the agent's proposal with its preview.
	code, queue := web.page(t, "/tag-operations")
	if code != http.StatusOK {
		t.Fatalf("GET /tag-operations = %d (%s)", code, queue)
	}
	for _, want := range []string{
		"Waiting for approval", "Split Networking into Transport (new), Security", "source retired",
		"Proposed by agent " + itTagAgent, "3 problems change tags",
		`action="/tag-operations/` + op.ID + `/apply"`, `action="/tag-operations/` + op.ID + `/reject"`, "Transport",
	} {
		if !strings.Contains(queue, want) {
			t.Errorf("queue lacks %q:\n%s", want, queue)
		}
	}
	if m := web.mastery(t)["transport"]; m.TagID != "" {
		t.Fatalf("proposal created the tag already: %+v", m)
	}
	if code, loc, body := web.formPost(t, "/tag-operations/"+op.ID+"/apply", url.Values{}); code != http.StatusSeeOther || loc != "/tag-operations?done=apply" {
		t.Fatalf("apply from the browser = %d %q %s", code, loc, body)
	}

	// Applied: the browser was sent back to the queue, which now offers Revert.
	_, applied := web.page(t, "/tag-operations?done=apply")
	if !strings.Contains(applied, "Operation applied.") || !strings.Contains(applied, `action="/tag-operations/`+op.ID+`/revert"`) {
		t.Errorf("applied page:\n%s", applied)
	}
	var got opJSON
	web.mustJSON(t, "GET", "/api/v1/tag-operations/"+op.ID, nil, http.StatusOK, &got)
	if got.Status != "applied" || got.DecidedBy == nil || got.DecidedBy.Kind != "human" || got.ProposedBy.Kind != "agent" {
		t.Fatalf("applied op = %+v", got)
	}

	// Attempt history is untouched; mastery now follows the new tags.
	if after := web.attemptIDs(t); !slices.Equal(after, attemptsBefore) {
		t.Fatalf("attempts changed by the split: before %v after %v", attemptsBefore, after)
	}
	after := web.mastery(t)
	if m := after["transport"]; m.CardsTotal != 1 || m.CardsSeen != 1 || m.Reviews != 1 || m.Recall == nil {
		t.Errorf("transport mastery = %+v", m)
	}
	if m := after["security"]; m.CardsTotal != 2 || m.CardsSeen != 1 || m.Reviews != 1 || m.Recall == nil {
		t.Errorf("security mastery = %+v", m)
	}
	if reviews := after["transport"].Reviews + after["security"].Reviews; reviews != len(attemptsBefore) {
		t.Errorf("reviews across the targets = %d, attempts = %d", reviews, len(attemptsBefore))
	}

	// A forged request without the CSRF token cannot revert it.
	req, _ := http.NewRequest("POST", e.srv.URL+"/tag-operations/"+op.ID+"/revert", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: web.cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("revert without CSRF = %d, want 403", resp.StatusCode)
	}

	// Revert from the browser restores the tags and still leaves history alone.
	if code, loc, body := web.formPost(t, "/tag-operations/"+op.ID+"/revert", url.Values{}); code != http.StatusSeeOther || loc != "/tag-operations?done=revert" {
		t.Fatalf("revert = %d %q %s", code, loc, body)
	}
	if again := web.attemptIDs(t); !slices.Equal(again, attemptsBefore) {
		t.Fatalf("attempts changed by the revert: %v", again)
	}
	if n := web.mastery(t)["networking"]; n.CardsTotal != 3 || n.CardsSeen != 2 || n.Reviews != 2 {
		t.Errorf("networking mastery after revert = %+v", n)
	}
	if code, _, body := web.formPost(t, "/tag-operations/"+op.ID+"/revert", url.Values{}); code != http.StatusConflict || !strings.Contains(body, "already been reverted") {
		t.Errorf("second revert = %d (%s)", code, body)
	}
}

// The Tags page links to the tag action pages once they are installed.
func TestTagsPageLinksToTagActions(t *testing.T) {
	e := newITEnv(t)
	web := e.webSession(t)
	web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": "Alpha"}, http.StatusCreated, nil)
	_, body := web.page(t, "/tags")
	for _, want := range []string{`href="/tag-operations/rename"`, `href="/tag-operations/merge"`, `href="/tag-operations/split"`, `href="/tag-operations"`} {
		if !strings.Contains(body, want) {
			t.Errorf("tags page lacks %s", want)
		}
	}
	for _, p := range []string{"/tag-operations", "/tag-operations/rename", "/tag-operations/merge", "/tag-operations/split"} {
		if r := e.do(t, "GET", p, nil, ""); r.code != http.StatusSeeOther || !strings.HasPrefix(r.header.Get("Location"), "/auth/login") {
			t.Errorf("anonymous GET %s = %d", p, r.code)
		}
		if code, _ := web.page(t, p); code != http.StatusOK {
			t.Errorf("GET %s = %d", p, code)
		}
	}
}
