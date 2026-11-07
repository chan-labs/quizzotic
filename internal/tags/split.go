// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// SplitTarget is one tag a split moves problems to: either an existing tag
// (Ref is its ID, slug or alias) or a new tag to create (New). Exactly one is
// set.
type SplitTarget struct {
	Ref string       `json:"ref,omitempty"`
	New *CreateInput `json:"new,omitempty"`
	// NewID is the ID the new tag is created with; one is assigned when nil.
	// A proposal stores it so applying creates the tag the preview showed.
	NewID *domain.ID `json:"new_id,omitempty"`
}

// SplitInput is the input of a split operation.
type SplitInput struct {
	// Source is the ID, slug or alias of the active tag to split.
	Source  string        `json:"source"`
	Targets []SplitTarget `json:"targets"`
	// Assignments maps a problem carrying the source tag to the targets it
	// moves to. Each entry names a target by its ID or slug (for a new
	// target, the slug it is created with). A problem with no assignment
	// keeps the source tag unless RetireSource is set.
	Assignments map[domain.ID][]string `json:"assignments"`
	// RetireSource retires the source tag after the move. Every problem that
	// carries the source must then have at least one assignment.
	RetireSource bool `json:"retire_source"`
}

// SplitResult describes an applied split.
type SplitResult struct {
	Operation Operation
	Source    Tag   // after the split
	Targets   []Tag // in input order
}

type splitPayload struct {
	Source       domain.ID              `json:"source"`
	Targets      []domain.ID            `json:"targets"`
	Created      []domain.ID            `json:"created"`
	Assignments  map[string][]domain.ID `json:"assignments"`
	RetireSource bool                   `json:"retire_source"`
}

// Split applies a split at once in one transaction: it creates the new
// targets, moves each assigned problem from the source to its targets,
// retires the source when asked, and records an applied TagOperation holding
// the before-snapshot of the source and the existing targets, attributed to
// actor. Invalid input is a validation error and changes nothing.
func (s *Service) Split(ctx context.Context, ownerID domain.ID, actor domain.Actor, in SplitInput) (_ SplitResult, err error) {
	ctx, end := platform.TraceMethod(ctx, "tags.Split")
	defer end(&err)
	if err := actor.Validate(); err != nil {
		return SplitResult{}, platform.Validation("invalid actor", platform.FieldError{Field: "actor", Detail: err.Error()})
	}
	var res SplitResult
	err = s.mutate(ctx, ownerID, func(r Repos) (err error) {
		if res, err = s.applySplit(ctx, r, ownerID, actor, in, &splitRun{}); err != nil {
			return err
		}
		ids := []domain.ID{res.Source.ID}
		for _, t := range res.Targets {
			ids = append(ids, t.ID)
		}
		return r.rebuild(ctx, ownerID, ids)
	})
	if err != nil {
		return SplitResult{}, err
	}
	return res, nil
}

// splitRun tunes applySplit. The zero value snapshots and records the
// operation. The proposal lifecycle sets External (it captured the snapshot
// itself) and NoRecord (it owns the operation row) and reads the resolved
// payload back.
type splitRun struct {
	External, NoRecord bool
	payload            splitPayload
}

