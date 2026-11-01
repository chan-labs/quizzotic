// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ErrNotFound is returned when the user does not exist.
var ErrNotFound = errors.New("settings: user not found")

// Repo persists a user's scheduling overrides on the user row. Every method is
// scoped to the owner it names.
type Repo interface {
	// Get returns the user's overrides, or ErrNotFound.
	Get(ctx context.Context, userID domain.ID) (Overrides, error)
	// Replace stores o as the user's complete set of overrides (nil fields
	// clear the override), or returns ErrNotFound. It leaves all other user
	// columns untouched.
	Replace(ctx context.Context, userID domain.ID, o Overrides) error
}

// Repos is the set of repositories a Transactor binds to one transaction.
type Repos struct {
	Settings Repo
}

// Transactor runs fn atomically over transaction-bound repositories. The store
// package adapts a store.Store to it.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repos) error) error
}
