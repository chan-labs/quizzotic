// SPDX-License-Identifier: Apache-2.0

package seed

import (
	"context"
	"errors"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ErrNotFound: the owner has no entry for the key.
var ErrNotFound = errors.New("seed: key not found")

// Kind says which sort of item a key names. Keys are unique per owner and kind.
type Kind string

// Kinds of seeded item.
const (
	KindTag     Kind = "tag"
	KindProblem Kind = "problem"
	KindQuiz    Kind = "quiz"
)

// Entry records what a key created and the digest of the item as last applied.
type Entry struct {
	EntityID domain.ID
	Digest   string
}

// KeyRepo persists the key-to-row map. Every method is scoped to the owner it
// names. An entry outlives a soft-deleted row, so a deleted item stays deleted.
type KeyRepo interface {
	// Get returns the entry, or ErrNotFound.
	Get(ctx context.Context, ownerID domain.ID, kind Kind, key string) (Entry, error)
	// Put inserts or replaces the entry; at becomes its updated_at.
	Put(ctx context.Context, ownerID domain.ID, kind Kind, key string, e Entry, at time.Time) error
}