func (s *Service) applySplit(ctx context.Context, r Repos, ownerID domain.ID, actor domain.Actor, in SplitInput, run *splitRun) (SplitResult, error) {
	fail := func(field, detail string) error {
		return platform.Validation("invalid split", platform.FieldError{Field: field, Detail: detail})
	}
	source, err := Resolve(ctx, r.Tags, ownerID, in.Source)
	if errors.Is(err, ErrNotFound) {
		return SplitResult{}, fail("source", "no such tag")
	}
	if err != nil {
		return SplitResult{}, err
	}
	if source.Status != StatusActive {
		return SplitResult{}, fail("source", "tag is "+string(source.Status))
	}
	if len(in.Targets) == 0 {
		return SplitResult{}, fail("targets", "at least one target is required")
	}

	// Resolve targets. byKey maps the names an assignment may use to an index.
	type target struct {
		tag   Tag
		isNew bool
	}
	var (
		targets []target
		byKey   = map[string]int{}
		seen    = map[domain.ID]bool{source.ID: true}
		slugs   = map[string]bool{}
		fe      []platform.FieldError
	)
	now := s.clock.Now()
	for i, tg := range in.Targets {
		field := fmt.Sprintf("targets[%d]", i)
		switch {
		case (tg.Ref == "") == (tg.New == nil):
			fe = append(fe, platform.FieldError{Field: field, Detail: "exactly one of ref and new is required"})
		case tg.New != nil:
			name := strings.TrimSpace(tg.New.Name)
			slug := strings.ToLower(strings.TrimSpace(tg.New.Slug))
			if slug == "" {
				slug = Slugify(name)
			}
			bad := append(append(checkName(name), checkSlug(slug)...), checkDescription(tg.New.Description)...)
			if len(bad) > 0 {
				for _, b := range bad {
					fe = append(fe, platform.FieldError{Field: field + "." + b.Field, Detail: b.Detail})
				}
				continue
			}
			if slugs[slug] || slug == source.Slug {
				fe = append(fe, platform.FieldError{Field: field, Detail: "slug " + slug + " repeats another target or the source"})
				continue
			}
			slugs[slug] = true
			id := s.ids.New()
			if tg.NewID != nil {
				id = *tg.NewID
			}
			t := Tag{ID: id, OwnerID: ownerID, Slug: slug, Name: name, Description: tg.New.Description,
				Status: StatusActive, Aliases: []string{}, CreatedAt: now, UpdatedAt: now}
			byKey[slug] = len(targets)
			byKey[t.ID.String()] = len(targets)
			targets = append(targets, target{tag: t, isNew: true})
		default:
			t, err := Resolve(ctx, r.Tags, ownerID, tg.Ref)
			if errors.Is(err, ErrNotFound) {
				fe = append(fe, platform.FieldError{Field: field, Detail: "no such tag"})
				continue
			}
			if err != nil {
				return SplitResult{}, err
			}
			switch {
			case t.Status != StatusActive:
				fe = append(fe, platform.FieldError{Field: field, Detail: "tag is " + string(t.Status)})
			case seen[t.ID]:
				fe = append(fe, platform.FieldError{Field: field, Detail: "tag is the source or repeats another target"})
			default:
				seen[t.ID] = true
				byKey[t.Slug] = len(targets)
				byKey[t.ID.String()] = len(targets)
				targets = append(targets, target{tag: t})
			}
		}
	}
	if len(fe) > 0 {
		return SplitResult{}, platform.Validation("invalid split", fe...)
	}

	// The problems currently carrying the source.
	onSource := map[domain.ID]bool{}
	var after domain.ID
	for {
		page, err := r.Tags.ProblemIDs(ctx, ownerID, source.ID, after, 500)
		if err != nil {
			return SplitResult{}, err
		}
		for _, id := range page {
			onSource[id] = true
			after = id
		}
		if len(page) < 500 {
			break
		}
	}

	// Check assignments.
	moves := map[domain.ID][]int{} // problem -> target indexes
	for pid, refs := range in.Assignments {
		field := "assignments." + pid.String()
		if !onSource[pid] {
			fe = append(fe, platform.FieldError{Field: field, Detail: "problem does not carry the source tag"})
			continue
		}
		for _, ref := range refs {
			i, ok := byKey[strings.ToLower(ref)]
			if !ok {
				fe = append(fe, platform.FieldError{Field: field, Detail: "unknown target " + ref})
				continue
			}
			if !slices.Contains(moves[pid], i) {
				moves[pid] = append(moves[pid], i)
			}
		}
	}
	if in.RetireSource {
		for pid := range onSource {
			if len(moves[pid]) == 0 && !hasErr(fe, "assignments."+pid.String()) {
				fe = append(fe, platform.FieldError{Field: "assignments." + pid.String(),
					Detail: "problem has no assignment; retiring the source requires every problem to be assigned"})
			}
		}
	}
	if len(fe) > 0 {
		slices.SortFunc(fe, func(a, b platform.FieldError) int { return strings.Compare(a.Field, b.Field) })
		return SplitResult{}, platform.Validation("invalid split", fe...)
	}

	// Before-snapshot: the source and the targets that already exist.
	snapIDs := []domain.ID{source.ID}
	for _, t := range targets {
		if !t.isNew {
			snapIDs = append(snapIDs, t.tag.ID)
		}
	}
	var snap Snapshot
	if !run.External {
		if snap, err = r.Ops.CaptureSnapshot(ctx, ownerID, snapIDs); err != nil {
			return SplitResult{}, err
		}
	}

	// Apply.
	pl := splitPayload{Source: source.ID, Targets: []domain.ID{}, Created: []domain.ID{},
		Assignments: map[string][]domain.ID{}, RetireSource: in.RetireSource}
	for _, t := range targets {
		pl.Targets = append(pl.Targets, t.tag.ID)
		if t.isNew {
			pl.Created = append(pl.Created, t.tag.ID)
			if err := r.Tags.Create(ctx, ownerID, t.tag); err != nil {
				return SplitResult{}, err
			}
		}
	}
	pids := make([]domain.ID, 0, len(moves))
	for pid := range moves {
		pids = append(pids, pid)
	}
	slices.SortFunc(pids, func(a, b domain.ID) int { return strings.Compare(a.String(), b.String()) })
	for _, pid := range pids {
		cur, err := r.Tags.ProblemTags(ctx, ownerID, pid)
		if err != nil {
			return SplitResult{}, err
		}
		var next []domain.ID
		for _, t := range cur {
			if t.ID != source.ID {
				next = append(next, t.ID)
			}
		}
		for _, i := range moves[pid] {
			id := targets[i].tag.ID
			next = append(next, id)
			pl.Assignments[pid.String()] = append(pl.Assignments[pid.String()], id)
		}
		if err := r.Tags.SetProblemTags(ctx, ownerID, pid, next); err != nil {
			return SplitResult{}, err
		}
	}
	if in.RetireSource {
		if err := r.Tags.Retire(ctx, ownerID, source.ID, now); err != nil {
			return SplitResult{}, err
		}
	}
	run.payload = pl
	var op Operation
	if !run.NoRecord {
		payload, err := json.Marshal(pl)
		if err != nil {
			return SplitResult{}, err
		}
		op = Operation{ID: s.ids.New(), OwnerID: ownerID, Kind: OpSplit, Payload: payload, Status: OpApplied,
			ProposedBy: actor, DecidedBy: actor, AppliedAt: now, Snapshot: &snap, CreatedAt: now, DecidedAt: now}
		if err := r.Ops.Create(ctx, ownerID, op); err != nil {
			return SplitResult{}, err
		}
	}

	res := SplitResult{Operation: op}
	if res.Source, err = r.Tags.Get(ctx, ownerID, source.ID); err != nil {
		return SplitResult{}, err
	}
	for _, t := range targets {
		got, err := r.Tags.Get(ctx, ownerID, t.tag.ID)
		if err != nil {
			return SplitResult{}, err
		}
		res.Targets = append(res.Targets, got)
	}
	return res, nil
}

func hasErr(fe []platform.FieldError, field string) bool {
	return slices.ContainsFunc(fe, func(e platform.FieldError) bool { return e.Field == field })
}
