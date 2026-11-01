// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// OpKind is the kind of a tag operation.
type OpKind string

// Operation kinds.
const (
	OpRename OpKind = "rename"
	OpMerge  OpKind = "merge"
	OpSplit  OpKind = "split"
)

// Valid reports whether k is a defined kind.
func (k OpKind) Valid() bool {
	switch k {
	case OpRename, OpMerge, OpSplit:
		return true
	}
	return false
}

// OpStatus is a tag operation's lifecycle state.
type OpStatus string

// Operation statuses.
const (
	OpProposed OpStatus = "proposed"
	OpApplied  OpStatus = "applied"
	OpRejected OpStatus = "rejected"
)

// Valid reports whether s is a defined status.
func (s OpStatus) Valid() bool {
	switch s {
	case OpProposed, OpApplied, OpRejected:
		return true
	}
	return false
}

// Operation is the audit record of one tag evolution step. Payload is the
// kind-specific input as JSON. Snapshot is the state of the tags and
// mappings the operation touched before it was applied; nil until applied.
type Operation struct {
	ID         domain.ID
	OwnerID    domain.ID
	Kind       OpKind
	Payload    json.RawMessage
	Status     OpStatus
	ProposedBy domain.Actor
	DecidedBy  domain.Actor // zero value while proposed
	AppliedAt  time.Time    // zero unless applied
	Snapshot   *Snapshot
	CreatedAt  time.Time
	DecidedAt  time.Time // zero while proposed
}

// Snapshot is a before-image of tag rows (with aliases) and the
// problem-to-tag mappings that reference them. Slices are sorted (tags by
// ID, mappings by problem then tag) so equal states encode identically.
type Snapshot struct {
	Tags     []SnapshotTag `json:"tags"`
	Mappings []Mapping     `json:"mappings"`
}

// SnapshotTag is a tag row as stored in a Snapshot.
type SnapshotTag struct {
	ID          domain.ID `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      Status    `json:"status"`
	MergedInto  domain.ID `json:"merged_into"`
	Aliases     []string  `json:"aliases"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Mapping is one problem_tags row.
type Mapping struct {
	ProblemID domain.ID `json:"problem_id"`
	TagID     domain.ID `json:"tag_id"`
}

// SnapshotTagOf converts a Tag to its snapshot form.
func SnapshotTagOf(t Tag) SnapshotTag {
	al := t.Aliases
	if al == nil {
		al = []string{}
	}
	return SnapshotTag{ID: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description, Status: t.Status,
		MergedInto: t.MergedInto, Aliases: al, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

// Errors returned by OpRepo.
var (
	// ErrOpNotFound: the operation does not exist or belongs to another owner.
	ErrOpNotFound = errors.New("tags: operation not found")
	// ErrOpNotProposed: Decide was called on an operation that is already decided.
	ErrOpNotProposed = errors.New("tags: operation is not proposed")
)

// OpFilter narrows OpRepo.List.
type OpFilter struct {
	Status OpStatus  // empty: any status
	After  domain.ID // keyset cursor: only IDs greater than this
	Limit  int       // <= 0: no limit
}

// Decision records how a proposed operation was decided. For OpApplied,
// Snapshot is the before-image and AppliedAt is set; for OpRejected both are
// zero.
type Decision struct {
	Status    OpStatus
	DecidedBy domain.Actor
	DecidedAt time.Time
	AppliedAt time.Time
	Snapshot  *Snapshot
}

// OpRepo persists tag operations. Every method is scoped to the owner it
// names; another owner's rows are indistinguishable from missing ones.
type OpRepo interface {
	// Create inserts op. A proposed op carries no snapshot or decision; an
	// op created already applied carries its Snapshot and AppliedAt.
	Create(ctx context.Context, ownerID domain.ID, op Operation) error

	// Get returns the operation or ErrOpNotFound.
	Get(ctx context.Context, ownerID, id domain.ID) (Operation, error)

	// List returns the owner's operations ordered by ID.
	List(ctx context.Context, ownerID domain.ID, f OpFilter) ([]Operation, error)

	// Decide moves a proposed operation to applied or rejected. It returns
	// ErrOpNotFound for a missing one and ErrOpNotProposed when the
	// operation was already decided; it never rewrites a stored snapshot.
	Decide(ctx context.Context, ownerID, id domain.ID, d Decision) error

	// CaptureSnapshot reads the current state of the given tags (rows with
	// aliases) and every problem_tags row that references them. A tag that
	// is not the owner's returns ErrNotFound.
	CaptureSnapshot(ctx context.Context, ownerID domain.ID, tagIDs []domain.ID) (Snapshot, error)
}
