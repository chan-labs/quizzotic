// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Errors returned by Repo and Service.
var (
	// ErrNotFound: the tag (or problem) does not exist or belongs to another owner.
	ErrNotFound = errors.New("tags: not found")
	// ErrSlugTaken: the slug is an active tag's slug or an alias of the same owner.
	ErrSlugTaken = errors.New("tags: slug already in use")
	// ErrPreconditionFailed: If-Match did not match the tag's current ETag.
	ErrPreconditionFailed = errors.New("tags: precondition failed")
)

// ListFilter narrows List.
type ListFilter struct {
	Status Status    // required
	After  domain.ID // keyset cursor: only IDs greater than this
	Limit  int       // <= 0: no limit
}

// Repo persists tags, aliases and problem-to-tag mappings. Every method is
// scoped to the owner it names; another owner's rows are indistinguishable
// from missing ones. Multi-step changes run inside store.WithTx.
type Repo interface {
	// LockOwner serializes units of work of one owner that read before they
	// write: it blocks until any other transaction holding the owner's lock
	// has finished, then holds the lock until the surrounding transaction
	// ends. Services call it first in every transaction that reads tags to
	// decide a write (slug checks, merge, rename, retire, split, revert,
	// tagging a problem). It is the same per-owner lock as review.Repo's and
	// metrics.Repo's. Backends whose writers are already serialized (SQLite)
	// do nothing. It must run inside store.WithTx; see
	// docs/adr/0007-owner-tag-lock.md.
	LockOwner(ctx context.Context, ownerID domain.ID) error

	// Create inserts t (with no aliases). It returns ErrSlugTaken when the
	// slug is an active slug or an alias of the owner.
	Create(ctx context.Context, ownerID domain.ID, t Tag) error

	// Get returns a tag of any status with its aliases, or ErrNotFound.
	Get(ctx context.Context, ownerID, id domain.ID) (Tag, error)

	// GetBySlug returns the owner's active tag with this exact slug.
	GetBySlug(ctx context.Context, ownerID domain.ID, slug string) (Tag, error)

	// GetByAlias returns the tag that holds this alias.
	GetByAlias(ctx context.Context, ownerID domain.ID, slug string) (Tag, error)

	// List returns the owner's tags with the filter's status, ordered by ID.
	List(ctx context.Context, ownerID domain.ID, f ListFilter) ([]Tag, error)

	// Update stores t's slug, name, description and updated_at. When the slug
	// changes, the previous slug becomes an alias of the tag, and an alias
	// equal to the new slug is dropped. A new slug that is another tag's
	// active slug or alias returns ErrSlugTaken.
	Update(ctx context.Context, ownerID domain.ID, t Tag) error

	// Retire sets the tag's status to retired and its updated_at to at. It
	// returns ErrNotFound for a missing tag. Mappings and aliases are kept.
	Retire(ctx context.Context, ownerID, id domain.ID, at time.Time) error

	// ProblemIDs returns IDs of the owner's non-deleted problems carrying the
	// tag, ascending, after the cursor.
	ProblemIDs(ctx context.Context, ownerID, tagID domain.ID, after domain.ID, limit int) ([]domain.ID, error)

	// ProblemTags returns the tags on a problem, ordered by slug.
	ProblemTags(ctx context.Context, ownerID, problemID domain.ID) ([]Tag, error)

	// SetProblemTags replaces the problem's tag set. The caller has checked
	// that the problem exists; a tag that is not the owner's returns ErrNotFound
	// and changes nothing.
	SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, tagIDs []domain.ID) error

	// Merge repoints every problem mapping of the source tags to the target
	// (deduplicated), marks the sources merged with merged_into set to the
	// target, moves their slugs and aliases to the target's aliases and sets
	// the target's updated_at to now. Callers have checked that the tags are
	// distinct and active; a tag that is not the owner's returns ErrNotFound
	// and changes nothing. It runs inside store.WithTx.
	Merge(ctx context.Context, ownerID domain.ID, sourceIDs []domain.ID, targetID domain.ID, now time.Time) error
}
