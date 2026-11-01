// SPDX-License-Identifier: Apache-2.0

package tags_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/tags"
)

type mergeEnv struct {
	t     *testing.T
	ctx   context.Context
	s     store.Store
	svc   *tags.Service
	owner domain.ID
	ids   *domain.IDGenerator
}

func newMergeEnv(t *testing.T) *mergeEnv {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := domain.NewFakeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	e := &mergeEnv{t: t, ctx: ctx, s: db, ids: ids, svc: tags.NewService(store.TagsTx(db), clock, ids)}
	u := auth.User{ID: ids.New(), DisplayName: "u", Email: "u@example.com", CreatedAt: clock.Now()}
	if err := db.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	e.owner = u.ID
	return e
}

func (e *mergeEnv) tag(slug string) tags.Tag {
	e.t.Helper()
	tg, err := e.svc.Create(e.ctx, e.owner, tags.CreateInput{Slug: slug, Name: slug})
	if err != nil {
		e.t.Fatal(err)
	}
	return tg
}

func (e *mergeEnv) problem(refs ...string) domain.ID {
	e.t.Helper()
	act := domain.Actor{Kind: domain.ActorHuman, UserID: e.owner}
	id := e.ids.New()
	now := time.Now().UTC()
	p := problems.Problem{ID: id, OwnerID: e.owner, Status: problems.StatusLive, CurrentVersion: 1,
		CreatedBy: act, CreatedAt: now, UpdatedAt: now}
	v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: "q",
		Payload: json.RawMessage(`{"reference_answer":"a"}`), ChangeKind: problems.ChangeMajor, CreatedBy: act, CreatedAt: now}
	if err := e.s.Problems().Create(e.ctx, e.owner, p, v); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.SetProblemTags(e.ctx, e.owner, id, refs); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *mergeEnv) slugsOf(p domain.ID) []string {
	e.t.Helper()
	ts, err := e.s.Tags().ProblemTags(e.ctx, e.owner, p)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, tg := range ts {
		out = append(out, tg.Slug)
	}
	return out
}

