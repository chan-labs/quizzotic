// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// RenameInput is the payload of a rename operation: the tag to rename (ID,
// slug or alias) and its new slug and/or name. Nil fields stay as they are;
// at least one must be set.
type RenameInput struct {
	Tag  string  `json:"tag"`
	Slug *string `json:"slug,omitempty"`
	Name *string `json:"name,omitempty"`
}

// Validate normalizes the new slug and name in place and reports field errors.
func (in *RenameInput) Validate() error {
	var fe []platform.FieldError
	if strings.TrimSpace(in.Tag) == "" {
		fe = append(fe, platform.FieldError{Field: "tag", Detail: "required"})
	}
	if in.Slug == nil && in.Name == nil {
		fe = append(fe, platform.FieldError{Field: "slug", Detail: "give a new slug, a new name, or both"})
	}
	if in.Slug != nil {
		s := strings.ToLower(strings.TrimSpace(*in.Slug))
		in.Slug = &s
		fe = append(fe, checkSlug(s)...)
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		in.Name = &n
		fe = append(fe, checkName(n)...)
	}
	if len(fe) > 0 {
		return platform.Validation("invalid rename", fe...)
	}
	return nil
}

// ApplyRename renames the tag in.Tag names, inside the caller's transaction.
// A changed slug leaves the old slug behind as an alias of the tag, so
// lookups by it keep resolving. Only active tags can be renamed. A new slug
// held by another tag (as slug or alias) returns ErrSlugTaken. It returns the
// tag as stored after the rename. The caller records the operation and its
// before-snapshot.
func ApplyRename(ctx context.Context, r Repo, ownerID domain.ID, now time.Time, in RenameInput) (Tag, error) {
	if err := in.Validate(); err != nil {
		return Tag{}, err
	}
	t, err := Resolve(ctx, r, ownerID, in.Tag)
	if err != nil {
		return Tag{}, err
	}
	if t.Status != StatusActive {
		return Tag{}, platform.Validation("invalid rename", platform.FieldError{
			Field: "tag", Detail: "tag is " + string(t.Status) + "; only active tags can be renamed"})
	}
	if in.Slug != nil {
		t.Slug = *in.Slug
	}
	if in.Name != nil {
		t.Name = *in.Name
	}
	t.UpdatedAt = now
	if err := r.Update(ctx, ownerID, t); err != nil {
		return Tag{}, err
	}
	return r.Get(ctx, ownerID, t.ID)
}

// Rename runs ApplyRename in its own transaction.
func (s *Service) Rename(ctx context.Context, ownerID domain.ID, in RenameInput) (Tag, error) {
	var out Tag
	err := s.tx.InTx(ctx, func(r Repos) (err error) {
		out, err = ApplyRename(ctx, r.Tags, ownerID, s.clock.Now(), in)
		return err
	})
	return out, err
}
