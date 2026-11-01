// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// The review service is the card recompute hook settings.Service takes. It is
// deliberately not installed here: the server wires the settings service with
// the settings UI (M5.7).
var _ settings.RetentionRecomputer = (*review.Service)(nil)

// Cards wires the card projection (internal/review) into store transactions:
// it binds a review.Scope, and the hooks built on it, to a transaction-bound
// Store so the projection is written atomically with the change that causes
// it. It carries the scheduling configuration the projection needs.
type Cards struct {
	defaults settings.Defaults
	base     fsrs.Params
}

// NewCards builds the wiring from the configured scheduling defaults: the
// default desired retention and the learning steps (also used for relearning).
func NewCards(c config.Scheduling) Cards {
	base := fsrs.Default()
	if c.DesiredRetention > 0 {
		base.DesiredRetention = c.DesiredRetention
	}
	if len(c.LearningSteps) > 0 {
		steps := make([]time.Duration, len(c.LearningSteps))
		for i, d := range c.LearningSteps {
			steps[i] = d.Std()
		}
		base.LearningSteps, base.RelearningSteps = steps, steps
	}
	d := settings.DefaultsFromConfig(c)
	d.DesiredRetention = base.DesiredRetention
	return Cards{defaults: d, base: base}
}

// DefaultCards is NewCards over the built-in configuration defaults.
func DefaultCards() Cards { return NewCards(config.Default().Scheduling) }

// Scope binds the projection to tx.
func (c Cards) Scope(tx Store) review.Scope {
	return review.Scope{
		Cards:    tx.Cards(),
		Attempts: tx.Attempts(),
		Problems: problemEpochs{tx.Problems()},
		Params:   paramsSource{tx: tx, c: c},
	}
}

// Projector is the projection bound to tx.
func (c Cards) Projector(tx Store) *review.Projector { return review.NewProjector(c.Scope(tx)) }

// CardUpdater is the attempts hook bound to tx.
func (c Cards) CardUpdater(tx Store) attempts.CardUpdater { return c.Projector(tx) }

// MajorEdit is the problems major-edit hook bound to tx.
func (c Cards) MajorEdit(tx Store) problems.MajorEditHandler { return c.Projector(tx) }

// Service is the review service over s: RebuildUser and RecomputeCards, each
// in one transaction, and RebuildAll / RebuildOne over s's users. opts are
// applied after the store's own wiring (for example review.WithLogger).
func (c Cards) Service(s Store, opts ...review.ServiceOption) *review.Service {
	return review.NewService(reviewTx{s, c}, append([]review.ServiceOption{review.WithUsers(s.Users())}, opts...)...)
}

type reviewTx struct {
	s Store
	c Cards
}

func (a reviewTx) InTx(ctx context.Context, fn func(review.Scope) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(a.c.Scope(tx)) })
}

// problemEpochs answers review.ProblemSource from a problems repo.
type problemEpochs struct{ r problems.Repo }

func (p problemEpochs) epoch(ctx context.Context, ownerID domain.ID, pr problems.Problem) (review.ProblemEpoch, error) {
	vs, err := p.r.ListVersions(ctx, ownerID, pr.ID)
	if err != nil {
		return review.ProblemEpoch{}, err
	}
	return review.ProblemEpoch{ID: pr.ID, Live: pr.Status == problems.StatusLive,
		Epoch: problems.CardEpoch(vs, pr.CurrentVersion)}, nil
}

func (p problemEpochs) Epoch(ctx context.Context, ownerID, id domain.ID) (review.ProblemEpoch, error) {
	pr, err := p.r.Get(ctx, ownerID, id)
	if errors.Is(err, problems.ErrNotFound) {
		return review.ProblemEpoch{}, review.ErrNotFound
	}
	if err != nil {
		return review.ProblemEpoch{}, err
	}
	return p.epoch(ctx, ownerID, pr)
}

func (p problemEpochs) LiveEpochs(ctx context.Context, ownerID domain.ID) ([]review.ProblemEpoch, error) {
	live, err := p.r.List(ctx, ownerID, problems.ListFilter{Status: problems.StatusLive})
	if err != nil {
		return nil, err
	}
	out := make([]review.ProblemEpoch, 0, len(live))
	for _, pr := range live {
		pe, err := p.epoch(ctx, ownerID, pr)
		if err != nil {
			return nil, err
		}
		out = append(out, pe)
	}
	return out, nil
}

// paramsSource resolves a user's scheduler parameters: the configured base
// with the user's desired-retention override and learning-step override.
type paramsSource struct {
	tx Store
	c  Cards
}

func (p paramsSource) Params(ctx context.Context, userID domain.ID) (fsrs.Params, error) {
	out := p.c.base
	o, err := p.tx.Settings().Get(ctx, userID)
	if err != nil {
		return fsrs.Params{}, err
	}
	out.DesiredRetention = settings.Resolve(p.c.defaults, o).DesiredRetention

	u, err := p.tx.Users().Get(ctx, userID)
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		return fsrs.Params{}, err
	}
	if len(u.Scheduling.LearningSteps) > 0 {
		steps := make([]time.Duration, len(u.Scheduling.LearningSteps))
		for i, s := range u.Scheduling.LearningSteps {
			d, err := time.ParseDuration(s)
			if err != nil {
				return fsrs.Params{}, fmt.Errorf("store: learning step %q: %w", s, err)
			}
			steps[i] = d
		}
		out.LearningSteps, out.RelearningSteps = steps, steps
	}
	return out, nil
}
