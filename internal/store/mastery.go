// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"log/slog"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/review"
)

// Mastery wires the mastery projection (internal/metrics) into store
// transactions, the way Cards wires the card projection. It reads cards with
// the same scheduler parameters the cards were folded with, and predicts
// recall at clock.Now().
type Mastery struct {
	cards Cards
	clock domain.Clock
}

// NewMastery builds the wiring over the card wiring c.
func NewMastery(c Cards, clock domain.Clock) Mastery { return Mastery{cards: c, clock: clock} }

// Scope binds the projection to tx.
func (m Mastery) Scope(tx Store) metrics.Scope {
	return metrics.Scope{Repo: tx.Mastery(), Params: paramsSource{tx: tx, c: m.cards}}
}

// Projector is the mastery projection bound to tx.
func (m Mastery) Projector(tx Store) *metrics.Projector {
	return metrics.NewProjector(m.Scope(tx), m.clock)
}

// Service is the metrics service over s: RebuildUser in one transaction.
func (m Mastery) Service(s Store) *metrics.Service {
	return metrics.NewService(masteryTx{s, m}, m.clock)
}

// CardUpdater is the attempts hook bound to tx that maintains both
// projections: the attempt's card first, then the mastery rows of the
// problem's tags, in the attempt's own transaction.
func (m Mastery) CardUpdater(tx Store) attempts.CardUpdater {
	return cardAndMastery{cards: m.cards.Projector(tx), mastery: m.Projector(tx)}
}

// Snapshotter is the daily snapshot writer over s: each user is snapshotted in
// one transaction, using the user's effective settings (the configured defaults
// with the user's overrides) for the time zone and rollover hour.
func (m Mastery) Snapshotter(s Store, log *slog.Logger) *metrics.Snapshotter {
	return metrics.NewSnapshotter(snapshotTx{s, m}, m.clock, log)
}

type snapshotTx struct {
	s Store
	m Mastery
}

func (a snapshotTx) InTx(ctx context.Context, fn func(metrics.SnapshotScope) error) error {
	return a.s.WithTx(ctx, func(tx Store) error {
		return fn(metrics.SnapshotScope{
			Scope:     a.m.Scope(tx),
			Snapshots: tx.MasterySnapshots(),
			Settings:  effectiveSettings{tx: tx, c: a.m.cards},
		})
	})
}

type masteryTx struct {
	s Store
	m Mastery
}

func (a masteryTx) InTx(ctx context.Context, fn func(metrics.Scope) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(a.m.Scope(tx)) })
}

type cardAndMastery struct {
	cards   *review.Projector
	mastery *metrics.Projector
}

func (u cardAndMastery) Apply(ctx context.Context, a attempts.Attempt, rating int) (*time.Time, error) {
	due, err := u.cards.Apply(ctx, a, rating)
	if err != nil {
		return nil, err
	}
	if err := u.mastery.UpdateProblem(ctx, a.UserID, a.ProblemID); err != nil {
		return nil, err
	}
	return due, nil
}
