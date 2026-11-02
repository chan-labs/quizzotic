// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// opReply is the part of a tag operation these tests read.
type opReply struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Through server.Run: a browser session of one user gets 404 for another
// user's tag detail page and tag operation pages, and nothing of the other
// user's changes.
func TestWebTagPagesAreOwnerScoped(t *testing.T) {
	e := newRunEnv(t)
	web := e.itEnv.webSession(t) // the "web-sub" user
	// "author-sub" (e.human) is the other owner.
	var secret struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if r := e.req(t, "POST", "/tags", e.human, map[string]any{"name": "Secret"}, nil); r.code != http.StatusCreated {
		t.Fatalf("create tag = %d (%s)", r.code, r.body)
	} else {
		secret = decodeReply[struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}](t, r)
	}
	var op opReply
	r := e.req(t, "POST", "/tag-operations", e.human, map[string]any{"kind": "rename",
		"payload": map[string]any{"tag": secret.ID, "name": "Renamed"}}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("propose = %d (%s)", r.code, r.body)
	}
	op = decodeReply[opReply](t, r)

	// A control: the viewer's own tag is served by the same pages.
	var mine struct {
		ID string `json:"id"`
	}
	web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": "Mine"}, http.StatusCreated, &mine)
	if code, _ := web.page(t, "/tags/"+mine.ID); code != http.StatusOK {
		t.Fatalf("own tag page = %d, want 200", code)
	}
	if code, _ := web.page(t, "/tag-operations/split?source="+mine.ID); code != http.StatusOK {
		t.Fatalf("own split wizard = %d, want 200", code)
	}

	for _, p := range []string{
		"/tags/" + secret.ID,
		"/tags/" + secret.Slug,
		"/tag-operations/split?source=" + secret.ID,
		"/tag-operations/split?source=" + secret.Slug + "&step=assign",
	} {
		code, body := web.page(t, p)
		if code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, code)
		}
		if strings.Contains(body, "Secret") || strings.Contains(body, secret.ID) {
			t.Errorf("GET %s leaks the other user's tag:\n%s", p, body)
		}
	}
	// The other user's operation is invisible and cannot be decided.
	for _, verb := range []string{"apply", "reject", "revert"} {
		code, _, body := web.formPost(t, "/tag-operations/"+op.ID+"/"+verb, url.Values{})
		if code != http.StatusNotFound {
			t.Errorf("%s of another user's operation = %d, want 404 (%s)", verb, code, body)
		}
	}
	// Proposing against another user's tag from the forms is refused too.
	if code, _, _ := web.formPost(t, "/tag-operations/rename", url.Values{"tag": {secret.ID}, "name": {"Hijacked"}}); code != http.StatusNotFound && code != http.StatusUnprocessableEntity {
		t.Errorf("rename of another user's tag = %d, want 404 or 422", code)
	}
	if code, _, _ := web.formPost(t, "/tag-operations/merge", url.Values{"sources": {secret.ID}, "target": {mine.ID}}); code != http.StatusNotFound && code != http.StatusUnprocessableEntity {
		t.Errorf("merge of another user's tag = %d, want 404 or 422", code)
	}

	// Nothing changed for the owner.
	if r := e.req(t, "GET", "/tag-operations/"+op.ID, e.human, nil, nil); r.code != http.StatusOK ||
		decodeReply[opReply](t, r).Status != "proposed" {
		t.Errorf("owner's operation after the attempts = %d %s, want still proposed", r.code, r.body)
	}
	var ops struct {
		Items []opReply `json:"items"`
	}
	if r := e.req(t, "GET", "/tag-operations", e.human, nil, nil); r.code != http.StatusOK {
		t.Fatalf("list = %d", r.code)
	} else {
		ops = decodeReply[struct {
			Items []opReply `json:"items"`
		}](t, r)
	}
	if len(ops.Items) != 1 {
		t.Errorf("owner has %d operations, want only the one proposed", len(ops.Items))
	}
}

// A tag named like markup is shown as text on every page that prints it.
func TestWebTagPagesEscapeTagNames(t *testing.T) {
	e := newRunEnv(t)
	web := e.itEnv.webSession(t)
	const name = `<script>alert(1)</script>`
	var tag struct {
		ID string `json:"id"`
	}
	web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": name}, http.StatusCreated, &tag)
	web.mustJSON(t, "POST", "/api/v1/tags", map[string]any{"name": "Other"}, http.StatusCreated, nil)
	// A pending operation on the tag, so the approvals queue prints it.
	web.mustJSON(t, "POST", "/api/v1/tag-operations", map[string]any{"kind": "rename",
		"payload": map[string]any{"tag": tag.ID, "name": `<img src=x onerror=alert(2)>`}}, http.StatusCreated, nil)

	for _, p := range []string{
		"/tags",
		"/tags/" + tag.ID,
		"/stats",
		"/tag-operations",
		"/tag-operations/rename?tag=" + url.QueryEscape(name),
		"/tag-operations/merge",
		"/tag-operations/split",
		"/tag-operations/split?source=" + tag.ID,
	} {
		code, body := web.page(t, p)
		if code != http.StatusOK {
			t.Errorf("GET %s = %d", p, code)
			continue
		}
		if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, "<img src=x") {
			t.Errorf("GET %s prints a tag name unescaped:\n%s", p, body)
		}
	}
	// The pages that list tags do print the name, as text.
	for _, p := range []string{"/tags", "/tags/" + tag.ID, "/tag-operations/merge"} {
		if _, body := web.page(t, p); !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("GET %s does not show the name as escaped text:\n%s", p, body)
		}
	}
}
