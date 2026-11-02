// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
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
	if err := p.sc.Cards.LockOwner(ctx, e.OwnerID); err != nil {
		return err
	}
	return p.sc.Cards.Delete(ctx, e.OwnerID, e.ProblemID)
}

// ProblemRetired implements problems.RetiredHandler: a card exists only for a
// live problem, so archiving or deleting one drops its card.
func (p *Projector) ProblemRetired(ctx context.Context, ownerID, problemID domain.ID) error {
	if err := p.sc.Cards.LockOwner(ctx, ownerID); err != nil {
		return err
	}
	return p.sc.Cards.Delete(ctx, ownerID, problemID)
}

// Recompute refolds the card for (user, problem) at epoch from the attempt log
// and stores it, or removes it when no attempt is scheduled. Only a live
// problem at exactly this epoch is touched: an older epoch (a late rating after
// a major edit) and a draft, archived, deleted or missing problem leave the
// stored card as it is and return nil. retention, when set, overrides the
// user's desired retention.
func (p *Projector) Recompute(ctx context.Context, userID, problemID domain.ID, epoch int, retention *float64) (*Card, error) {
	// Before reading the epoch and the log: a concurrent attempt or edit of
	// this owner must finish first, or its change would be missed.
	if err := p.sc.Cards.LockOwner(ctx, userID); err != nil {
		return nil, err
	}
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
	_, err := p.rebuild(ctx, userID, retention)
	return err
}

// rebuild is Rebuild reporting how many cards it wrote.
func (p *Projector) rebuild(ctx context.Context, userID domain.ID, retention *float64) (int, error) {
	if err := p.sc.Cards.LockOwner(ctx, userID); err != nil {
		return 0, err
	}
	if err := p.sc.Cards.DeleteAll(ctx, userID); err != nil {
		return 0, err
	}
	live, err := p.sc.Problems.LiveEpochs(ctx, userID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, pe := range live {
		c, err := p.fold(ctx, userID, pe.ID, pe.Epoch, retention)
		if err != nil {
			return 0, fmt.Errorf("review: card for problem %s: %w", pe.ID, err)
		}
		if c != nil {
			n++
		}
	}
	return n, nil
}

// DefaultRebuildBatch is how many users RebuildAll reads and logs at a time.
const DefaultRebuildBatch = 100

// Service runs the projection's own operations, each in one transaction.
type Service struct {
	tx     Transactor
	users  auth.UserRepo
	logger *slog.Logger
	batch  int
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithUsers gives the Service the user repo that RebuildAll and RebuildOne
// need to visit and check users.
func WithUsers(u auth.UserRepo) ServiceOption { return func(s *Service) { s.users = u } }

// WithLogger sets where rebuild progress is logged; the default is slog.Default().
func WithLogger(l *slog.Logger) ServiceOption { return func(s *Service) { s.logger = l } }

// WithBatchSize sets how many users RebuildAll reads per page and logs
// progress for; n <= 0 keeps DefaultRebuildBatch.
func WithBatchSize(n int) ServiceOption {
	return func(s *Service) {
		if n > 0 {
			s.batch = n
		}
	}
}

// NewService builds a Service over tx.
func NewService(tx Transactor, opts ...ServiceOption) *Service {
	s := &Service{tx: tx, logger: slog.Default(), batch: DefaultRebuildBatch}
	for _, o := range opts {
		o(s)
	}
	return s
}

// RebuildUser drops and rebuilds all of one user's cards in one transaction
// and returns how many cards it wrote.
func (s *Service) RebuildUser(ctx context.Context, userID domain.ID) (int, error) {
	var n int
	err := s.tx.InTx(ctx, func(sc Scope) error {
		var err error
		n, err = NewProjector(sc).rebuild(ctx, userID, nil)
		return err
	})
	return n, err
}

// RebuildSummary is what a rebuild did.
type RebuildSummary struct {
	Users int `json:"users"` // users rebuilt
	Cards int `json:"cards"` // cards written
}

// RebuildOne rebuilds one existing user's cards, or returns ErrNotFound when
// there is no such user.
func (s *Service) RebuildOne(ctx context.Context, userID domain.ID) (RebuildSummary, error) {
	if s.users == nil {
		return RebuildSummary{}, errors.New("review: rebuild needs a user repo (WithUsers)")
	}
	if _, err := s.users.Get(ctx, userID); errors.Is(err, auth.ErrNotFound) {
		return RebuildSummary{}, ErrNotFound
	} else if err != nil {
		return RebuildSummary{}, err
	}
	s.logger.Info("rebuild started", slog.String("user", userID.String()))
	n, err := s.RebuildUser(ctx, userID)
	if err != nil {
		return RebuildSummary{}, fmt.Errorf("review: rebuild user %s: %w", userID, err)
	}
	s.logger.Info("rebuild finished", slog.Int("users", 1), slog.Int("cards", n))
	return RebuildSummary{Users: 1, Cards: n}, nil
}

// RebuildAll rebuilds every user's cards from the attempt log. Users are read
// in pages of the batch size, each user is rebuilt in its own transaction
// (a failure stops the run and leaves later users untouched; rerunning is
// safe because a rebuild is idempotent), and progress is logged after each
// batch. The summary counts what was done even when an error is returned.
func (s *Service) RebuildAll(ctx context.Context) (RebuildSummary, error) {
	if s.users == nil {
		return RebuildSummary{}, errors.New("review: rebuild needs a user repo (WithUsers)")
	}
	var sum RebuildSummary
	s.logger.Info("rebuild started", slog.Int("batch_size", s.batch))
	var after domain.ID
	for batchNo := 1; ; batchNo++ {
		ids, err := s.users.ListIDs(ctx, after, s.batch)
		if err != nil {
			return sum, fmt.Errorf("review: list users: %w", err)
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return sum, err
			}
			n, err := s.RebuildUser(ctx, id)
			if err != nil {
				return sum, fmt.Errorf("review: rebuild user %s: %w", id, err)
			}
			sum.Users++
			sum.Cards += n
		}
		if len(ids) > 0 {
			after = ids[len(ids)-1]
			s.logger.Info("rebuild progress", slog.Int("batch", batchNo),
				slog.Int("users", sum.Users), slog.Int("cards", sum.Cards))
		}
		if len(ids) < s.batch {
			break
		}
	}
	s.logger.Info("rebuild finished", slog.Int("users", sum.Users), slog.Int("cards", sum.Cards))
	return sum, nil
}

// RecomputeCards recomputes every card of the user with the given desired
// retention. It implements settings.RetentionRecomputer: the settings service
// calls it after a change of retention has been stored.
func (s *Service) RecomputeCards(ctx context.Context, userID domain.ID, retention float64) error {
	return s.tx.InTx(ctx, func(sc Scope) error { return NewProjector(sc).Rebuild(ctx, userID, &retention) })
}
