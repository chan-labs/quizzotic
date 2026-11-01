// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"errors"

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

	// ProblemIDs returns IDs of the owner's non-deleted problems carrying the
	// tag, ascending, after the cursor.
	ProblemIDs(ctx context.Context, ownerID, tagID domain.ID, after domain.ID, limit int) ([]domain.ID, error)

	// ProblemTags returns the tags on a problem, ordered by slug.
	ProblemTags(ctx context.Context, ownerID, problemID domain.ID) ([]Tag, error)

	// SetProblemTags replaces the problem's tag set. The caller has checked
	// that the problem exists; a tag that is not the owner's returns ErrNotFound
	// and changes nothing.
	SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, tagIDs []domain.ID) error
}
