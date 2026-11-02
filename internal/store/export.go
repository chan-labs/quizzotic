// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/export"
	"github.com/chan-labs/quizzotic/internal/review"
)

// ExportTx adapts a Store to export.Transactor: an import is one store
// transaction.
func ExportTx(s Store) export.Transactor { return exportTx{s} }

type exportTx struct{ s Store }

func (a exportTx) InTx(ctx context.Context, fn func(export.Repo) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(tx.Export()) })
}

// ExportService is the export service over s. After an import commits it
// rebuilds the user's projections with the same code `quizzotic admin
// rebuild` runs: cards from the attempt log, tag mastery from the cards, then
// the mastery history from the attempt log.
func (m Mastery) ExportService(s Store, log *slog.Logger) *export.Service {
	if log == nil {
		log = slog.Default()
	}
	return export.NewService(s.Export(), ExportTx(s), m.clock,
		export.WithLogger(log), export.WithRebuilder(projections{s: s, m: m, log: log}))
}

type projections struct {
	s   Store
	m   Mastery
	log *slog.Logger
}

func (p projections) Rebuild(ctx context.Context, userID domain.ID) error {
	if _, err := p.m.cards.Service(p.s, review.WithLogger(p.log)).RebuildOne(ctx, userID); err != nil {
		return fmt.Errorf("rebuild cards: %w", err)
	}
	if err := p.m.Service(p.s).RebuildUser(ctx, userID); err != nil {
		return fmt.Errorf("rebuild mastery: %w", err)
	}
	if _, err := p.m.Snapshotter(p.s, p.log).BackfillUser(ctx, userID, nil); err != nil {
		return fmt.Errorf("rebuild mastery history: %w", err)
	}
	return nil
}
