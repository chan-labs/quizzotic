// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
)

// ErrNotFound is returned when a card or the problem it belongs to does not
// exist or belongs to another owner.
var ErrNotFound = errors.New("review: not found")

// Repo persists cards. Every method is scoped to the owner it names; another
// owner's rows are indistinguishable from missing ones.
type Repo interface {
	// Get returns the owner's card for the problem, or ErrNotFound.
	Get(ctx context.Context, ownerID, problemID domain.ID) (Card, error)

	// Put inserts the card or replaces the owner's existing card for the
	// problem (whatever its epoch). c.UserID must equal ownerID and the problem
	// must be the owner's, otherwise ErrNotFound is returned and nothing is
	// stored.
	Put(ctx context.Context, ownerID domain.ID, c Card) error

	// Delete removes the owner's card for the problem. A missing card is not
	// an error.
	Delete(ctx context.Context, ownerID, problemID domain.ID) error

	// DeleteAll removes every card of the owner.
	DeleteAll(ctx context.Context, ownerID domain.ID) error

	// List returns the owner's cards ordered by problem ID.
	List(ctx context.Context, ownerID domain.ID) ([]Card, error)
}

// AttemptLog reads the attempt log. attempts.Repo implements it.
type AttemptLog interface {
	ForCard(ctx context.Context, ownerID, problemID domain.ID, epoch int) ([]attempts.Attempt, error)
}

// ProblemEpoch says whether a problem is live and which card epoch it is at.
type ProblemEpoch struct {
	ID    domain.ID
	Live  bool
	Epoch int
}

// ProblemSource reads the problems the projection depends on. Only live
// problems have cards.
type ProblemSource interface {
	// Epoch returns the owner's non-deleted problem, or ErrNotFound.
	Epoch(ctx context.Context, ownerID, problemID domain.ID) (ProblemEpoch, error)
	// LiveEpochs returns every live, non-deleted problem of the owner.
	LiveEpochs(ctx context.Context, ownerID domain.ID) ([]ProblemEpoch, error)
}

// ParamsSource supplies the scheduler parameters for a user: the configured
// defaults with the user's overrides (desired retention, learning steps).
type ParamsSource interface {
	Params(ctx context.Context, userID domain.ID) (fsrs.Params, error)
}

// Scope is everything the projection reads and writes, bound to one
// transaction (or to the bare store).
type Scope struct {
	Cards    Repo
	Attempts AttemptLog
	Problems ProblemSource
	Params   ParamsSource
}

// Transactor runs fn atomically over a transaction-bound Scope. The store
// package adapts a store.Store to it.
type Transactor interface {
	InTx(ctx context.Context, fn func(Scope) error) error
}
