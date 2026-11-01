// SPDX-License-Identifier: Apache-2.0

package tags_test

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// state renders every tag row (all statuses, with aliases) and every problem
// mapping of the given problems, so two states compare with ==.
func (f *splitFx) state(problems ...domain.ID) string {
	f.t.Helper()
	var lines []string
	for _, st := range []tags.Status{tags.StatusActive, tags.StatusMerged, tags.StatusRetired} {
		ts, err := f.st.Tags().List(f.ctx, f.owner, tags.ListFilter{Status: st})
		if err != nil {
			f.t.Fatal(err)
		}
		for _, tg := range ts {
			lines = append(lines, fmt.Sprintf("tag %v %q %q %q %s into=%v aliases=%v c=%v u=%v",
				tg.ID, tg.Slug, tg.Name, tg.Description, tg.Status, tg.MergedInto, tg.Aliases, tg.CreatedAt.UnixMilli(), tg.UpdatedAt.UnixMilli()))
		}
	}
	for _, p := range problems {
		ts, err := f.st.Tags().ProblemTags(f.ctx, f.owner, p)
		if err != nil {
			f.t.Fatal(err)
		}
		var ids []string
		for _, tg := range ts {
			ids = append(ids, tg.ID.String())
		}
		sort.Strings(ids)
		lines = append(lines, fmt.Sprintf("problem %v %v", p, ids))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func (f *splitFx) applyNow(kind tags.OpKind, payload any) tags.Operation {
	f.t.Helper()
	d, err := f.svc.Propose(f.ctx, f.owner, f.actor, tags.ProposeInput{Kind: kind, Payload: mustJSON(f.t, payload), Apply: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return d.Operation
}

func (f *splitFx) revert(id domain.ID) (tags.OpDetail, error) {
	return f.svc.RevertOperation(f.ctx, f.owner, f.actor, id)
}

func TestRevertMergeRoundTrips(t *testing.T) {
	f := newSplitFx(t)
	a, b, dst := f.tag("golang"), f.tag("go-lang"), f.tag("go")
	if _, err := f.svc.Rename(f.ctx, f.owner, tags.RenameInput{Tag: "go-lang", Name: ptr("Go Lang")}); err != nil {
		t.Fatal(err)
	}
	ps := []domain.ID{f.problem(a.ID), f.problem(a.ID, dst.ID), f.problem(b.ID, dst.ID), f.problem(dst.ID), f.problem()}
	before := f.state(ps...)

	op := f.applyNow(tags.OpMerge, tags.MergeInput{Sources: []string{"golang", "go-lang"}, Target: "go"})
	if f.state(ps...) == before {
		t.Fatal("merge changed nothing")
	}
	d, err := f.revert(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after := f.state(ps...); after != before {
		t.Fatalf("state after revert differs:\n--- before\n%s\n--- after\n%s", before, after)
	}
	got := d.Operation
	if got.Status != tags.OpApplied || got.RevertedAt.IsZero() || got.RevertedBy != f.actor || got.Snapshot == nil {
		t.Fatalf("reverted op = %+v", got)
	}
	// Aliases resolve as before: the old slugs are the tags again.
	if tg, err := f.svc.Get(f.ctx, f.owner, "golang"); err != nil || tg.ID != a.ID {
		t.Fatalf("Get(golang) = %+v, %v", tg, err)
	}

	// Reverted once.
	if _, err := f.revert(op.ID); !errors.Is(err, tags.ErrOpReverted) {
		t.Fatalf("second revert = %v, want ErrOpReverted", err)
	}
	// The merge can be proposed and applied again after the revert.
	f.applyNow(tags.OpMerge, tags.MergeInput{Sources: []string{"golang"}, Target: "go"})
}

func TestRevertRename(t *testing.T) {
	f := newSplitFx(t)
	a := f.tag("old")
	p := f.problem(a.ID)
	before := f.state(p)
	op := f.applyNow(tags.OpRename, tags.RenameInput{Tag: "old", Slug: ptr("new"), Name: ptr("New name")})
	if tg, _ := f.svc.Get(f.ctx, f.owner, "old"); tg.Slug != "new" {
		t.Fatalf("rename not applied: %+v", tg)
	}
	if _, err := f.revert(op.ID); err != nil {
		t.Fatal(err)
	}
	if after := f.state(p); after != before {
		t.Fatalf("state after revert differs:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestRevertSplitDirectRemovesCreatedTags(t *testing.T) {
	f := newSplitFx(t)
	src, other := f.tag("data"), f.tag("math")
	p1, p2, p3 := f.problem(src.ID), f.problem(src.ID, other.ID), f.problem(src.ID)
	before := f.state(p1, p2, p3)

	res, err := f.svc.Split(f.ctx, f.owner, f.actor, tags.SplitInput{
		Source: "data",
		Targets: []tags.SplitTarget{
			{New: &tags.CreateInput{Name: "Trees"}}, {New: &tags.CreateInput{Name: "Graphs"}}, {Ref: "math"}},
		Assignments:  map[domain.ID][]string{p1: {"trees"}, p2: {"graphs", "math"}, p3: {"trees", "graphs"}},
		RetireSource: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := []domain.ID{res.Targets[0].ID, res.Targets[1].ID}
	if f.status(src.ID) != tags.StatusRetired {
		t.Fatal("split not applied")
	}
	if _, err := f.revert(res.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if after := f.state(p1, p2, p3); after != before {
		t.Fatalf("state after revert differs:\n--- before\n%s\n--- after\n%s", before, after)
	}
	for _, id := range created {
		if _, err := f.st.Tags().Get(f.ctx, f.owner, id); !errors.Is(err, tags.ErrNotFound) {
			t.Fatalf("created tag %v still exists: %v", id, err)
		}
	}
}

func TestRevertSplitProposedThenApplied(t *testing.T) {
	f := newSplitFx(t)
	src := f.tag("data")
	p1, p2 := f.problem(src.ID), f.problem(src.ID)
	before := f.state(p1, p2)

	d, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{Kind: tags.OpSplit, Payload: mustJSON(t, tags.SplitInput{
		Source:       "data",
		Targets:      []tags.SplitTarget{{New: &tags.CreateInput{Name: "Trees"}}, {New: &tags.CreateInput{Name: "Graphs"}}},
		Assignments:  map[domain.ID][]string{p1: {"trees"}, p2: {"graphs"}},
		RetireSource: true})})
	if err != nil {
		t.Fatal(err)
	}
	// Reverting a proposal is refused.
	if _, err := f.revert(d.Operation.ID); !errors.Is(err, tags.ErrOpNotApplied) {
		t.Fatalf("revert of proposed = %v, want ErrOpNotApplied", err)
	}
	if _, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, d.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.revert(d.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if after := f.state(p1, p2); after != before {
		t.Fatalf("state after revert differs:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestRevertRejectedIsRefused(t *testing.T) {
	f := newSplitFx(t)
	f.tag("a")
	d, err := f.svc.Propose(f.ctx, f.owner, f.actor, tags.ProposeInput{Kind: tags.OpRename, Payload: mustJSON(t, tags.RenameInput{Tag: "a", Slug: ptr("b")})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RejectOperation(f.ctx, f.owner, f.actor, d.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.revert(d.Operation.ID); !errors.Is(err, tags.ErrOpNotApplied) {
		t.Fatalf("revert of rejected = %v, want ErrOpNotApplied", err)
	}
}

func TestRevertRefusedWhenLaterOperationTouchedSameTags(t *testing.T) {
	f := newSplitFx(t)
	a, b := f.tag("alpha"), f.tag("beta")
	f.tag("unrelated")
	p := f.problem(a.ID)
	before := f.state(p)

	merge := f.applyNow(tags.OpMerge, tags.MergeInput{Sources: []string{"alpha"}, Target: "beta"})
	afterMerge := f.state(p)
	rename := f.applyNow(tags.OpRename, tags.RenameInput{Tag: "beta", Name: ptr("Beta renamed")})
	// An operation on other tags does not block.
	other := f.applyNow(tags.OpRename, tags.RenameInput{Tag: "unrelated", Name: ptr("Other")})

	if _, err := f.revert(merge.ID); !errors.Is(err, tags.ErrRevertConflict) {
		t.Fatalf("revert merge = %v, want ErrRevertConflict", err)
	}
	if f.state(p) == before || f.status(a.ID) != tags.StatusMerged {
		t.Fatal("refused revert changed state")
	}
	if _, err := f.revert(other.ID); err != nil {
		t.Fatalf("revert unrelated = %v", err)
	}
	// Reverting the later operation first unblocks the earlier one.
	if _, err := f.revert(rename.ID); err != nil {
		t.Fatal(err)
	}
	if f.state(p) != afterMerge {
		t.Fatal("rename revert broke state")
	}
	if _, err := f.revert(merge.ID); err != nil {
		t.Fatal(err)
	}
	if after := f.state(p); !strings.Contains(after, "unrelated") || strings.Contains(after, "Beta renamed") {
		t.Fatalf("state = %s", after)
	}
	for _, id := range []domain.ID{a.ID, b.ID} {
		if f.status(id) != tags.StatusActive {
			t.Fatalf("tag %v not active after reverts", id)
		}
	}
}

func TestRevertOtherOwnersOperationIsNotFound(t *testing.T) {
	f := newSplitFx(t)
	f.tag("a")
	op := f.applyNow(tags.OpRename, tags.RenameInput{Tag: "a", Slug: ptr("b")})
	other := f.ids.New()
	actor := domain.Actor{Kind: domain.ActorHuman, UserID: other}
	if _, err := f.svc.RevertOperation(f.ctx, other, actor, op.ID); !errors.Is(err, tags.ErrOpNotFound) {
		t.Fatalf("revert by other owner = %v, want ErrOpNotFound", err)
	}
	if tg, _ := f.svc.Get(f.ctx, f.owner, "b"); tg.Slug != "b" {
		t.Fatalf("tag changed: %+v", tg)
	}
}
