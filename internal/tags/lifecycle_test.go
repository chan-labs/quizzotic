// SPDX-License-Identifier: Apache-2.0

package tags_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func (f *splitFx) agent() domain.Actor {
	return domain.Actor{Kind: domain.ActorAgent, UserID: f.owner, ClientID: "agent-client"}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProposeStoresProposalWithoutChanges(t *testing.T) {
	f := newSplitFx(t)
	a, b := f.tag("alpha"), f.tag("beta")
	p1, p2 := f.problem(a.ID), f.problem(a.ID, b.ID)

	d, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{
		Kind: tags.OpMerge, Payload: mustJSON(t, tags.MergeInput{Sources: []string{"alpha"}, Target: "beta"})})
	if err != nil {
		t.Fatal(err)
	}
	op := d.Operation
	if op.Status != tags.OpProposed || op.Snapshot != nil || op.ProposedBy.ClientID != "agent-client" || !op.DecidedBy.UserID.IsNil() {
		t.Fatalf("op = %+v", op)
	}
	if d.Preview == nil || !slices.Equal(d.Preview.AffectedProblems, []domain.ID{p1, p2}) {
		t.Fatalf("preview = %+v", d.Preview)
	}
	// alpha 2 -> 0 (merged), beta 1 -> 2.
	got := map[string][2]int{}
	for _, te := range d.Preview.Tags {
		got[te.Slug] = [2]int{te.ProblemsBefore, te.ProblemsAfter}
	}
	if got["alpha"] != [2]int{2, 0} || got["beta"] != [2]int{1, 2} {
		t.Fatalf("preview counts = %v", got)
	}
	// Nothing changed, and the stored payload names tags by ID.
	if f.status(a.ID) != tags.StatusActive || !slices.Equal(f.slugsOf(p1), []string{"alpha"}) {
		t.Fatal("proposal changed tags")
	}
	stored, err := f.st.TagOperations().Get(f.ctx, f.owner, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	var norm tags.MergeInput
	if err := json.Unmarshal(stored.Payload, &norm); err != nil || norm.Target != b.ID.String() || norm.Sources[0] != a.ID.String() {
		t.Fatalf("stored payload = %s (%v)", stored.Payload, err)
	}
}

func TestApplyOperationCapturesSnapshotAndDecider(t *testing.T) {
	f := newSplitFx(t)
	a := f.tag("alpha")
	f.tag("beta")
	p1 := f.problem(a.ID)
	d, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{
		Kind: tags.OpMerge, Payload: mustJSON(t, tags.MergeInput{Sources: []string{"alpha"}, Target: "beta"})})
	if err != nil {
		t.Fatal(err)
	}
	// The world moves on after the proposal: the snapshot is the state at apply.
	p2 := f.problem(a.ID)

	got, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, d.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	op := got.Operation
	if op.Status != tags.OpApplied || op.DecidedBy != f.actor || op.ProposedBy.ClientID != "agent-client" || op.AppliedAt.IsZero() {
		t.Fatalf("op = %+v", op)
	}
	if op.Snapshot == nil || len(op.Snapshot.Tags) != 2 || len(op.Snapshot.Mappings) != 2 {
		t.Fatalf("snapshot = %+v", op.Snapshot)
	}
	if got.Result == nil || !slices.Equal(got.Result.AffectedProblems, []domain.ID{p1, p2}) {
		t.Fatalf("result = %+v", got.Result)
	}
	if f.status(a.ID) != tags.StatusMerged || !slices.Equal(f.slugsOf(p2), []string{"beta"}) {
		t.Fatal("merge not applied")
	}
	if _, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, op.ID); !errors.Is(err, tags.ErrOpNotProposed) {
		t.Fatalf("second apply = %v, want ErrOpNotProposed", err)
	}
	if _, err := f.svc.RejectOperation(f.ctx, f.owner, f.actor, op.ID); !errors.Is(err, tags.ErrOpNotProposed) {
		t.Fatalf("reject after apply = %v, want ErrOpNotProposed", err)
	}
}

