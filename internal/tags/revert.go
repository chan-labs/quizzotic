// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// RevertOperation restores the before-snapshot of an applied operation in one
// transaction: the tags it recorded get their rows, aliases and problem
// mappings back, and tags the operation created (a split's new targets) are
// removed. It returns ErrOpNotApplied for a proposed or rejected operation,
// ErrOpReverted when it was reverted already and ErrRevertConflict when a
// later applied, not reverted operation touched any of the same tags. Mastery
// and history of every tag involved are rebuilt in the same transaction.
func (s *Service) RevertOperation(ctx context.Context, ownerID domain.ID, actor domain.Actor, id domain.ID) (OpDetail, error) {
	if err := actor.Validate(); err != nil {
		return OpDetail{}, platform.Validation("invalid actor", platform.FieldError{Field: "actor", Detail: err.Error()})
	}
	var d OpDetail
	err := s.mutate(ctx, ownerID, func(r Repos) error {
		op, err := r.Ops.Get(ctx, ownerID, id)
		if err != nil {
			return err
		}
		if op.Status != OpApplied {
			return ErrOpNotApplied
		}
		if !op.RevertedAt.IsZero() {
			return ErrOpReverted
		}
		if op.Snapshot == nil {
			return ErrOpNotApplied
		}
		created, err := createdTagIDs(op)
		if err != nil {
			return err
		}
		mine := opTagIDs(op, created)

		for after := domain.Nil; ; {
			page, err := r.Ops.List(ctx, ownerID, OpFilter{Status: OpApplied, After: after, Limit: 200})
			if err != nil {
				return err
			}
			for _, o := range page {
				if o.ID == op.ID || !o.RevertedAt.IsZero() || !appliedAfter(o, op) {
					continue
				}
				oc, err := createdTagIDs(o)
				if err != nil {
					return err
				}
				if slices.ContainsFunc(opTagIDs(o, oc), func(t domain.ID) bool { return slices.Contains(mine, t) }) {
					return ErrRevertConflict
				}
			}
			if len(page) < 200 {
				break
			}
			after = page[len(page)-1].ID
		}

		if err := r.forget(ctx, ownerID, created); err != nil {
			return err
		}
		if err := r.Ops.Restore(ctx, ownerID, *op.Snapshot, created); err != nil {
			return err
		}
		if err := r.Ops.MarkReverted(ctx, ownerID, id, actor, s.clock.Now()); err != nil {
			return err
		}
		if err := r.rebuild(ctx, ownerID, mine); err != nil {
			return err
		}
		op, err = r.Ops.Get(ctx, ownerID, id)
		d = OpDetail{Operation: op}
		return err
	})
	return d, err
}

// appliedAfter reports whether o was applied after op (ties broken by ID).
func appliedAfter(o, op Operation) bool {
	if o.AppliedAt.Equal(op.AppliedAt) {
		return compareIDs(o.ID, op.ID) > 0
	}
	return o.AppliedAt.After(op.AppliedAt)
}

// opTagIDs is every tag an applied operation touched: those in its snapshot
// plus those it created.
func opTagIDs(op Operation, created []domain.ID) []domain.ID {
	var ids []domain.ID
	if op.Snapshot != nil {
		for _, t := range op.Snapshot.Tags {
			ids = append(ids, t.ID)
		}
	}
	return append(ids, created...)
}

// createdTagIDs returns the IDs of tags a split created. A split recorded
// directly by Service.Split stores them in "created"; one that went through
// the proposal lifecycle has a normalized payload with "new_id" on each new
// target.
func createdTagIDs(op Operation) ([]domain.ID, error) {
	if op.Kind != OpSplit {
		return nil, nil
	}
	var p struct {
		Created []domain.ID       `json:"created"`
		Targets []json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal(op.Payload, &p); err != nil {
		return nil, err
	}
	out := slices.Clone(p.Created)
	for _, raw := range p.Targets {
		var t struct {
			NewID *domain.ID `json:"new_id"`
		}
		if json.Unmarshal(raw, &t) != nil || t.NewID == nil { // a plain ID string is an existing target
			continue
		}
		if !slices.Contains(out, *t.NewID) {
			out = append(out, *t.NewID)
		}
	}
	return out, nil
}
