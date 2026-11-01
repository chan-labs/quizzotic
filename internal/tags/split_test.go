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

type splitFx struct {
	t     *testing.T
	ctx   context.Context
	st    *sqlite.Store
	svc   *tags.Service
	owner domain.ID
	actor domain.Actor
	ids   *domain.IDGenerator
}

func newSplitFx(t *testing.T) *splitFx {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := domain.NewFakeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	u := auth.User{ID: ids.New(), DisplayName: "A", Email: "a@example.com", CreatedAt: clock.Now()}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	return &splitFx{t: t, ctx: ctx, st: st, svc: tags.NewService(store.TagsTx(st), clock, ids), owner: u.ID,
		actor: domain.Actor{Kind: domain.ActorHuman, UserID: u.ID}, ids: ids}
}

func (f *splitFx) tag(slug string) tags.Tag {
	f.t.Helper()
	tg, err := f.svc.Create(f.ctx, f.owner, tags.CreateInput{Slug: slug, Name: "Tag " + slug})
	if err != nil {
		f.t.Fatal(err)
	}
	return tg
}

func (f *splitFx) problem(tagIDs ...domain.ID) domain.ID {
	f.t.Helper()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	id := f.ids.New()
	act := domain.Actor{Kind: domain.ActorHuman, UserID: f.owner}
	p := problems.Problem{ID: id, OwnerID: f.owner, Status: problems.StatusLive, CurrentVersion: 1,
		CreatedBy: act, CreatedAt: now, UpdatedAt: now}
	v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: "q",
		Payload: json.RawMessage(`{"reference_answer":"a"}`), ChangeKind: problems.ChangeMajor, CreatedBy: act, CreatedAt: now}
	if err := f.st.Problems().Create(f.ctx, f.owner, p, v); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.Tags().SetProblemTags(f.ctx, f.owner, id, tagIDs); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *splitFx) slugsOf(p domain.ID) []string {
	f.t.Helper()
	ts, err := f.st.Tags().ProblemTags(f.ctx, f.owner, p)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, tg := range ts {
		out = append(out, tg.Slug)
	}
	return out
}

