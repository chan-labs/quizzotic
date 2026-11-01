// SPDX-License-Identifier: Apache-2.0

package quizzes

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ErrNotFound: the quiz does not exist, is deleted, or belongs to another owner.
var ErrNotFound = errors.New("quizzes: not found")

// ErrItemNotFound: an item names a problem that is missing, deleted or another
// owner's. Repo.SetItems returns it so ownership holds even if a caller skips
// the service's check.
var ErrItemNotFound = errors.New("quizzes: item problem not found")

// ListFilter narrows List. The zero value lists every non-deleted quiz.
type ListFilter struct {
	Status Status    // empty: any
	After  domain.ID // keyset cursor: only IDs greater than this
	Limit  int       // <= 0: no limit
}

// Repo persists quizzes and their ordered items. Every method is scoped to the
// owner it names; another owner's rows are indistinguishable from missing
// ones. Multi-step changes run inside store.WithTx.
type Repo interface {
	// Create inserts q with no items (q.Items is ignored).
	Create(ctx context.Context, ownerID domain.ID, q Quiz) error

	// Get returns a non-deleted quiz with its items in order, or ErrNotFound.
	Get(ctx context.Context, ownerID, id domain.ID) (Quiz, error)

	// List returns the owner's non-deleted quizzes with items, ordered by ID.
	List(ctx context.Context, ownerID domain.ID, f ListFilter) ([]Quiz, error)

	// Update stores q's title, description, status and updated_at.
	Update(ctx context.Context, ownerID domain.ID, q Quiz) error

	// SetItems replaces the quiz's items with problemIDs in order and sets
	// updated_at. The IDs must be distinct problems of the owner that are not
	// deleted, else ErrItemNotFound. It takes several statements: run it
	// inside store.WithTx so a failure leaves the old items in place.
	SetItems(ctx context.Context, ownerID, id domain.ID, problemIDs []domain.ID, at time.Time) error

	// SoftDelete marks a quiz deleted; its rows are kept.
	SoftDelete(ctx context.Context, ownerID, id domain.ID, at time.Time) error
}
