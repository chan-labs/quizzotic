// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// SessionKind says what a session serves.
type SessionKind string

// Session kinds.
const (
	SessionReview SessionKind = "review"
	SessionQuiz   SessionKind = "quiz"
)

// Session is a stored review or quiz session: the frozen, ordered list of
// problems served to a client (spec: ReviewSession). Items only ever grow, by
// appending learning-step cards that come due mid-session.
type Session struct {
	ID     domain.ID
	UserID domain.ID
	Kind   SessionKind
	// QuizID is set for a quiz session and nil otherwise.
	QuizID      domain.ID
	Items       []Item
	CreatedAt   time.Time
	CompletedAt *time.Time
}

var (
	// ErrSessionNotFound is returned when a session does not exist or belongs
	// to another owner.
	ErrSessionNotFound = errors.New("review: session not found")
	// ErrSessionChanged is returned by AppendItems when the session's list is
	// no longer the length the caller read: someone appended in between.
	ErrSessionChanged = errors.New("review: session changed")
)

// SessionRepo persists sessions. Every method is scoped to the owner it names;
// another owner's rows are indistinguishable from missing ones.
type SessionRepo interface {
	// Create stores s with its items in order. s.UserID must equal ownerID,
	// the quiz (if any) and every problem must be the owner's, otherwise
	// ErrNotFound is returned and nothing is stored.
	Create(ctx context.Context, ownerID domain.ID, s Session) error

	// Get returns the owner's session with its items in order, or
	// ErrSessionNotFound.
	Get(ctx context.Context, ownerID, id domain.ID) (Session, error)

	// AppendItems appends items to the end of the session's list, but only
	// when the list currently holds exactly have items; otherwise nothing is
	// stored and ErrSessionChanged is returned. This makes concurrent
	// appenders safe: the loser rereads and recomputes. A missing or foreign
	// session is ErrSessionNotFound.
	AppendItems(ctx context.Context, ownerID, id domain.ID, have int, items []Item) error

	// Complete marks the owner's session completed at the given time unless it
	// already is; the first completion time is kept. A missing or foreign
	// session is ErrSessionNotFound.
	Complete(ctx context.Context, ownerID, id domain.ID, at time.Time) error
}
