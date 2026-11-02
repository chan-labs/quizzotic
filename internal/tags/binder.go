// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"errors"
	"fmt"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Binder resolves tag references and attaches tags to problems over a
// transaction-bound Repo. The problems service uses it, through its own
// narrow interface, to tag problems inside a batch create.
type Binder struct{ repo Repo }

// NewBinder returns a Binder over r, which should be bound to the caller's
// transaction.
func NewBinder(r Repo) *Binder { return &Binder{repo: r} }

// ResolveTags turns refs into distinct active tag IDs of the owner. Unknown
// and retired tags are returned as field errors named tags[i].
func (b *Binder) ResolveTags(ctx context.Context, ownerID domain.ID, refs []string) ([]domain.ID, []platform.FieldError, error) {
	// The batch that follows tags problems with what was resolved here, so
	// take the owner's tag lock now (see Repo.LockOwner).
	if err := b.repo.LockOwner(ctx, ownerID); err != nil {
		return nil, nil, err
	}
	return resolveRefs(ctx, b.repo, ownerID, refs)
}

// SetProblemTags replaces the problem's tag set.
func (b *Binder) SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, tagIDs []domain.ID) error {
	return b.repo.SetProblemTags(ctx, ownerID, problemID, tagIDs)
}

// resolveRefs maps refs (ID, slug or alias) to distinct active tag IDs. A
// merged tag stands for the tag it was merged into; bad refs are field errors.
func resolveRefs(ctx context.Context, r Repo, ownerID domain.ID, refs []string) ([]domain.ID, []platform.FieldError, error) {
	var (
		ids  []domain.ID
		seen = map[domain.ID]bool{}
		fe   []platform.FieldError
	)
	for i, ref := range refs {
		field := fmt.Sprintf("tags[%d]", i)
		t, err := Resolve(ctx, r, ownerID, ref)
		if errors.Is(err, ErrNotFound) {
			fe = append(fe, platform.FieldError{Field: field, Detail: "no such tag"})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if t, err = FollowMerged(ctx, r, ownerID, t); err != nil {
			return nil, nil, err
		}
		if t.Status != StatusActive {
			fe = append(fe, platform.FieldError{Field: field, Detail: "tag is " + string(t.Status)})
			continue
		}
		if !seen[t.ID] {
			seen[t.ID] = true
			ids = append(ids, t.ID)
		}
	}
	return ids, fe, nil
}
