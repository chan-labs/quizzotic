// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ErrNotFound is returned when an attempt (or the problem it answers) does not
// exist or belongs to another owner.
var ErrNotFound = errors.New("attempts: not found")

// Repo persists attempts. Every method is scoped to the owner it names;
// another owner's rows are indistinguishable from missing ones. There is
// deliberately no update or delete: attempts are append-only.
type Repo interface {
	// Create appends a. a.UserID must equal ownerID, and the problem must be
	// the owner's, otherwise ErrNotFound is returned and nothing is stored.
	Create(ctx context.Context, ownerID domain.ID, a Attempt) error

	// Get returns the owner's attempt, or ErrNotFound.
	Get(ctx context.Context, ownerID, id domain.ID) (Attempt, error)
}
