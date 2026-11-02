// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/review"
)

// ErrNotFound is returned when a mastery row or the tag it belongs to does not
// exist or belongs to another owner.
var ErrNotFound = errors.New("metrics: not found")

// TagCard is one live problem carrying a tag, with the owner's card for it.
// Card is nil when the problem has no card yet (never answered).
type TagCard struct {
	TagID     domain.ID
	ProblemID domain.ID
	Card      *review.Card
}

// Repo persists the mastery projection and reads the inputs it is computed
// from. Every method is scoped to the owner it names; another owner's rows are
// indistinguishable from missing ones.
type Repo interface {
	// LockOwner takes the owner's lock for the rest of the transaction, the
	// same one tags.Repo.LockOwner and review.Repo.LockOwner take. The
	// projection takes it before reading the tags and cards a row is computed
	// from, so a concurrent attempt, tag change or rebuild of the same owner
	// cannot leave a row that misses it. It must run inside store.WithTx.
	LockOwner(ctx context.Context, ownerID domain.ID) error

	// Get returns the owner's mastery row for the tag, or ErrNotFound.
	Get(ctx context.Context, ownerID, tagID domain.ID) (TagMastery, error)

	// Put inserts the row or replaces the owner's existing row for the tag.
	// m.UserID must equal ownerID and the tag must be the owner's, otherwise
	// ErrNotFound is returned and nothing is stored.
	Put(ctx context.Context, ownerID domain.ID, m TagMastery) error

	// Delete removes the owner's row for the tag. A missing row is not an error.
	Delete(ctx context.Context, ownerID, tagID domain.ID) error

	// DeleteAll removes every mastery row of the owner.
	DeleteAll(ctx context.Context, ownerID domain.ID) error

	// List returns the owner's mastery rows ordered by tag ID.
	List(ctx context.Context, ownerID domain.ID) ([]TagMastery, error)

	// ActiveTagIDs returns the owner's active tags, ascending.
	ActiveTagIDs(ctx context.Context, ownerID domain.ID) ([]domain.ID, error)

	// ProblemTagIDs returns the tags (of any status) on the owner's problem,
	// ascending. A missing problem has none.
	ProblemTagIDs(ctx context.Context, ownerID, problemID domain.ID) ([]domain.ID, error)

	// TagCards returns, for each of the owner's active tags in tagIDs (all
	// active tags when tagIDs is empty), one entry per live, non-deleted
	// problem carrying the tag, ordered by tag ID then problem ID. A tag that
	// is not active, or has no live problem, contributes nothing.
	TagCards(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) ([]TagCard, error)

	// LastAttempts returns the time of the latest attempt of the owner on any
	// non-deleted problem carrying each tag in tagIDs (all of the owner's
	// tags when tagIDs is empty). Tags with no attempt are absent.
	LastAttempts(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) (map[domain.ID]time.Time, error)

	// AttemptLog returns the owner's attempts on non-deleted problems that
	// belong to a card (problem and epoch) with at least one attempt answered
	// at or after since, ordered by problem ID, card epoch, answered_at, ID.
	// Whole card histories are returned so a card's state before each answer
	// can be replayed.
	AttemptLog(ctx context.Context, ownerID domain.ID, since time.Time) ([]attempts.Attempt, error)

	// ProblemTags maps each of the owner's problems to its active tags,
	// ascending. A problem with no active tag is absent.
	ProblemTags(ctx context.Context, ownerID domain.ID) (map[domain.ID][]domain.ID, error)

	// DueTimes returns, ascending, the due instants before the cutoff of the
	// owner's cards on live, non-deleted problems.
	DueTimes(ctx context.Context, ownerID domain.ID, before time.Time) ([]time.Time, error)

	// HistoryProblems returns every problem of the owner, deleted ones
	// included, with the lifecycle data a historical replay needs, ordered by
	// problem ID.
	HistoryProblems(ctx context.Context, ownerID domain.ID) ([]HistoryProblem, error)

	// HistoryAttempts returns every attempt of the owner, including those on
	// deleted problems, ordered by problem ID, card epoch, answered_at, ID.
	HistoryAttempts(ctx context.Context, ownerID domain.ID) ([]attempts.Attempt, error)

	// ActiveTags returns the owner's active tags with their creation times,
	// ordered by tag ID.
	ActiveTags(ctx context.Context, ownerID domain.ID) ([]HistoryTag, error)

	// TagLabels returns the slug and name of each of the owner's active tags,
	// ordered by tag ID.
	TagLabels(ctx context.Context, ownerID domain.ID) ([]TagLabel, error)

	// StudiedCards returns the owner's cards on live, non-deleted problems
	// that have been reviewed at least once (see Studied), with the prompt and
	// type of the problem's current version, ordered by problem ID.
	StudiedCards(ctx context.Context, ownerID domain.ID) ([]ProblemCard, error)
}

// TagLabel names an active tag.
type TagLabel struct {
	ID         domain.ID
	Slug, Name string
}

// ProblemCard is a studied card with the problem it is for.
type ProblemCard struct {
	ProblemID domain.ID
	// Type and Prompt are those of the problem's current version.
	Type, Prompt string
	Card         review.Card
}

// ParamsSource supplies the scheduler parameters for a user (the same source
// the card projection uses), so recall is predicted with the parameters that
// scheduled the cards.
type ParamsSource interface {
	Params(ctx context.Context, userID domain.ID) (fsrs.Params, error)
}

// Scope is everything the projection reads and writes, bound to one
// transaction (or to the bare store).
type Scope struct {
	Repo   Repo
	Params ParamsSource
}

// Transactor runs fn atomically over a transaction-bound Scope. The store
// package adapts a store.Store to it.
type Transactor interface {
	InTx(ctx context.Context, fn func(Scope) error) error
}
