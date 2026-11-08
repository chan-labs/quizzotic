// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/seedrun"
)

const httpSeedPack = `
version: 1
tags:
  - {key: t.basics, slug: basics, name: Basics}
problems:
  - key: p.one
    type: open
    prompt: Seeded one?
    payload: {reference_answer: one}
    tags: [t.basics]
  - key: p.two
    type: open
    prompt: Seeded two?
    payload: {reference_answer: two}
  - key: p.three
    type: open
    prompt: Seeded three?
    payload: {reference_answer: three}
quizzes:
  - key: q.trio
    title: Seeded trio
    problems: [p.one, p.two, p.three]
`

// TestSeededContentIsManageableThroughTheAPI is the M2 exit check: content
// loaded from YAML behaves like content created over HTTP.
func TestSeededContentIsManageableThroughTheAPI(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()

	// The user exists once they have called the API.
	e.req(t, "GET", "/problems", e.human, nil, nil)
	ownerID, _ := e.userFor(t, "author-sub")
	owner, err := domain.ParseID(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := seed.Parse(strings.NewReader(httpSeedPack))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // twice: the second run must change nothing
		if _, err := seedrun.ApplyFile(ctx, e.st, owner, f); err != nil {
			t.Fatal(err)
		}
	}

	// List: three live problems, created by the system actor; the tag filter works.
	all := e.list(t, e.human, url.Values{"created_by_kind": {"system"}})
	if len(all.Items) != 3 {
		t.Fatalf("seeded problems = %d, want 3 (second run must not duplicate)", len(all.Items))
	}
	for _, p := range all.Items {
		if p.Status != problems.StatusLive || p.CreatedBy.Kind != domain.ActorSystem {
			t.Errorf("problem = %+v", p)
		}
	}
	if tagged := e.list(t, e.human, url.Values{"tag": {"basics"}}); len(tagged.Items) != 1 || tagged.Items[0].Prompt != "Seeded one?" {
		t.Errorf("tag filter = %+v", tagged.Items)
	}
	byPrompt := map[string]problems.View{}
	for _, p := range all.Items {
		byPrompt[p.Prompt] = p
	}
	one, two, three := byPrompt["Seeded one?"], byPrompt["Seeded two?"], byPrompt["Seeded three?"]

	// Edit as the human: a new version by a human actor.
	path := "/problems/" + one.ID.String()
	etag := e.req(t, "GET", path, e.human, nil, nil).header.Get("ETag")
	r := e.req(t, "PATCH", path, e.human, map[string]any{"change_kind": "minor", "prompt": "Edited one?"},
		map[string]string{"If-Match": etag})
	if r.code != http.StatusOK {
		t.Fatalf("edit = %d (%s)", r.code, r.body)
	}
	edited := decodeReply[problems.View](t, r)
	if edited.Version != 2 || edited.Prompt != "Edited one?" {
		t.Errorf("edited = %+v", edited)
	}
	vs := e.req(t, "GET", path+"/versions", e.human, nil, nil)
	if vs.code != http.StatusOK || !strings.Contains(string(vs.body), `"system"`) || !strings.Contains(string(vs.body), `"human"`) {
		t.Errorf("versions should show the system v1 and the human v2: %d %s", vs.code, vs.body)
	}

	// Archive another one; it leaves the live list.
	pathTwo := "/problems/" + two.ID.String()
	etag = e.req(t, "GET", pathTwo, e.human, nil, nil).header.Get("ETag")
	if r := e.req(t, "POST", pathTwo+"/archive", e.human, nil, map[string]string{"If-Match": etag}); r.code != http.StatusOK {
		t.Fatalf("archive = %d (%s)", r.code, r.body)
	}
	if live := e.list(t, e.human, url.Values{"status": {"live"}, "created_by_kind": {"system"}}); len(live.Items) != 2 {
		t.Errorf("live after archive = %d, want 2", len(live.Items))
	}

	// Re-seeding the unchanged file leaves those API edits alone.
	if _, err := seedrun.ApplyFile(ctx, e.st, owner, f); err != nil {
		t.Fatal(err)
	}
	if got := decodeReply[problems.View](t, e.req(t, "GET", path, e.human, nil, nil)); got.Prompt != "Edited one?" || got.Version != 2 {
		t.Errorf("re-seed clobbered the edit: %+v", got)
	}
	if got := decodeReply[problems.View](t, e.req(t, "GET", pathTwo, e.human, nil, nil)); got.Status != problems.StatusArchived {
		t.Errorf("re-seed resurrected the archived problem: %+v", got)
	}

	// Reorder the seeded quiz.
	qs := e.req(t, "GET", "/quizzes", e.human, nil, nil)
	page := decodeReply[struct {
		Items []quizJSON `json:"items"`
	}](t, qs)
	if len(page.Items) != 1 || page.Items[0].Title != "Seeded trio" || page.Items[0].CreatedBy.Kind != "system" {
		t.Fatalf("quizzes = %s", qs.body)
	}
	quiz := page.Items[0]
	want := []string{one.ID.String(), two.ID.String(), three.ID.String()}
	if !slices.Equal(quiz.problemIDs(), want) {
		t.Fatalf("seeded order = %v, want %v", quiz.problemIDs(), want)
	}
	qpath := "/quizzes/" + quiz.ID
	qetag := e.req(t, "GET", qpath, e.human, nil, nil).header.Get("ETag")
	reordered := []string{three.ID.String(), one.ID.String(), two.ID.String()}
	put := e.req(t, "PUT", qpath+"/items", e.human, map[string]any{"problem_ids": reordered}, map[string]string{"If-Match": qetag})
	if put.code != http.StatusOK {
		t.Fatalf("reorder = %d (%s)", put.code, put.body)
	}
	if got := decodeReply[quizJSON](t, e.req(t, "GET", qpath, e.human, nil, nil)); !slices.Equal(got.problemIDs(), reordered) {
		t.Errorf("order after reorder = %v, want %v", got.problemIDs(), reordered)
	}
}
