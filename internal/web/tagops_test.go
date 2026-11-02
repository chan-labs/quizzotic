// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func (e *probEnv) tag(name string) tags.Tag {
	e.t.Helper()
	tg, err := e.tags.Create(context.Background(), e.owner, tags.CreateInput{Name: name})
	if err != nil {
		e.t.Fatal(err)
	}
	return tg
}

// problem creates a live problem carrying the given tag slugs.
func (e *probEnv) problem(prompt string, slugs ...string) domain.ID {
	e.t.Helper()
	f := mcqForm()
	f.Set("prompt", prompt)
	id := e.create(f)
	if _, err := e.tags.SetProblemTags(context.Background(), e.owner, id, slugs); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *probEnv) tagSlugs(id domain.ID) []string {
	e.t.Helper()
	ts, err := e.tags.ProblemTags(context.Background(), e.owner, id)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, t := range ts {
		out = append(out, t.Slug)
	}
	return out
}

func (e *probEnv) agentPropose(kind tags.OpKind, payload any) tags.OpDetail {
	e.t.Helper()
	raw, _ := json.Marshal(payload)
	agent := domain.Actor{Kind: domain.ActorAgent, UserID: e.owner, ClientID: "tagger"}
	d, err := e.tags.Propose(context.Background(), e.owner, agent, tags.ProposeInput{Kind: kind, Payload: raw})
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

var opIDRe = regexp.MustCompile(`proposed=([0-9a-z-]+)`)

func (e *probEnv) proposedID(loc string) string {
	e.t.Helper()
	m := opIDRe.FindStringSubmatch(loc)
	if m == nil || !strings.HasPrefix(loc, "/tag-operations?") {
		e.t.Fatalf("Location = %q, want the queue with proposed=", loc)
	}
	return m[1]
}

func TestTagOpsPagesNeedLoginAndCSRF(t *testing.T) {
	e := newProbEnv(t)
	for _, p := range []string{"/tag-operations", "/tag-operations/rename", "/tag-operations/merge", "/tag-operations/split"} {
		rec := get(e.mux, p, false)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
			t.Errorf("anonymous GET %s = %d", p, rec.Code)
		}
		if rec := get(e.mux, p, true); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
	a := e.tag("Alpha")
	e.tag("Beta")
	for _, p := range []string{"/tag-operations/rename", "/tag-operations/merge", "/tag-operations/split"} {
		if rec := e.do("POST", p, url.Values{"tag": {a.Slug}, "name": {"X"}}, false); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF = %d, want 403", p, rec.Code)
		}
	}
	op := e.agentPropose(tags.OpRename, tags.RenameInput{Tag: a.Slug, Name: ptr("Alpha 2")})
	for _, verb := range []string{"apply", "reject", "revert"} {
		if rec := e.do("POST", "/tag-operations/"+op.Operation.ID.String()+"/"+verb, url.Values{}, false); rec.Code != http.StatusForbidden {
			t.Errorf("%s without CSRF = %d, want 403", verb, rec.Code)
		}
	}
	if d, _ := e.tags.GetOperation(context.Background(), e.owner, op.Operation.ID); d.Operation.Status != tags.OpProposed {
		t.Errorf("operation status %s after forged requests", d.Operation.Status)
	}
}

func ptr(s string) *string { return &s }

