// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/settings"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// ErrNotSupported is returned for a backend that is recognized but not built yet.
var ErrNotSupported = errors.New("store: backend not yet supported")

// Store is the single persistence boundary. It will compose each feature's
// Repo interface (Users, Identities, WebSessions, Problems, Tags, Quizzes,
// Attempts, Cards, ReviewSessions, Mastery, TagOperations, Idempotency) as
// those features land; every repo method takes the owner user ID.
type Store interface {
	// WithTx runs fn atomically. fn gets a Store bound to the transaction;
	// returning an error (or panicking) rolls back, nil commits. Calling
	// WithTx on the Store passed to fn joins the same transaction.
	WithTx(ctx context.Context, fn func(tx Store) error) error

	// Users and Identities are bound to this Store's transaction, if any.
	Users() auth.UserRepo
	Identities() auth.IdentityRepo

	// WebSessions and LoginStates back the browser login flow.
	WebSessions() auth.WebSessionRepo
	LoginStates() auth.LoginStateRepo

	// Settings reads and writes a user's scheduling overrides.
	Settings() settings.Repo

	// Problems stores problems and their immutable versions.
	Problems() problems.Repo

	// Tags stores tags, aliases and problem-to-tag mappings.
	Tags() tags.Repo

	// Quizzes stores quizzes and their ordered items.
	Quizzes() quizzes.Repo

	// TagOperations stores the tag evolution audit trail and its snapshots.
	TagOperations() tags.OpRepo

	// SeedKeys maps seed-file keys to the rows they created.
	SeedKeys() seed.KeyRepo

	// Idempotency stores POST responses for Idempotency-Key replay.
	Idempotency() platform.IdempotencyRepo

	// Migrate applies pending embedded migrations and returns the versions applied.
	Migrate(ctx context.Context) ([]int, error)

	// Close releases the underlying connections.
	Close() error
}

// AuthTx adapts a Store to auth.Transactor: each InTx call is one store
// transaction with transaction-bound repositories.
func AuthTx(s Store) auth.Transactor { return authTx{s} }

type authTx struct{ s Store }

func (a authTx) InTx(ctx context.Context, fn func(auth.Repos) error) error {
	return a.s.WithTx(ctx, func(tx Store) error {
		return fn(auth.Repos{Users: tx.Users(), Identities: tx.Identities()})
	})
}

// ProblemsTx adapts a Store to problems.Transactor.
func ProblemsTx(s Store) problems.Transactor { return problemsTx{s} }

type problemsTx struct{ s Store }

func (a problemsTx) InTx(ctx context.Context, fn func(problems.Repo) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(tx.Problems()) })
}

// ProblemsBatchTx adapts a Store to problems.BatchTransactor: problems and
// their tags are written in one store transaction.
func ProblemsBatchTx(s Store) problems.BatchTransactor { return problemsBatchTx{s} }

type problemsBatchTx struct{ s Store }

func (a problemsBatchTx) InBatchTx(ctx context.Context, fn func(problems.BatchScope) error) error {
	return a.s.WithTx(ctx, func(tx Store) error {
		return fn(problems.BatchScope{Problems: tx.Problems(), Tags: tags.NewBinder(tx.Tags())})
	})
}

// SettingsTx adapts a Store to settings.Transactor.
func SettingsTx(s Store) settings.Transactor { return settingsTx{s} }

type settingsTx struct{ s Store }

func (a settingsTx) InTx(ctx context.Context, fn func(settings.Repos) error) error {
	return a.s.WithTx(ctx, func(tx Store) error {
		return fn(settings.Repos{Settings: tx.Settings()})
	})
}

// TagsTx adapts a Store to tags.Transactor.
func TagsTx(s Store) tags.Transactor { return tagsTx{s} }

type tagsTx struct{ s Store }

func (a tagsTx) InTx(ctx context.Context, fn func(tags.Repos) error) error {
	return a.s.WithTx(ctx, func(tx Store) error {
		return fn(tags.Repos{Tags: tx.Tags(), Ops: tx.TagOperations(), Problems: tx.Problems()})
	})
}

// QuizzesTx adapts a Store to quizzes.Transactor.
func QuizzesTx(s Store) quizzes.Transactor { return quizzesTx{s} }

type quizzesTx struct{ s Store }

func (a quizzesTx) InTx(ctx context.Context, fn func(quizzes.Repo) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(tx.Quizzes()) })
}

// SeedTx adapts a Store to seed.Transactor. Each InTx call builds fresh
// problems, tags and quizzes services over one store transaction, so a seed
// file applies completely or not at all and every write passes the services'
// rules.
func SeedTx(s Store, clock domain.Clock, ids *domain.IDGenerator) seed.Transactor {
	return seedTx{s, clock, ids}
}

type seedTx struct {
	s     Store
	clock domain.Clock
	ids   *domain.IDGenerator
}

func (a seedTx) InTx(ctx context.Context, fn func(seed.Services) error) error {
	return a.s.WithTx(ctx, func(tx Store) error {
		ps := problems.NewService(tx.Problems(), ProblemsTx(tx), a.clock, a.ids)
		return fn(seed.Services{
			Problems: ps,
			Tags:     tags.NewService(TagsTx(tx), a.clock, a.ids),
			Quizzes:  quizzes.NewService(QuizzesTx(tx), ps, a.clock, a.ids),
			Keys:     tx.SeedKeys(),
			Clock:    a.clock,
		})
	})
}
