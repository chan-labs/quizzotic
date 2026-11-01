// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
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
		return fn(tags.Repos{Tags: tx.Tags(), Problems: tx.Problems()})
	})
}
