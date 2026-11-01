// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// RetentionRecomputer is the hook through which a change of desired retention
// reaches the user's cards.
//
// The spec requires that changing desired retention recomputes the affected
// user's cards ("Setting a late rating, changing desired retention, or a major
// problem edit recomputes the affected cards"). Cards are a derived fold over
// attempts and belong to the cards projection (M4.3), which does not exist
// yet. M4.3 implements this interface and passes it to Service via
// WithRetentionRecomputer; until then there are no cards to recompute and the
// hook is simply unset. There is deliberately no stand-in implementation.
//
// Contract for implementers:
//   - It is called after the new setting has been committed, with the new
//     effective retention, and only when the effective value actually changed.
//   - It must recompute every card owned by userID using retention, and must
//     be idempotent (a fold over attempts is), so a retry or
//     `quizzotic admin rebuild` converges.
//   - If it returns an error, Service.Update returns that error even though
//     the setting is already stored; the caller may retry the same update.
type RetentionRecomputer interface {
	RecomputeCards(ctx context.Context, userID domain.ID, retention float64) error
}

// Service holds the settings rules.
type Service struct {
	tx         Transactor
	defaults   Defaults
	recomputer RetentionRecomputer
}

// Option configures a Service.
type Option func(*Service)

// WithRetentionRecomputer installs the card recompute hook (see
// RetentionRecomputer).
func WithRetentionRecomputer(r RetentionRecomputer) Option {
	return func(s *Service) { s.recomputer = r }
}

// NewService builds a Service over the configured defaults.
func NewService(tx Transactor, defaults Defaults, opts ...Option) *Service {
	s := &Service{tx: tx, defaults: defaults}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Get returns the user's effective settings.
func (s *Service) Get(ctx context.Context, userID domain.ID) (Settings, error) {
	var out Settings
	err := s.tx.InTx(ctx, func(r Repos) error {
		o, err := r.Settings.Get(ctx, userID)
		if err != nil {
			return err
		}
		out = Resolve(s.defaults, o)
		return nil
	})
	return out, err
}

// StudyDay returns the user's current study day at now.
func (s *Service) StudyDay(ctx context.Context, userID domain.ID, now time.Time) (StudyDay, error) {
	cur, err := s.Get(ctx, userID)
	if err != nil {
		return StudyDay{}, err
	}
	return cur.StudyDay(now)
}

// Update stores o as the user's complete set of overrides (nil clears an
// override back to the default) and returns the new effective settings. It
// returns a *ValidationError (errors.Is ErrInvalid) for bad values. If the
// effective desired retention changed, the RetentionRecomputer hook, when
// installed, runs after commit.
func (s *Service) Update(ctx context.Context, userID domain.ID, o Overrides) (Settings, error) {
	if err := o.Validate(); err != nil {
		return Settings{}, err
	}
	var before, after Settings
	err := s.tx.InTx(ctx, func(r Repos) error {
		old, err := r.Settings.Get(ctx, userID)
		if err != nil {
			return err
		}
		before, after = Resolve(s.defaults, old), Resolve(s.defaults, o)
		return r.Settings.Replace(ctx, userID, o)
	})
	if err != nil {
		return Settings{}, err
	}
	if s.recomputer != nil && before.DesiredRetention != after.DesiredRetention {
		if err := s.recomputer.RecomputeCards(ctx, userID, after.DesiredRetention); err != nil {
			return after, errors.Join(errors.New("settings: stored, but recomputing cards failed"), err)
		}
	}
	return after, nil
}