func TestSplitProposalCreatesPreviewedTagOnApply(t *testing.T) {
	f := newSplitFx(t)
	src := f.tag("data")
	p1, p2 := f.problem(src.ID), f.problem(src.ID)
	d, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{Kind: tags.OpSplit, Payload: mustJSON(t, tags.SplitInput{
		Source:       "data",
		Targets:      []tags.SplitTarget{{New: &tags.CreateInput{Name: "Trees"}}},
		Assignments:  map[domain.ID][]string{p1: {"trees"}, p2: {"trees"}},
		RetireSource: true,
	})})
	if err != nil {
		t.Fatal(err)
	}
	var previewed *tags.TagEffect
	for i, te := range d.Preview.Tags {
		if te.Created {
			previewed = &d.Preview.Tags[i]
		}
	}
	if previewed == nil || previewed.Slug != "trees" || previewed.ProblemsBefore != 0 || previewed.ProblemsAfter != 2 {
		t.Fatalf("preview tags = %+v", d.Preview.Tags)
	}
	if _, err := f.st.Tags().Get(f.ctx, f.owner, previewed.ID); !errors.Is(err, tags.ErrNotFound) {
		t.Fatalf("preview created the tag: %v", err)
	}
	// A client cannot choose the ID of a tag a proposal creates.
	forced := f.ids.New()
	d2, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{Kind: tags.OpSplit, Payload: mustJSON(t, tags.SplitInput{
		Source: "data", Targets: []tags.SplitTarget{{New: &tags.CreateInput{Name: "Graphs"}, NewID: &forced}}})})
	if err != nil {
		t.Fatal(err)
	}
	for _, te := range d2.Preview.Tags {
		if te.ID == forced {
			t.Fatal("client-chosen new_id was honored")
		}
	}

	if _, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, d.Operation.ID); err != nil {
		t.Fatal(err)
	}
	created, err := f.st.Tags().Get(f.ctx, f.owner, previewed.ID)
	if err != nil || created.Slug != "trees" {
		t.Fatalf("created tag = %+v (%v)", created, err)
	}
	if f.status(src.ID) != tags.StatusRetired {
		t.Fatal("source not retired")
	}
}

func TestProposeRejectsInvalidAndStoresNothing(t *testing.T) {
	f := newSplitFx(t)
	f.tag("alpha")
	for name, in := range map[string]tags.ProposeInput{
		"bad kind":    {Kind: "explode", Payload: json.RawMessage(`{}`)},
		"no payload":  {Kind: tags.OpRename},
		"unknown key": {Kind: tags.OpRename, Payload: json.RawMessage(`{"tag":"alpha","slug":"x","bogus":1}`)},
		"no such tag": {Kind: tags.OpRename, Payload: json.RawMessage(`{"tag":"nope","slug":"x"}`)},
		"merge self":  {Kind: tags.OpMerge, Payload: json.RawMessage(`{"sources":["alpha"],"target":"alpha"}`)},
	} {
		if _, err := f.svc.Propose(f.ctx, f.owner, f.actor, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ops, _, err := f.svc.ListOperations(f.ctx, f.owner, "", platform.Page{Limit: 10})
	if err != nil || len(ops) != 0 {
		t.Fatalf("ops = %v (%v)", ops, err)
	}
}

func TestRejectOperationChangesNothing(t *testing.T) {
	f := newSplitFx(t)
	f.tag("alpha")
	d, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{
		Kind: tags.OpRename, Payload: json.RawMessage(`{"tag":"alpha","slug":"omega"}`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.RejectOperation(f.ctx, f.owner, f.actor, d.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation.Status != tags.OpRejected || got.Operation.DecidedBy != f.actor || got.Operation.Snapshot != nil {
		t.Fatalf("op = %+v", got.Operation)
	}
	if _, err := f.svc.Get(f.ctx, f.owner, "alpha"); err != nil {
		t.Fatalf("tag renamed by a rejected proposal: %v", err)
	}
	if _, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, d.Operation.ID); !errors.Is(err, tags.ErrOpNotProposed) {
		t.Fatalf("apply after reject = %v", err)
	}
}

func TestApplyFailureLeavesProposalProposed(t *testing.T) {
	f := newSplitFx(t)
	f.tag("alpha")
	d, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{
		Kind: tags.OpRename, Payload: json.RawMessage(`{"tag":"alpha","slug":"omega"}`)})
	if err != nil {
		t.Fatal(err)
	}
	f.tag("omega") // takes the slug after the proposal
	if _, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, d.Operation.ID); !errors.Is(err, tags.ErrSlugTaken) {
		t.Fatalf("apply = %v, want ErrSlugTaken", err)
	}
	got, err := f.svc.GetOperation(f.ctx, f.owner, d.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation.Status != tags.OpProposed || got.Preview != nil || got.PreviewError == "" {
		t.Fatalf("detail = %+v", got)
	}
}
