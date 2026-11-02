// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ErrNotFound is returned when an attempt (or the problem it answers) does not
// exist or belongs to another owner.
var ErrNotFound = errors.New("attempts: not found")

// ErrAlreadyRated is returned by SetRating when the attempt already has a
// rating: a rating is filled in once.
var ErrAlreadyRated = errors.New("attempts: already rated")

// ErrDuplicate is returned by Create when the attempt carries a session ID and
// the session already holds as many attempts at that problem as it has items
// for it: every presentation of the problem is answered, so this one is a
// concurrent or replayed duplicate. A learning-step card that the session
// lists again has a further item, so its repeat is not a duplicate.
var ErrDuplicate = errors.New("attempts: duplicate attempt in session")

// ListFilter narrows List. The zero value lists all of the owner's attempts.
type ListFilter struct {
	// ProblemID, when set, keeps attempts at that problem.
	ProblemID domain.ID
	// TagID, when set, keeps attempts at problems carrying this tag. The
	// caller has resolved it for the same owner.
	TagID domain.ID
	// SessionID, when set, keeps attempts answered within that session.
	SessionID domain.ID
	// Since keeps attempts answered at or after it; Until keeps those answered
	// strictly before it. Zero means unbounded.
	Since, Until time.Time
	After        domain.ID // keyset cursor: only IDs greater than this
	Limit        int       // <= 0: no limit
}

// Repo persists attempts. Every method is scoped to the owner it names;
// another owner's rows are indistinguishable from missing ones. There is
// deliberately no general update or delete: attempts are append-only, and
// SetRating is the one permitted change.
type Repo interface {
	// SetRating fills in the missing rating of the owner's attempt, and the
	// score that follows from it when the attempt has none yet (score may be
	// nil to leave it alone). It returns ErrNotFound for a missing or foreign
	// attempt and ErrAlreadyRated when a rating is already stored. The store
	// enforces the one-time fill as well.
	SetRating(ctx context.Context, ownerID, id domain.ID, rating int, score *float64) error

	// Create appends a. a.UserID must equal ownerID, and the problem must be
	// the owner's, otherwise ErrNotFound is returned and nothing is stored.
	// When a.SessionID names a session that lists the problem, the count of
	// the session's attempts at it is checked against the count of its items
	// for it inside the same transaction, and ErrDuplicate is returned (and
	// nothing stored) when no presentation is left to answer. A session ID
	// that is unknown, or does not list the problem, is not checked.
	Create(ctx context.Context, ownerID domain.ID, a Attempt) error

	// Get returns the owner's attempt, or ErrNotFound.
	Get(ctx context.Context, ownerID, id domain.ID) (Attempt, error)

	// ForCard lists the owner's attempts at one problem and card epoch in
	// answered_at order (ties by ID). It is the input of the card fold; an
	// unknown problem or foreign owner yields an empty list.
	ForCard(ctx context.Context, ownerID, problemID domain.ID, epoch int) ([]Attempt, error)

	// List returns the owner's attempts ordered by ID, which is answer order.
	List(ctx context.Context, ownerID domain.ID, f ListFilter) ([]Attempt, error)
}