func (f *splitFx) status(id domain.ID) tags.Status {
	f.t.Helper()
	tg, err := f.st.Tags().Get(f.ctx, f.owner, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return tg.Status
}

func wantValidation(t *testing.T, err error, field string) {
	t.Helper()
	var p *platform.Problem
	if !errors.As(err, &p) || p.Type != platform.TypeValidation {
		t.Fatalf("err = %v, want validation error", err)
	}
	for _, fe := range p.Errors {
		if fe.Field == field {
			return
		}
	}
	t.Fatalf("no field error for %q in %+v", field, p.Errors)
}

func TestSplitNewTargetsRetire(t *testing.T) {
	f := newSplitFx(t)
	src, other := f.tag("data-structures"), f.tag("other")
	p1, p2 := f.problem(src.ID, other.ID), f.problem(src.ID)

	res, err := f.svc.Split(f.ctx, f.owner, f.actor, tags.SplitInput{
		Source: "data-structures",
		Targets: []tags.SplitTarget{
			{New: &tags.CreateInput{Name: "Trees"}},
			{New: &tags.CreateInput{Slug: "graphs", Name: "Graphs"}},
		},
		Assignments:  map[domain.ID][]string{p1: {"trees"}, p2: {"trees", "graphs"}},
		RetireSource: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.slugsOf(p1); !slices.Equal(got, []string{"other", "trees"}) {
		t.Fatalf("p1 tags = %v", got)
	}
	if got := f.slugsOf(p2); !slices.Equal(got, []string{"graphs", "trees"}) {
		t.Fatalf("p2 tags = %v", got)
	}
	if res.Source.Status != tags.StatusRetired || f.status(src.ID) != tags.StatusRetired {
		t.Fatalf("source not retired: %+v", res.Source)
	}
	if len(res.Targets) != 2 || res.Targets[0].Slug != "trees" || res.Targets[1].Status != tags.StatusActive {
		t.Fatalf("targets = %+v", res.Targets)
	}

	// The operation is recorded as applied with a before-snapshot of the source.
	op, err := f.st.TagOperations().Get(f.ctx, f.owner, res.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != tags.OpSplit || op.Status != tags.OpApplied || op.Snapshot == nil || op.DecidedBy != f.actor {
		t.Fatalf("op = %+v", op)
	}
	if len(op.Snapshot.Tags) != 1 || op.Snapshot.Tags[0].ID != src.ID || op.Snapshot.Tags[0].Status != tags.StatusActive ||
		len(op.Snapshot.Mappings) != 2 {
		t.Fatalf("snapshot = %+v", op.Snapshot)
	}
}

func TestSplitRejectsUnassignedWhenRetiring(t *testing.T) {
	f := newSplitFx(t)
	src := f.tag("big")
	p1, p2 := f.problem(src.ID), f.problem(src.ID)
	in := tags.SplitInput{
		Source:       "big",
		Targets:      []tags.SplitTarget{{New: &tags.CreateInput{Name: "Small"}}},
		Assignments:  map[domain.ID][]string{p1: {"small"}, p2: {}},
		RetireSource: true,
	}
	_, err := f.svc.Split(f.ctx, f.owner, f.actor, in)
	wantValidation(t, err, "assignments."+p2.String())

	delete(in.Assignments, p2)
	_, err = f.svc.Split(f.ctx, f.owner, f.actor, in)
	wantValidation(t, err, "assignments."+p2.String())

	// Nothing changed: no new tag, no operation, source intact.
	if _, err := tags.Resolve(f.ctx, f.st.Tags(), f.owner, "small"); !errors.Is(err, tags.ErrNotFound) {
		t.Fatalf("small exists: %v", err)
	}
	if ops, _ := f.st.TagOperations().List(f.ctx, f.owner, tags.OpFilter{}); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	if f.status(src.ID) != tags.StatusActive || !slices.Equal(f.slugsOf(p1), []string{"big"}) {
		t.Fatal("source or mappings changed")
	}
}

func TestSplitExistingTargetKeepSource(t *testing.T) {
	f := newSplitFx(t)
	src, dst := f.tag("algo"), f.tag("sorting")
	p1, p2, p3 := f.problem(src.ID), f.problem(src.ID, dst.ID), f.problem(src.ID)

	res, err := f.svc.Split(f.ctx, f.owner, f.actor, tags.SplitInput{
		Source: "algo",
		Targets: []tags.SplitTarget{
			{Ref: "sorting"},
			{New: &tags.CreateInput{Name: "Searching"}},
		},
		// p3 is left unassigned and keeps the source.
		Assignments: map[domain.ID][]string{p1: {dst.ID.String()}, p2: {"sorting", "searching"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.slugsOf(p1); !slices.Equal(got, []string{"sorting"}) {
		t.Fatalf("p1 = %v", got)
	}
	if got := f.slugsOf(p2); !slices.Equal(got, []string{"searching", "sorting"}) {
		t.Fatalf("p2 = %v", got)
	}
	if got := f.slugsOf(p3); !slices.Equal(got, []string{"algo"}) {
		t.Fatalf("p3 = %v", got)
	}
	if f.status(src.ID) != tags.StatusActive {
		t.Fatal("source retired without retire_source")
	}
	// Snapshot holds the source and the existing target, not the new one.
	snap := res.Operation.Snapshot
	if len(snap.Tags) != 2 || len(snap.Mappings) != 4 {
		t.Fatalf("snapshot = %+v", snap)
	}
	var pl struct {
		Created []domain.ID `json:"created"`
	}
	if err := json.Unmarshal(res.Operation.Payload, &pl); err != nil || len(pl.Created) != 1 || pl.Created[0] != res.Targets[1].ID {
		t.Fatalf("payload = %s (%v)", res.Operation.Payload, err)
	}
}

func TestSplitValidation(t *testing.T) {
	f := newSplitFx(t)
	src, other := f.tag("src"), f.tag("other")
	pOn := f.problem(src.ID)
	pOff := f.problem(other.ID)
	f.tag("dst")
	retired := f.tag("old")
	if _, err := f.svc.Split(f.ctx, f.owner, f.actor, tags.SplitInput{Source: "old",
		Targets: []tags.SplitTarget{{Ref: "dst"}}, RetireSource: true}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		in    tags.SplitInput
		field string
	}{
		{"unknown source", tags.SplitInput{Source: "nope", Targets: []tags.SplitTarget{{Ref: "dst"}}}, "source"},
		{"retired source", tags.SplitInput{Source: retired.Slug, Targets: []tags.SplitTarget{{Ref: "dst"}}}, "source"},
		{"no targets", tags.SplitInput{Source: "src"}, "targets"},
		{"target is source", tags.SplitInput{Source: "src", Targets: []tags.SplitTarget{{Ref: "src"}}}, "targets[0]"},
		{"unknown target", tags.SplitInput{Source: "src", Targets: []tags.SplitTarget{{Ref: "nope"}}}, "targets[0]"},
		{"neither ref nor new", tags.SplitInput{Source: "src", Targets: []tags.SplitTarget{{}}}, "targets[0]"},
		{"bad new name", tags.SplitInput{Source: "src", Targets: []tags.SplitTarget{{New: &tags.CreateInput{}}}}, "targets[0].name"},
		{"problem not on source", tags.SplitInput{Source: "src", Targets: []tags.SplitTarget{{Ref: "dst"}},
			Assignments: map[domain.ID][]string{pOff: {"dst"}}}, "assignments." + pOff.String()},
		{"assignment to non-target", tags.SplitInput{Source: "src", Targets: []tags.SplitTarget{{Ref: "dst"}},
			Assignments: map[domain.ID][]string{pOn: {"other"}}}, "assignments." + pOn.String()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.svc.Split(f.ctx, f.owner, f.actor, c.in)
			wantValidation(t, err, c.field)
		})
	}
	if _, err := f.svc.Split(f.ctx, f.owner, domain.Actor{}, tags.SplitInput{Source: "src",
		Targets: []tags.SplitTarget{{Ref: "dst"}}}); err == nil {
		t.Fatal("zero actor accepted")
	}
	// New target whose slug is taken.
	_, err := f.svc.Split(f.ctx, f.owner, f.actor, tags.SplitInput{Source: "src",
		Targets: []tags.SplitTarget{{New: &tags.CreateInput{Name: "Other"}}}})
	if !errors.Is(err, tags.ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

func TestSplitOtherOwnersTagIsNotFound(t *testing.T) {
	f := newSplitFx(t)
	f.tag("src")
	f.tag("dst")
	stranger := auth.User{ID: f.ids.New(), DisplayName: "B", Email: "b@example.com", CreatedAt: time.Now()}
	if err := f.st.Users().Create(f.ctx, stranger); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Split(f.ctx, stranger.ID, domain.Actor{Kind: domain.ActorHuman, UserID: stranger.ID},
		tags.SplitInput{Source: "src", Targets: []tags.SplitTarget{{Ref: "dst"}}})
	wantValidation(t, err, "source")
}
