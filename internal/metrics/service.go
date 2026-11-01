// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Projector maintains the mastery projection inside one transaction-bound
// Scope. The store binds one to each attempt transaction, so mastery is
// written atomically with the attempt and card that cause it.
type Projector struct {
	sc    Scope
	clock domain.Clock
}

// NewProjector builds a Projector over sc that predicts recall at clock.Now().
func NewProjector(sc Scope, clock domain.Clock) *Projector { return &Projector{sc, clock} }

// UpdateProblem recomputes the mastery rows of every tag on the problem. It is
// the incremental update after an attempt (or a card change) on the problem;
// the card projection must already reflect the change.
func (p *Projector) UpdateProblem(ctx context.Context, ownerID, problemID domain.ID) error {
	ids, err := p.sc.Repo.ProblemTagIDs(ctx, ownerID, problemID)
	if err != nil || len(ids) == 0 {
		return err
	}
	return p.UpdateTags(ctx, ownerID, ids)
}

// UpdateTags recomputes the rows of the given tags. A tag that is no longer
// active (merged, retired, or gone) has its row removed.
func (p *Projector) UpdateTags(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) error {
	active, err := p.sc.Repo.ActiveTagIDs(ctx, ownerID)
	if err != nil {
		return err
	}
	isActive := make(map[domain.ID]bool, len(active))
	for _, id := range active {
		isActive[id] = true
	}
	var want []domain.ID
	seen := map[domain.ID]bool{}
	for _, id := range tagIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if isActive[id] {
			want = append(want, id)
		} else if err := p.sc.Repo.Delete(ctx, ownerID, id); err != nil {
			return err
		}
	}
	if len(want) == 0 {
		return nil
	}
	return p.compute(ctx, ownerID, want)
}

// Rebuild drops every mastery row of the owner and recomputes one row for each
// active tag. It is the whole projection; incremental updates must always
// agree with it.
func (p *Projector) Rebuild(ctx context.Context, ownerID domain.ID) error {
	if err := p.sc.Repo.DeleteAll(ctx, ownerID); err != nil {
		return err
	}
	active, err := p.sc.Repo.ActiveTagIDs(ctx, ownerID)
	if err != nil || len(active) == 0 {
		return err
	}
	return p.compute(ctx, ownerID, active)
}

// compute writes a row for each of the active tags in ids.
func (p *Projector) compute(ctx context.Context, ownerID domain.ID, ids []domain.ID) error {
	params, err := p.sc.Params.Params(ctx, ownerID)
	if err != nil {
		return err
	}
	if err := params.Validate(); err != nil {
		return err
	}
	cards, err := p.sc.Repo.TagCards(ctx, ownerID, ids)
	if err != nil {
		return err
	}
	last, err := p.sc.Repo.LastAttempts(ctx, ownerID, ids)
	if err != nil {
		return err
	}
	byTag := make(map[domain.ID][]TagCard, len(ids))
	for _, tc := range cards {
		byTag[tc.TagID] = append(byTag[tc.TagID], tc)
	}
	now := p.clock.Now()
	for _, id := range ids {
		m := Compute(params, now, ownerID, id, byTag[id], last[id])
		if err := p.sc.Repo.Put(ctx, ownerID, m); err != nil {
			return err
		}
	}
	return nil
}

// Service runs the projection's own operations, each in one transaction.
type Service struct {
	tx    Transactor
	clock domain.Clock
}

// NewService builds a Service over tx.
func NewService(tx Transactor, clock domain.Clock) *Service { return &Service{tx, clock} }

// RebuildUser drops and recomputes all of one user's mastery rows.
func (s *Service) RebuildUser(ctx context.Context, userID domain.ID) error {
	return s.tx.InTx(ctx, func(sc Scope) error { return NewProjector(sc, s.clock).Rebuild(ctx, userID) })
}
