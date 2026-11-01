// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ErrNotFound is returned when a problem or version does not exist, is
// soft-deleted (unless the method says otherwise), or belongs to another owner.
var ErrNotFound = errors.New("problems: not found")

// ListFilter narrows List. The zero value lists the owner's non-deleted
// problems of every status.
type ListFilter struct {
	Status         Status // empty: any
	IncludeDeleted bool
	// Type, when set, keeps problems whose current version has this type.
	Type string
	// CreatedByKind, when set, keeps problems created by this kind of actor.
	CreatedByKind domain.ActorKind
	// Query, when set, keeps problems whose current version's prompt contains
	// it as a case-insensitive substring. Matching is literal: wildcard
	// characters in Query have no special meaning.
	Query string
	After domain.ID // keyset cursor: only IDs greater than this
	Limit int       // <= 0: no limit
}

// Current is a problem with its current version.
type Current struct {
	Problem Problem
	Version Version
}

// Repo persists problems and their immutable versions. Every method is scoped
// to the owner it names; another owner's rows are indistinguishable from
// missing ones. Callers that need CreateWithVersion/AppendVersion to be atomic
// with other writes run them inside store.WithTx.
type Repo interface {
	// Create inserts p and its first version (v.Version must be 1 and
	// p.CurrentVersion 1) in one statement group.
	Create(ctx context.Context, ownerID domain.ID, p Problem, v Version) error

	// Get returns a non-deleted problem, or ErrNotFound.
	Get(ctx context.Context, ownerID, id domain.ID) (Problem, error)

	// GetVersion returns one version of a non-deleted problem, or ErrNotFound.
	GetVersion(ctx context.Context, ownerID, id domain.ID, version int) (Version, error)

	// ListVersions returns all versions of a non-deleted problem, oldest first.
	ListVersions(ctx context.Context, ownerID, id domain.ID) ([]Version, error)

	// List returns the owner's problems ordered by ID.
	List(ctx context.Context, ownerID domain.ID, f ListFilter) ([]Problem, error)

	// ListCurrent is List with each problem's current version, in one query.
	ListCurrent(ctx context.Context, ownerID domain.ID, f ListFilter) ([]Current, error)

	// AppendVersion assigns v the next version number, stores it, and moves
	// the problem's current_version to it (updated_at becomes v.CreatedAt). It returns the stored version, or
	// ErrNotFound for a missing or deleted problem.
	AppendVersion(ctx context.Context, ownerID, id domain.ID, v Version) (Version, error)

	// SetStatus changes the status of a non-deleted problem.
	SetStatus(ctx context.Context, ownerID, id domain.ID, s Status, at time.Time) error

	// SoftDelete marks a problem deleted; versions are kept. Deleting a
	// missing or already-deleted problem returns ErrNotFound.
	SoftDelete(ctx context.Context, ownerID, id domain.ID, at time.Time) error
}
