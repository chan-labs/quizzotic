// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/review"
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

	// Attempts appends answers; attempts are never updated or deleted.
	Attempts() attempts.Repo

	// Cards stores the card projection, which is derived from Attempts and
	// can be dropped and rebuilt at any time.
	Cards() review.Repo

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

// ProblemsEditTx is ProblemsTx whose edits also supply the major-edit handler
// bound to the edit's own transaction (problems.EditTransactor), so a major
// edit and the card epoch change are atomic. majorEdit builds the handler for
// a transaction, for example Cards.MajorEdit.
func ProblemsEditTx(s Store, majorEdit func(tx Store) problems.MajorEditHandler) problems.Transactor {
	return problemsEditTx{problemsTx{s}, majorEdit}
}

type problemsEditTx struct {
	problemsTx
	majorEdit func(tx Store) problems.MajorEditHandler
}

func (a problemsEditTx) InEditTx(ctx context.Context, fn func(problems.Repo, problems.MajorEditHandler) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(tx.Problems(), a.majorEdit(tx)) })
}

// AttemptsTx adapts a Store to attempts.Transactor.
func AttemptsTx(s Store) attempts.Transactor { return attemptsTx{s} }

type attemptsTx struct{ s Store }

func (a attemptsTx) InTx(ctx context.Context, fn func(attempts.Repo) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(tx.Attempts()) })
}

// AttemptsCardTx is AttemptsTx whose transactions also supply the card updater
// bound to the same transaction (attempts.CardTransactor), so an attempt and
// its card are written atomically. cards builds the updater for a transaction,
// for example Cards.CardUpdater.
func AttemptsCardTx(s Store, cards func(tx Store) attempts.CardUpdater) attempts.Transactor {
	return attemptsCardTx{attemptsTx{s}, cards}
}

type attemptsCardTx struct {
	attemptsTx
	cards func(tx Store) attempts.CardUpdater
}

func (a attemptsCardTx) InCardTx(ctx context.Context, fn func(attempts.Repo, attempts.CardUpdater) error) error {
	return a.s.WithTx(ctx, func(tx Store) error { return fn(tx.Attempts(), a.cards(tx)) })
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
// rules. majorEdit, when not nil, builds the handler told about a major edit
// made through seeding, bound to the seed transaction (see Cards.MajorEdit); a
// seed file's major edits then start a new card epoch like any other.
func SeedTx(s Store, clock domain.Clock, ids *domain.IDGenerator, majorEdit func(tx Store) problems.MajorEditHandler) seed.Transactor {
	return seedTx{s, clock, ids, majorEdit}
}

type seedTx struct {
	s         Store
	clock     domain.Clock
	ids       *domain.IDGenerator
	majorEdit func(tx Store) problems.MajorEditHandler
}

func (a seedTx) InTx(ctx context.Context, fn func(seed.Services) error) error {
	return a.s.WithTx(ctx, func(tx Store) error {
		var popts []problems.Option
		if a.majorEdit != nil {
			popts = append(popts, problems.WithMajorEditHandler(a.majorEdit(tx)))
		}
		ps := problems.NewService(tx.Problems(), ProblemsTx(tx), a.clock, a.ids, popts...)
		return fn(seed.Services{
			Problems: ps,
			Tags:     tags.NewService(TagsTx(tx), a.clock, a.ids),
			Quizzes:  quizzes.NewService(QuizzesTx(tx), ps, a.clock, a.ids),
			Keys:     tx.SeedKeys(),
			Clock:    a.clock,
		})
	})
}