func TestTagRenameProposeThenApply(t *testing.T) {
	e := newProbEnv(t)
	a := e.tag("Alpha")
	p := e.problem("one", a.Slug)

	form := get(e.mux, "/tag-operations/rename?tag="+a.Slug, true).Body.String()
	for _, want := range []string{`name="csrf_token" value="` + testCSRF + `"`, `name="name"`, `name="slug"`, `value="` + a.ID.String() + `" selected`} {
		if !strings.Contains(form, want) {
			t.Errorf("rename form lacks %q", want)
		}
	}

	// Nothing to change is a validation error shown on the form.
	rec := e.do("POST", "/tag-operations/rename", url.Values{"tag": {a.ID.String()}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `role="alert"`) {
		t.Fatalf("empty rename = %d\n%s", rec.Code, rec.Body)
	}

	rec = e.do("POST", "/tag-operations/rename", url.Values{"tag": {a.ID.String()}, "name": {"Alpha Prime"}, "slug": {"alpha-prime"}}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d\n%s", rec.Code, rec.Body)
	}
	id := e.proposedID(rec.Header().Get("Location"))
	if got, _ := e.tags.Get(context.Background(), e.owner, a.ID.String()); got.Slug != a.Slug {
		t.Fatal("proposing renamed the tag")
	}

	queue := get(e.mux, rec.Header().Get("Location"), true).Body.String()
	for _, want := range []string{"Waiting for approval", "Rename Alpha", "alpha-prime", "/tag-operations/" + id + "/apply", "/tag-operations/" + id + "/reject", "Operation proposed."} {
		if !strings.Contains(queue, want) {
			t.Errorf("queue lacks %q:\n%s", want, queue)
		}
	}

	rec = e.do("POST", "/tag-operations/"+id+"/apply", url.Values{}, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tag-operations?done=apply" {
		t.Fatalf("apply = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := e.tagSlugs(p); len(got) != 1 || got[0] != "alpha-prime" {
		t.Errorf("problem tags after rename = %v", got)
	}
	done := get(e.mux, "/tag-operations?done=apply", true).Body.String()
	if !strings.Contains(done, "Operation applied.") || !strings.Contains(done, "/tag-operations/"+id+"/revert") {
		t.Errorf("applied list lacks revert:\n%s", done)
	}

	// A second apply is a conflict, shown as a message.
	rec = e.do("POST", "/tag-operations/"+id+"/apply", url.Values{}, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already been decided") {
		t.Errorf("second apply = %d\n%s", rec.Code, rec.Body)
	}

	// A taken slug is reported on the form.
	e.tag("Beta")
	rec = e.do("POST", "/tag-operations/rename", url.Values{"tag": {"alpha-prime"}, "slug": {"beta"}}, true)
	if rec.Code < 400 || rec.Code >= 500 || !strings.Contains(rec.Body.String(), `role="alert"`) {
		t.Errorf("taken slug = %d\n%s", rec.Code, rec.Body)
	}
}

func TestTagMergeProposeApplyRevert(t *testing.T) {
	e := newProbEnv(t)
	a, b, c := e.tag("Alpha"), e.tag("Beta"), e.tag("Gamma")
	p1 := e.problem("one", a.Slug)
	p2 := e.problem("two", a.Slug, c.Slug)
	p3 := e.problem("three", b.Slug)

	form := get(e.mux, "/tag-operations/merge", true).Body.String()
	for _, want := range []string{`name="sources"`, `name="target"`, `type="checkbox"`} {
		if !strings.Contains(form, want) {
			t.Errorf("merge form lacks %q", want)
		}
	}
	// Merging a tag into itself is rejected and keeps the choices.
	rec := e.do("POST", "/tag-operations/merge", url.Values{"sources": {a.ID.String()}, "target": {a.ID.String()}}, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `role="alert"`) || !strings.Contains(rec.Body.String(), `value="`+a.ID.String()+`" checked`) {
		t.Fatalf("self merge = %d\n%s", rec.Code, rec.Body)
	}

	rec = e.do("POST", "/tag-operations/merge", url.Values{"sources": {a.ID.String(), c.ID.String()}, "target": {b.ID.String()}}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("merge = %d\n%s", rec.Code, rec.Body)
	}
	id := e.proposedID(rec.Header().Get("Location"))
	queue := get(e.mux, rec.Header().Get("Location"), true).Body.String()
	// Preview: 2 problems change; Beta goes from 1 to 3... (p1, p2 join p3).
	if !strings.Contains(queue, "Merge Alpha, Gamma into Beta") || !strings.Contains(queue, "2 problems change tags") {
		t.Errorf("merge preview:\n%s", queue)
	}
	if got := e.tagSlugs(p1); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("proposal changed tags: %v", got)
	}

	if rec := e.do("POST", "/tag-operations/"+id+"/apply", url.Values{}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply = %d\n%s", rec.Code, rec.Body)
	}
	for _, p := range []domain.ID{p1, p2, p3} {
		if got := e.tagSlugs(p); len(got) != 1 || got[0] != "beta" {
			t.Errorf("tags after merge = %v", got)
		}
	}
	if rec := e.do("POST", "/tag-operations/"+id+"/revert", url.Values{}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("revert = %d\n%s", rec.Code, rec.Body)
	}
	if got := e.tagSlugs(p2); len(got) != 2 {
		t.Errorf("tags after revert = %v", got)
	}
	page := get(e.mux, "/tag-operations", true).Body.String()
	if !strings.Contains(page, "Reverted") || strings.Contains(page, "/revert\"") {
		t.Errorf("reverted op still offers Revert:\n%s", page)
	}
	if rec := e.do("POST", "/tag-operations/"+id+"/revert", url.Values{}, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already been reverted") {
		t.Errorf("second revert = %d", rec.Code)
	}
}

func TestTagOpReject(t *testing.T) {
	e := newProbEnv(t)
	a := e.tag("Alpha")
	op := e.agentPropose(tags.OpRename, tags.RenameInput{Tag: a.Slug, Name: ptr("Nope")})
	id := op.Operation.ID.String()
	queue := get(e.mux, "/tag-operations", true).Body.String()
	if !strings.Contains(queue, "Proposed by agent tagger") {
		t.Errorf("agent proposer not shown:\n%s", queue)
	}
	if rec := e.do("POST", "/tag-operations/"+id+"/reject", url.Values{}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("reject = %d", rec.Code)
	}
	got, _ := e.tags.GetOperation(context.Background(), e.owner, op.Operation.ID)
	if got.Operation.Status != tags.OpRejected {
		t.Errorf("status = %s", got.Operation.Status)
	}
	if strings.Contains(get(e.mux, "/tag-operations", true).Body.String(), "/tag-operations/"+id+"/apply") {
		t.Error("rejected op still offers Apply")
	}
	if rec := e.do("POST", "/tag-operations/"+id+"/apply", url.Values{}, true); rec.Code != http.StatusConflict {
		t.Errorf("apply rejected = %d, want 409", rec.Code)
	}
	if rec := e.do("POST", "/tag-operations/not-an-id/apply", url.Values{}, true); rec.Code != http.StatusNotFound {
		t.Errorf("bad id = %d", rec.Code)
	}
}

func TestTagOpRevertConflictShownGracefully(t *testing.T) {
	e := newProbEnv(t)
	a := e.tag("Alpha")
	e.problem("one", a.Slug)
	ctx := context.Background()
	ok := domain.Actor{Kind: domain.ActorHuman, UserID: e.owner, ClientID: "web-session"}
	apply := func(name string) tags.OpDetail {
		raw, _ := json.Marshal(tags.RenameInput{Tag: a.ID.String(), Name: ptr(name)})
		d, err := e.tags.Propose(ctx, e.owner, ok, tags.ProposeInput{Kind: tags.OpRename, Payload: raw, Apply: true})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	first, _ := apply("Second"), apply("Third")
	rec := e.do("POST", "/tag-operations/"+first.Operation.ID.String()+"/revert", url.Values{}, true)
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, "a later applied operation touched the same tags") || !strings.Contains(body, `role="alert"`) {
		t.Fatalf("revert conflict = %d\n%s", rec.Code, body)
	}
	if !strings.Contains(body, "Applied") {
		t.Error("conflict response lost the page")
	}
}

func TestSplitWizard(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()
	src, other := e.tag("Networking"), e.tag("Security")
	p1 := e.problem("What is TCP?", src.Slug)
	p2 := e.problem("What is TLS?", src.Slug)
	p3 := e.problem("What is DNS?", src.Slug)

	// Step 1: choose the source.
	s1 := get(e.mux, "/tag-operations/split", true).Body.String()
	if !strings.Contains(s1, "Step 1 of 3") || !strings.Contains(s1, `name="source"`) {
		t.Fatalf("step 1:\n%s", s1)
	}
	// Step 2: define targets.
	s2 := get(e.mux, "/tag-operations/split?source="+src.ID.String(), true).Body.String()
	for _, want := range []string{"Step 2 of 3", `name="existing" value="` + other.ID.String() + `"`, `name="new_name"`, `name="retire_source"`} {
		if !strings.Contains(s2, want) {
			t.Errorf("step 2 lacks %q", want)
		}
	}
	if strings.Contains(s2, `name="existing" value="`+src.ID.String()+`"`) {
		t.Error("source offered as its own target")
	}
	// No target chosen: stays on step 2 with a message.
	if s := get(e.mux, "/tag-operations/split?step=assign&source="+src.ID.String(), true).Body.String(); !strings.Contains(s, "Choose at least one") || !strings.Contains(s, "Step 2 of 3") {
		t.Errorf("no targets:\n%s", s)
	}
	// Unknown source.
	if rec := get(e.mux, "/tag-operations/split?source=nope", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown source = %d", rec.Code)
	}
	// Step 3: assign problems.
	q := url.Values{"source": {src.ID.String()}, "step": {"assign"}, "existing": {other.ID.String()},
		"new_name": {"Transport", "", ""}, "new_slug": {"", "", ""}}
	s3 := get(e.mux, "/tag-operations/split?"+q.Encode(), true).Body.String()
	for _, want := range []string{"Step 3 of 3", "What is TCP?", "What is TLS?", "What is DNS?", "Transport (new)", "Security", `name="assign"`, `name="csrf_token"`} {
		if !strings.Contains(s3, want) {
			t.Errorf("step 3 lacks %q", want)
		}
	}

	form := url.Values{"source": {src.ID.String()}, "existing": {other.ID.String()},
		"new_name": {"Transport", "", ""}, "new_slug": {"", "", ""},
		"assign": {p1.String() + "|transport", p2.String() + "|transport", p2.String() + "|" + other.ID.String()},
		"retire_source": {"1"}}
	// p3 unassigned while retiring: the service refuses and the wizard shows it.
	rec := e.do("POST", "/tag-operations/split", form, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `role="alert"`) || !strings.Contains(rec.Body.String(), `value="`+p1.String()+`|transport" checked`) {
		t.Fatalf("incomplete retire split = %d\n%s", rec.Code, rec.Body)
	}
	form.Add("assign", p3.String()+"|"+other.ID.String())
	rec = e.do("POST", "/tag-operations/split", form, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("split = %d\n%s", rec.Code, rec.Body)
	}
	id := e.proposedID(rec.Header().Get("Location"))
	if got := e.tagSlugs(p1); len(got) != 1 || got[0] != "networking" {
		t.Fatalf("proposal changed tags: %v", got)
	}
	queue := get(e.mux, rec.Header().Get("Location"), true).Body.String()
	for _, want := range []string{"Split Networking into Security, Transport (new)", "source retired", "Transport", "new"} {
		if !strings.Contains(queue, want) {
			t.Errorf("split preview lacks %q:\n%s", want, queue)
		}
	}
	if rec := e.do("POST", "/tag-operations/"+id+"/apply", url.Values{}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply = %d\n%s", rec.Code, rec.Body)
	}
	if got := e.tagSlugs(p1); len(got) != 1 || got[0] != "transport" {
		t.Errorf("p1 tags = %v", got)
	}
	if got := e.tagSlugs(p2); len(got) != 2 {
		t.Errorf("p2 tags = %v", got)
	}
	if got := e.tagSlugs(p3); len(got) != 1 || got[0] != "security" {
		t.Errorf("p3 tags = %v", got)
	}
	retired, _, _ := e.tags.List(ctx, e.owner, tags.StatusRetired, platform.Page{Limit: 10})
	if len(retired) != 1 || retired[0].ID != src.ID {
		t.Errorf("retired = %v", retired)
	}
}