func TestMergeOverlappingProblems(t *testing.T) {
	e := newMergeEnv(t)
	a, b, dst := e.tag("golang"), e.tag("go-lang"), e.tag("go")
	p1 := e.problem("golang")
	p2 := e.problem("golang", "go")
	p3 := e.problem("golang", "go-lang")
	p4 := e.problem("go-lang", "go")
	p5 := e.problem("go")

	res, err := e.svc.Merge(e.ctx, e.owner, tags.MergeInput{Sources: []string{"golang", b.ID.String()}, Target: "go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []domain.ID{p1, p2, p3, p4, p5} {
		if got := e.slugsOf(p); !slices.Equal(got, []string{"go"}) {
			t.Fatalf("problem %v tags = %v, want [go] exactly once", p, got)
		}
	}
	if ids, _ := e.s.Tags().ProblemIDs(e.ctx, e.owner, dst.ID, domain.Nil, 0); len(ids) != 5 {
		t.Fatalf("target problems = %d, want 5", len(ids))
	}
	if len(res.Affected) != 4 || slices.Contains(res.Affected, p5) {
		t.Fatalf("affected = %v, want p1-p4", res.Affected)
	}
	if len(res.Sources) != 2 || res.Sources[0].ID != a.ID || res.Sources[0].Status != tags.StatusMerged ||
		res.Sources[0].MergedInto != dst.ID || res.Sources[1].MergedInto != dst.ID {
		t.Fatalf("sources = %+v", res.Sources)
	}
	if !slices.Equal(res.Target.Aliases, []string{"go-lang", "golang"}) || res.Target.Status != tags.StatusActive {
		t.Fatalf("target = %+v", res.Target)
	}
}

func TestMergeAliasResolution(t *testing.T) {
	e := newMergeEnv(t)
	e.tag("golang")
	dst := e.tag("go")
	// A rename before the merge leaves an alias on the source too.
	slug := "golang-2"
	old, _ := e.svc.Get(e.ctx, e.owner, "golang")
	if _, err := e.svc.Update(e.ctx, e.owner, "golang", tags.ETag(old), tags.Patch{Slug: &slug}); err != nil {
		t.Fatal(err)
	}
	p := e.problem("golang-2")
	if _, err := e.svc.Merge(e.ctx, e.owner, tags.MergeInput{Sources: []string{"golang-2"}, Target: dst.Slug}); err != nil {
		t.Fatal(err)
	}
	// Current slug, previous alias and ID of the source all reach the target.
	for _, ref := range []string{"golang-2", "golang", "GoLang", old.ID.String()} {
		got, err := e.svc.Get(e.ctx, e.owner, ref)
		if ref == old.ID.String() {
			if err != nil || got.Status != tags.StatusMerged || got.MergedInto != dst.ID {
				t.Fatalf("Get(source id) = %+v, %v", got, err)
			}
			continue
		}
		if err != nil || got.ID != dst.ID {
			t.Fatalf("Get(%q) = %+v, %v; want target", ref, got, err)
		}
	}
	// Filters and tagging by an old slug land on the target.
	if id, found, err := e.svc.ResolveTagFilter(e.ctx, e.owner, "golang"); err != nil || !found || id != dst.ID {
		t.Fatalf("filter = %v %v %v", id, found, err)
	}
	if id, _, _ := e.svc.ResolveTagFilter(e.ctx, e.owner, old.ID.String()); id != dst.ID {
		t.Fatalf("filter by merged id = %v", id)
	}
	q := e.problem("golang")
	if got := e.slugsOf(q); !slices.Equal(got, []string{"go"}) {
		t.Fatalf("tagged via old slug = %v", got)
	}
	if got := e.slugsOf(p); !slices.Equal(got, []string{"go"}) {
		t.Fatalf("migrated problem = %v", got)
	}
	// The merged tags are listed as merged, not active.
	act, _, _ := e.svc.List(e.ctx, e.owner, tags.StatusActive, platform.Page{Limit: 10})
	mer, _, _ := e.svc.List(e.ctx, e.owner, tags.StatusMerged, platform.Page{Limit: 10})
	if len(act) != 1 || len(mer) != 1 {
		t.Fatalf("active=%d merged=%d", len(act), len(mer))
	}
}

func TestMergeValidation(t *testing.T) {
	e := newMergeEnv(t)
	e.tag("a")
	bID := e.tag("b").ID.String()
	e.tag("c")
	if _, err := e.svc.Merge(e.ctx, e.owner, tags.MergeInput{Sources: []string{"b"}, Target: "c"}); err != nil {
		t.Fatal(err)
	}
	p := e.problem("a")
	cases := map[string]tags.MergeInput{
		"no sources":     {Target: "a"},
		"into itself":    {Sources: []string{"a"}, Target: "a"},
		"merged source":  {Sources: []string{bID}, Target: "a"},
		"merged target":  {Sources: []string{"a"}, Target: bID},
		"duplicate":      {Sources: []string{"a", "a"}, Target: "c"},
		"dup by alias":   {Sources: []string{"a", "A"}, Target: "c"},
		"unknown source": {Sources: []string{"zzz"}, Target: "c"},
		"unknown target": {Sources: []string{"a"}, Target: "zzz"},
	}
	// b was merged into c: its ID names a merged tag (its slug now names c).
	for name, in := range cases {
		_, err := e.svc.Merge(e.ctx, e.owner, in)
		var pe *platform.Problem
		if !errors.As(err, &pe) {
			t.Errorf("%s: err = %v, want validation problem", name, err)
		}
	}
	if got := e.slugsOf(p); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("failed merges changed mappings: %v", got)
	}
	if a, _ := e.svc.Get(e.ctx, e.owner, "a"); a.Status != tags.StatusActive {
		t.Fatalf("a = %+v", a)
	}
}
