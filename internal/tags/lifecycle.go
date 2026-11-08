// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// This file is the proposed -> applied/rejected lifecycle of a tag operation.
// Rename, Merge and Split above apply at once; here an operation is stored as
// proposed with a validated, normalized payload (tags as IDs, new split
// targets with pre-assigned IDs) and applied later by a different principal.

// errDryRun rolls a transaction back after its result has been read.
var errDryRun = errors.New("tags: dry run")

// ProposeInput is the body of a tag operation proposal. Payload is the
// kind-specific input (RenameInput, MergeInput or SplitInput as JSON).
type ProposeInput struct {
	Kind    OpKind          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	// Apply applies the operation at once. The caller must hold tags:apply;
	// the service does not check scopes.
	Apply bool `json:"apply"`
}

// TagEffect is one tag an operation touches, with problem counts around it.
type TagEffect struct {
	ID             domain.ID `json:"id"`
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Status         Status    `json:"status"`          // after the operation
	ProblemsBefore int       `json:"problems_before"` // 0 for a tag the operation creates
	ProblemsAfter  int       `json:"problems_after"`
	Created        bool      `json:"created"`
}

// Effect is what an operation does (preview) or did (result).
type Effect struct {
	// AffectedProblems lists, ascending, the problems whose tag mappings
	// change; mastery for them and the involved tags is rebuilt (M8.7).
	AffectedProblems []domain.ID `json:"affected_problems"`
	Tags             []TagEffect `json:"tags"`
}

// tagIDs lists the tags the operation touched, those it created included.
func (e Effect) tagIDs() []domain.ID {
	ids := make([]domain.ID, len(e.Tags))
	for i, t := range e.Tags {
		ids[i] = t.ID
	}
	return ids
}

// OpDetail is an operation with its effect: Preview for a proposed one,
// Result when this call applied it. PreviewError is set instead of Preview
// when a proposed operation no longer validates against the current state.
type OpDetail struct {
	Operation    Operation
	Preview      *Effect
	Result       *Effect
	PreviewError string
}

// runOut is what executing an operation produced.
type runOut struct {
	payload json.RawMessage // normalized
	effect  Effect
	snap    Snapshot // before-image of the existing tags involved
}

// Propose stores a proposed operation after validating it against the current
// state, or with in.Apply applies it in one transaction and stores it applied.
// Invalid input is a validation error and stores nothing.
func (s *Service) Propose(ctx context.Context, ownerID domain.ID, actor domain.Actor, in ProposeInput) (_ OpDetail, err error) {
	ctx, end := platform.TraceMethod(ctx, "tags.Propose")
	defer end(&err)
	if err := actor.Validate(); err != nil {
		return OpDetail{}, platform.Validation("invalid actor", platform.FieldError{Field: "actor", Detail: err.Error()})
	}
	if !in.Kind.Valid() {
		return OpDetail{}, platform.Validation("invalid operation", platform.FieldError{
			Field: "kind", Detail: "must be rename, merge or split"})
	}
	now := s.clock.Now()
	op := Operation{ID: s.ids.New(), OwnerID: ownerID, Kind: in.Kind, Status: OpProposed, ProposedBy: actor, CreatedAt: now}

	if in.Apply {
		var d OpDetail
		err := s.mutate(ctx, ownerID, func(r Repos) error {
			out, err := s.run(ctx, r, ownerID, actor, in.Kind, in.Payload, false)
			if err != nil {
				return err
			}
			op.Payload, op.Status, op.DecidedBy, op.DecidedAt, op.AppliedAt, op.Snapshot = out.payload, OpApplied, actor, now, now, &out.snap
			op.Result = &out.effect
			if err := r.Ops.Create(ctx, ownerID, op); err != nil {
				return err
			}
			if err := r.rebuild(ctx, ownerID, out.effect.tagIDs()); err != nil {
				return err
			}
			d = OpDetail{Operation: op, Result: &out.effect}
			return nil
		})
		return d, err
	}

	// Validate and preview by running the operation in a transaction that is
	// rolled back, so the rules are exactly the apply-time rules.
	var out runOut
	err = s.tx.InTx(ctx, func(r Repos) (err error) {
		if out, err = s.run(ctx, r, ownerID, actor, in.Kind, in.Payload, false); err != nil {
			return err
		}
		return errDryRun
	})
	if !errors.Is(err, errDryRun) {
		return OpDetail{}, err
	}
	op.Payload = out.payload
	if err := s.tx.InTx(ctx, func(r Repos) error { return r.Ops.Create(ctx, ownerID, op) }); err != nil {
		return OpDetail{}, err
	}
	return OpDetail{Operation: op, Preview: &out.effect}, nil
}

