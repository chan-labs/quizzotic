// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
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

	// Migrate applies pending embedded migrations and returns the versions applied.
	Migrate(ctx context.Context) ([]int, error)

	// Close releases the underlying connections.
	Close() error
}
