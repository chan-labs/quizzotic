// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// Projector maintains the card projection inside one transaction-bound Scope.
// It is the real attempts.CardUpdater and problems.MajorEditHandler: the store
// binds one to each attempt or edit transaction, so a card is written
// atomically with the change that caused it.
type Projector struct{ sc Scope }

var (
	_ attempts.CardUpdater      = (*Projector)(nil)
	_ problems.MajorEditHandler = (*Projector)(nil)
	_ problems.RetiredHandler   = (*Projector)(nil)
)

// NewProjector builds a Projector over sc.
func NewProjector(sc Scope) *Projector { return &Projector{sc} }

// Apply implements attempts.CardUpdater. It refolds the attempt's card from
// the stored attempt log, so the rating argument is not used: the effective
// rating is derived from what is stored (see EffectiveRating), and a late
// rating is picked up because the repo has already filled it in. It returns
// the card's new due time, or nil when the card was left alone (the problem is
// no longer live, or the attempt belongs to an older epoch).
func (p *Projector) Apply(ctx context.Context, a attempts.Attempt, _ int) (*time.Time, error) {
	c, err := p.Recompute(ctx, a.UserID, a.ProblemID, a.CardEpoch, nil)
	if err != nil || c == nil {
		return nil, err
	}
	return &c.Due, nil
}

// ProblemMajorEdit implements problems.MajorEditHandler. A major edit starts a
// new card epoch, so the old epoch's card is dropped; the new epoch has no
// attempts yet and gets its card with its first answer. The old attempts stay
// in the log, attached to the old epoch.
func (p *Projector) ProblemMajorEdit(ctx context.Context, e problems.MajorEdit) error {
	return p.sc.Cards.Delete(ctx, e.OwnerID, e.ProblemID)
}

// ProblemRetired implements problems.RetiredHandler: a card exists only for a
// live problem, so archiving or deleting one drops its card.
func (p *Projector) ProblemRetired(ctx context.Context, ownerID, problemID domain.ID) error {
	return p.sc.Cards.Delete(ctx, ownerID, problemID)
}

// Recompute refolds the card for (user, problem) at epoch from the attempt log
// and stores it, or removes it when no attempt is scheduled. Only a live
// problem at exactly this epoch is touched: an older epoch (a late rating after
// a major edit) and a draft, archived, deleted or missing problem leave the
// stored card as it is and return nil. retention, when set, overrides the
// user's desired retention.
func (p *Projector) Recompute(ctx context.Context, userID, problemID domain.ID, epoch int, retention *float64) (*Card, error) {
	pe, err := p.sc.Problems.Epoch(ctx, userID, problemID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !pe.Live || pe.Epoch != epoch {
		return nil, nil
	}
	return p.fold(ctx, userID, problemID, epoch, retention)
}

func (p *Projector) fold(ctx context.Context, userID, problemID domain.ID, epoch int, retention *float64) (*Card, error) {
	params, err := p.sc.Params.Params(ctx, userID)
	if err != nil {
		return nil, err
	}
	if retention != nil {
		params.DesiredRetention = *retention
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	log, err := p.sc.Attempts.ForCard(ctx, userID, problemID, epoch)
	if err != nil {
		return nil, err
	}
	c, ok := Fold(params, log)
	if !ok {
		return nil, p.sc.Cards.Delete(ctx, userID, problemID)
	}
	c.UserID, c.ProblemID, c.Epoch = userID, problemID, epoch
	if err := p.sc.Cards.Put(ctx, userID, c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Rebuild drops every card of the user and refolds them from the attempt log:
// one card for each live problem that has a scheduled attempt at its current
// epoch. retention, when set, overrides the user's desired retention. It is
// the whole projection; incremental updates must always agree with it.
func (p *Projector) Rebuild(ctx context.Context, userID domain.ID, retention *float64) error {
	if err := p.sc.Cards.DeleteAll(ctx, userID); err != nil {
		return err
	}
	live, err := p.sc.Problems.LiveEpochs(ctx, userID)
	if err != nil {
		return err
	}
	for _, pe := range live {
		if _, err := p.fold(ctx, userID, pe.ID, pe.Epoch, retention); err != nil {
			return fmt.Errorf("review: card for problem %s: %w", pe.ID, err)
		}
	}
	return nil
}

// Service runs the projection's own operations, each in one transaction.
type Service struct{ tx Transactor }

// NewService builds a Service over tx.
func NewService(tx Transactor) *Service { return &Service{tx} }

// RebuildUser drops and rebuilds all of one user's cards.
func (s *Service) RebuildUser(ctx context.Context, userID domain.ID) error {
	return s.tx.InTx(ctx, func(sc Scope) error { return NewProjector(sc).Rebuild(ctx, userID, nil) })
}

// RecomputeCards recomputes every card of the user with the given desired
// retention. It implements settings.RetentionRecomputer: the settings service
// calls it after a change of retention has been stored.
func (s *Service) RecomputeCards(ctx context.Context, userID domain.ID, retention float64) error {
	return s.tx.InTx(ctx, func(sc Scope) error { return NewProjector(sc).Rebuild(ctx, userID, &retention) })
}
