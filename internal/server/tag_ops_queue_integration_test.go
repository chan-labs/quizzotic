// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/problems"
)

type opPageJSON struct {
	Items      []opJSON `json:"items"`
	NextCursor *string  `json:"next_cursor"`
}

// The approvals queue shows what an applied operation did and lists the
// rejected ones, and the API returns previews and results from one list call.
// It runs through server.Run, the way `quizzotic serve` starts the server.
func TestTagOpsQueueShowsResultsAndRejected(t *testing.T) {
	idp := newFakeIDP(t)
	pe, _ := startRunEnvWith(t, idp, filepath.Join(t.TempDir(), "q.db"), nil, func(cfg *config.Config) {
		cfg.Auth.Issuers[0].Clients = append(cfg.Auth.Issuers[0].Clients,
			config.Client{ID: itTagAgent, Kind: "agent", AllowedScopes: []string{"tags:read", "tags:write", "problems:read"}})
	})
	e := pe.itEnv
	web := e.webSession(t)

	tag := func(name string) string {
		var tg struct {
			ID string `json:"id"`
		}
		web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": name}, http.StatusCreated, &tg)
		return tg.ID
	}
	tag("Alpha")
	tag("Beta")
	// A hostile name: pages must escape it. Tags are referenced by ID.
	evilID := tag("<b>Evil</b>")
	for i, p := range []string{"One?", "Two?"} {
		body := openBody(p)
		body["status"] = "live"
		var v problems.View
		web.mustJSON(t, "POST", "/api/v1/problems", body, http.StatusCreated, &v)
		slug := []string{"alpha", "beta"}[i]
		web.mustJSON(t, "PUT", "/api/v1/problems/"+v.ID.String()+"/tags", map[string]any{"tags": []string{slug}}, http.StatusOK, nil)
	}

	propose := func(kind string, payload map[string]any) opJSON {
		var op opJSON
		web.agentCall(t, "POST", "/api/v1/tag-operations", map[string]any{"kind": kind, "payload": payload}, http.StatusCreated, &op)
		return op
	}
	merge := propose("merge", map[string]any{"sources": []string{"alpha"}, "target": "beta"})
	rename := propose("rename", map[string]any{"tag": "beta", "name": "Beta Renamed"})
	evil := propose("rename", map[string]any{"tag": evilID, "name": "<i>Worse</i>"})
	pending := propose("rename", map[string]any{"tag": evilID, "slug": "evil-two"})

	post := func(op opJSON, verb string) {
		t.Helper()
		if code, loc, body := web.formPost(t, "/tag-operations/"+op.ID+"/"+verb, url.Values{}); code != http.StatusSeeOther || loc != "/tag-operations?done="+verb {
			t.Fatalf("%s %s = %d %q %s", verb, op.ID, code, loc, body)
		}
	}
	post(merge, "apply")
	post(rename, "apply")
	post(evil, "reject")

	// One list call carries the preview of the proposed operation and the
	// recorded results of the applied ones.
	var all opPageJSON
	web.mustJSON(t, "GET", "/api/v1/tag-operations?limit=100", nil, http.StatusOK, &all)
	if len(all.Items) != 4 {
		t.Fatalf("list returned %d operations, want 4", len(all.Items))
	}
	by := map[string]opJSON{}
	for _, op := range all.Items {
		by[op.ID] = op
	}
	if m := by[merge.ID]; m.Status != "applied" || m.Result == nil || len(m.Result.AffectedProblemIDs) != 1 ||
		m.Result.counts()["alpha"] != [2]int{1, 0} || m.Result.counts()["beta"] != [2]int{1, 2} {
		t.Errorf("merge in list = %+v result %+v", m, m.Result)
	}
	if m := by[pending.ID]; m.Status != "proposed" || m.Preview == nil || m.Result != nil {
		t.Errorf("pending in list = %+v", m)
	}
	if m := by[evil.ID]; m.Status != "rejected" || m.Result != nil || m.Preview != nil {
		t.Errorf("rejected in list = %+v", m)
	}
	// Get returns the same result for an applied operation.
	var got opJSON
	web.mustJSON(t, "GET", "/api/v1/tag-operations/"+merge.ID, nil, http.StatusOK, &got)
	if got.Result == nil || got.Result.counts()["beta"] != [2]int{1, 2} || got.Preview != nil {
		t.Errorf("get applied = %+v", got)
	}

	// The queue page shows the results and the rejected operation, escaped.
	code, queue := web.page(t, "/tag-operations")
	if code != http.StatusOK {
		t.Fatalf("GET /tag-operations = %d", code)
	}
	for _, want := range []string{
		"<h2 id=\"rejected-h\">Rejected</h2>", "Rejected by", "1 problem changed tags",
		"Result of Merge Alpha into Beta", `action="/tag-operations/` + pending.ID + `/apply"`,
		"&lt;b&gt;Evil&lt;/b&gt;",
	} {
		if !strings.Contains(queue, want) {
			t.Errorf("queue lacks %q:\n%s", want, queue)
		}
	}
	if strings.Contains(queue, "<b>Evil</b>") || strings.Contains(queue, "<i>Worse</i>") {
		t.Errorf("queue contains unescaped tag names:\n%s", queue)
	}
	_, rejected, _ := strings.Cut(queue, `id="rejected-h"`)
	if !strings.Contains(rejected, `id="op-`+evil.ID+`"`) || strings.Contains(rejected, merge.ID) || strings.Contains(rejected, pending.ID) {
		t.Errorf("rejected section is wrong:\n%s", rejected)
	}

	// Another user sees none of it.
	other := idp.access("quizzotic-cli", "other-sub")
	r := pe.req(t, "GET", "/tag-operations?limit=100", other, nil, nil)
	if r.code != http.StatusOK || strings.Contains(string(r.body), merge.ID) || strings.Contains(string(r.body), "Alpha") {
		t.Errorf("other owner list = %d %s", r.code, r.body)
	}
	if r := pe.req(t, "GET", "/tag-operations/"+merge.ID, other, nil, nil); r.code != http.StatusNotFound {
		t.Errorf("other owner get = %d", r.code)
	}
}