// GetOperation returns the operation. A proposed one carries a preview
// computed against the current state, or PreviewError when it no longer
// validates. An applied one carries the Result recorded when it was applied
// (nil for one applied before results were recorded).
func (s *Service) GetOperation(ctx context.Context, ownerID, id domain.ID) (_ OpDetail, err error) {
	ctx, end := platform.TraceMethod(ctx, "tags.GetOperation")
	defer end(&err)
	var d OpDetail
	err = s.tx.InTx(ctx, func(r Repos) error {
		op, err := r.Ops.Get(ctx, ownerID, id)
		if err != nil {
			return err
		}
		d = OpDetail{Operation: op}
		switch op.Status {
		case OpApplied:
			d.Result = op.Result
		case OpProposed:
			out, err := s.run(ctx, r, ownerID, op.ProposedBy, op.Kind, op.Payload, true)
			var p *platform.Problem
			switch {
			case errors.As(err, &p), errors.Is(err, ErrSlugTaken), errors.Is(err, ErrNotFound):
				d.PreviewError = previewError(err)
			case err != nil:
				return err
			default:
				d.Preview = &out.effect
			}
		}
		return errDryRun // read-only: discard the preview run
	})
	if !errors.Is(err, errDryRun) {
		return OpDetail{}, err
	}
	return d, nil
}

// ListOperationDetails is ListOperations with each operation's detail: the
// preview of every proposed one and the recorded result of every applied
// one. Each preview runs in its own rolled-back transaction, because a
// preview executes the operation and earlier ones must not leak into later
// ones.
func (s *Service) ListOperationDetails(ctx context.Context, ownerID domain.ID, status OpStatus, p platform.Page) (_ []OpDetail, _ string, err error) {
	ctx, end := platform.TraceMethod(ctx, "tags.ListOperationDetails")
	defer end(&err)
	ops, next, err := s.ListOperations(ctx, ownerID, status, p)
	if err != nil {
		return nil, "", err
	}
	out := make([]OpDetail, 0, len(ops))
	for _, op := range ops {
		d := OpDetail{Operation: op}
		switch op.Status {
		case OpApplied:
			d.Result = op.Result
		case OpProposed:
			if d, err = s.GetOperation(ctx, ownerID, op.ID); err != nil {
				return nil, "", err
			}
		}
		out = append(out, d)
	}
	return out, next, nil
}

func previewError(err error) string {
	var p *platform.Problem
	if errors.As(err, &p) {
		msg := p.Detail
		var msgSb205 strings.Builder
		for _, f := range p.Errors {
			msgSb205.WriteString("; " + f.Field + ": " + f.Detail)
		}
		msg += msgSb205.String()
		return strings.TrimPrefix(msg, "; ")
	}
	if errors.Is(err, ErrSlugTaken) {
		return "a slug is already in use"
	}
	return "a tag no longer exists"
}

// ListOperations returns one page of the owner's operations, optionally only
// those with status, and the cursor for the next page ("" on the last one).
func (s *Service) ListOperations(ctx context.Context, ownerID domain.ID, status OpStatus, p platform.Page) (_ []Operation, _ string, err error) {
	ctx, end := platform.TraceMethod(ctx, "tags.ListOperations")
	defer end(&err)
	if status != "" && !status.Valid() {
		return nil, "", platform.Validation("invalid status", platform.FieldError{
			Field: "status", Detail: "must be proposed, applied or rejected"})
	}
	after, err := parseCursor(p.Cursor)
	if err != nil {
		return nil, "", err
	}
	var out []Operation
	err = s.tx.InTx(ctx, func(r Repos) (err error) {
		out, err = r.Ops.List(ctx, ownerID, OpFilter{Status: status, After: after, Limit: p.Limit + 1})
		return err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > p.Limit {
		out = out[:p.Limit]
		next = out[len(out)-1].ID.String()
	}
	return out, next, nil
}

// ApplyOperation applies a proposed operation in one transaction: it captures
// the before-snapshot, runs the operation against the current state and
// records actor as the decider. A decided operation returns ErrOpNotProposed.
func (s *Service) ApplyOperation(ctx context.Context, ownerID domain.ID, actor domain.Actor, id domain.ID) (_ OpDetail, err error) {
	ctx, end := platform.TraceMethod(ctx, "tags.ApplyOperation")
	defer end(&err)
	if err := actor.Validate(); err != nil {
		return OpDetail{}, platform.Validation("invalid actor", platform.FieldError{Field: "actor", Detail: err.Error()})
	}
	var d OpDetail
	err = s.mutate(ctx, ownerID, func(r Repos) error {
		op, err := r.Ops.Get(ctx, ownerID, id)
		if err != nil {
			return err
		}
		if op.Status != OpProposed {
			return ErrOpNotProposed
		}
		out, err := s.run(ctx, r, ownerID, actor, op.Kind, op.Payload, true)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := r.Ops.Decide(ctx, ownerID, id, Decision{Status: OpApplied, DecidedBy: actor, DecidedAt: now,
			AppliedAt: now, Snapshot: &out.snap, Result: &out.effect}); err != nil {
			return err
		}
		if err := r.rebuild(ctx, ownerID, out.effect.tagIDs()); err != nil {
			return err
		}
		if op, err = r.Ops.Get(ctx, ownerID, id); err != nil {
			return err
		}
		d = OpDetail{Operation: op, Result: &out.effect}
		return nil
	})
	return d, err
}

// RejectOperation marks a proposed operation rejected by actor and changes
// nothing else. A decided operation returns ErrOpNotProposed.
func (s *Service) RejectOperation(ctx context.Context, ownerID domain.ID, actor domain.Actor, id domain.ID) (_ OpDetail, err error) {
	ctx, end := platform.TraceMethod(ctx, "tags.RejectOperation")
	defer end(&err)
	if err := actor.Validate(); err != nil {
		return OpDetail{}, platform.Validation("invalid actor", platform.FieldError{Field: "actor", Detail: err.Error()})
	}
	var d OpDetail
	err = s.tx.InTx(ctx, func(r Repos) error {
		if err := r.Ops.Decide(ctx, ownerID, id, Decision{Status: OpRejected, DecidedBy: actor, DecidedAt: s.clock.Now()}); err != nil {
			return err
		}
		op, err := r.Ops.Get(ctx, ownerID, id)
		d = OpDetail{Operation: op}
		return err
	})
	return d, err
}

// run executes an operation inside the caller's transaction: it resolves and
// snapshots the tags involved, applies the change, and returns the normalized
// payload and the effect. trusted payloads come from a stored proposal (they
// may fix the IDs of tags a split creates); untrusted ones come from a client.
func (s *Service) run(ctx context.Context, r Repos, ownerID domain.ID, actor domain.Actor, kind OpKind, raw json.RawMessage, trusted bool) (runOut, error) {
	switch kind {
	case OpRename:
		return s.runRename(ctx, r, ownerID, raw)
	case OpMerge:
		return s.runMerge(ctx, r, ownerID, raw)
	case OpSplit:
		return s.runSplit(ctx, r, ownerID, actor, raw, trusted)
	}
	return runOut{}, platform.Validation("invalid operation", platform.FieldError{Field: "kind", Detail: "must be rename, merge or split"})
}

func (s *Service) runRename(ctx context.Context, r Repos, ownerID domain.ID, raw json.RawMessage) (runOut, error) {
	var in RenameInput
	if err := decodePayload(raw, &in); err != nil {
		return runOut{}, err
	}
	if err := in.Validate(); err != nil {
		return runOut{}, err
	}
	b, err := s.begin(ctx, r, ownerID, []string{in.Tag})
	if err != nil {
		return runOut{}, err
	}
	if len(b.ids) == 0 {
		return runOut{}, platform.Validation("invalid rename", platform.FieldError{Field: "tag", Detail: "no such tag"})
	}
	t, err := ApplyRename(ctx, r.Tags, ownerID, s.clock.Now(), in)
	if err != nil {
		return runOut{}, err
	}
	in.Tag = t.ID.String()
	return s.finish(ctx, r, ownerID, b, nil, nil, in)
}

func (s *Service) runMerge(ctx context.Context, r Repos, ownerID domain.ID, raw json.RawMessage) (runOut, error) {
	var in MergeInput
	if err := decodePayload(raw, &in); err != nil {
		return runOut{}, err
	}
	if len(in.Sources) == 0 || len(in.Sources) > maxMergeSources {
		return runOut{}, platform.Validation("invalid merge", platform.FieldError{Field: "sources", Detail: "give 1 to 50 tags"})
	}
	b, err := s.begin(ctx, r, ownerID, append(slices.Clone(in.Sources), in.Target))
	if err != nil {
		return runOut{}, err
	}
	res, err := s.merge(ctx, r, ownerID, in)
	if err != nil {
		return runOut{}, err
	}
	norm := MergeInput{Target: res.Target.ID.String()}
	for _, t := range res.Sources {
		norm.Sources = append(norm.Sources, t.ID.String())
	}
	return s.finish(ctx, r, ownerID, b, nil, res.Affected, norm)
}

func (s *Service) runSplit(ctx context.Context, r Repos, ownerID domain.ID, actor domain.Actor, raw json.RawMessage, trusted bool) (runOut, error) {
	var in SplitInput
	if err := decodePayload(raw, &in); err != nil {
		return runOut{}, err
	}
	refs := []string{in.Source}
	for i := range in.Targets {
		tg := &in.Targets[i]
		if !trusted {
			tg.NewID = nil
		}
		if tg.New != nil && tg.NewID == nil {
			id := s.ids.New()
			tg.NewID = &id
		}
		if tg.Ref != "" {
			refs = append(refs, tg.Ref)
		}
	}
	b, err := s.begin(ctx, r, ownerID, refs)
	if err != nil {
		return runOut{}, err
	}
	run := splitRun{External: true, NoRecord: true}
	res, err := s.applySplit(ctx, r, ownerID, actor, in, &run)
	if err != nil {
		return runOut{}, err
	}
	norm := SplitInput{Source: run.payload.Source.String(), RetireSource: run.payload.RetireSource,
		Assignments: map[domain.ID][]string{}}
	for i, id := range run.payload.Targets {
		if slices.Contains(run.payload.Created, id) {
			t := res.Targets[i]
			norm.Targets = append(norm.Targets, SplitTarget{
				New: &CreateInput{Slug: t.Slug, Name: t.Name, Description: t.Description}, NewID: &id})
		} else {
			norm.Targets = append(norm.Targets, SplitTarget{Ref: id.String()})
		}
	}
	var affected []domain.ID
	for k, ids := range run.payload.Assignments {
		pid, err := domain.ParseID(k)
		if err != nil {
			return runOut{}, err
		}
		affected = append(affected, pid)
		for _, id := range ids {
			norm.Assignments[pid] = append(norm.Assignments[pid], id.String())
		}
	}
	slices.SortFunc(affected, compareIDs)
	return s.finish(ctx, r, ownerID, b, run.payload.Created, affected, norm)
}

// before is the state of an operation's existing tags ahead of the change.
type before struct {
	ids    []domain.ID // distinct existing tags the operation names
	counts map[domain.ID]int
	snap   Snapshot
}

// begin resolves refs (those that exist), counts their problems and captures
// the before-snapshot. Refs that do not resolve are left for the operation's
// own validation to report.
func (s *Service) begin(ctx context.Context, r Repos, ownerID domain.ID, refs []string) (before, error) {
	b := before{counts: map[domain.ID]int{}}
	for _, ref := range refs {
		t, err := Resolve(ctx, r.Tags, ownerID, ref)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return before{}, err
		}
		if slices.Contains(b.ids, t.ID) {
			continue
		}
		b.ids = append(b.ids, t.ID)
		if b.counts[t.ID], err = countProblems(ctx, r.Tags, ownerID, t.ID); err != nil {
			return before{}, err
		}
	}
	if len(b.ids) > 0 {
		var err error
		if b.snap, err = r.Ops.CaptureSnapshot(ctx, ownerID, b.ids); err != nil {
			return before{}, err
		}
	}
	return b, nil
}

// finish reads the tags after the change and assembles the run output.
func (s *Service) finish(ctx context.Context, r Repos, ownerID domain.ID, b before, created, affected []domain.ID, norm any) (runOut, error) {
	payload, err := json.Marshal(norm)
	if err != nil {
		return runOut{}, err
	}
	out := runOut{payload: payload, snap: b.snap, effect: Effect{AffectedProblems: affected}}
	if out.effect.AffectedProblems == nil {
		out.effect.AffectedProblems = []domain.ID{}
	}
	for _, id := range append(slices.Clone(b.ids), created...) {
		t, err := r.Tags.Get(ctx, ownerID, id)
		if err != nil {
			return runOut{}, err
		}
		n, err := countProblems(ctx, r.Tags, ownerID, id)
		if err != nil {
			return runOut{}, err
		}
		out.effect.Tags = append(out.effect.Tags, TagEffect{ID: id, Slug: t.Slug, Name: t.Name, Status: t.Status,
			ProblemsBefore: b.counts[id], ProblemsAfter: n, Created: slices.Contains(created, id)})
	}
	return out, nil
}

func countProblems(ctx context.Context, r Repo, ownerID, tagID domain.ID) (int, error) {
	n := 0
	for after := domain.Nil; ; {
		page, err := r.ProblemIDs(ctx, ownerID, tagID, after, 500)
		if err != nil {
			return 0, err
		}
		n += len(page)
		if len(page) < 500 {
			return n, nil
		}
		after = page[len(page)-1]
	}
}

// decodePayload strictly decodes a stored or submitted payload.
func decodePayload(raw json.RawMessage, v any) error {
	fail := func(detail string) error {
		return platform.Validation("invalid payload", platform.FieldError{Field: "payload", Detail: detail})
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fail("is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fail(err.Error())
	}
	return nil
}
