// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// Field limits, mirrored in api/openapi.yaml.
const (
	maxSlugLen        = 64
	maxNameLen        = 200
	maxDescriptionLen = 2000
	maxProblemTags    = 50
)

var slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Repos is the set of repositories a Transactor binds to one transaction.
type Repos struct {
	Tags     Repo
	Ops      OpRepo
	Problems problems.Repo
}

// Transactor runs fn atomically over transaction-bound repositories. The store
// package provides the implementation.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repos) error) error
}

// mutate is InTx for a unit of work that reads tags to decide a write: it
// takes the owner's tag lock before anything is read, so two such units of the
// same owner never interleave (Postgres runs at READ COMMITTED).
func (s *Service) mutate(ctx context.Context, ownerID domain.ID, fn func(Repos) error) error {
	return s.tx.InTx(ctx, func(r Repos) error {
		if err := r.Tags.LockOwner(ctx, ownerID); err != nil {
			return err
		}
		return fn(r)
	})
}

// Service holds the tag rules.
type Service struct {
	tx    Transactor
	clock domain.Clock
	ids   *domain.IDGenerator
}

// NewService builds a Service.
func NewService(tx Transactor, clock domain.Clock, ids *domain.IDGenerator) *Service {
	return &Service{tx: tx, clock: clock, ids: ids}
}

// CreateInput is the body of a tag creation.
type CreateInput struct {
	Slug        string `json:"slug"` // derived from Name when empty
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Patch holds the fields to change; nil leaves a field alone.
type Patch struct {
	Slug        *string
	Name        *string
	Description *string
}

// TaggedProblem is a problem with the content of its current version.
type TaggedProblem struct {
	Problem problems.Problem
	Version problems.Version
}

// Create makes an active tag.
func (s *Service) Create(ctx context.Context, ownerID domain.ID, in CreateInput) (Tag, error) {
	name := strings.TrimSpace(in.Name)
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if slug == "" {
		slug = Slugify(name)
	}
	var fe []platform.FieldError
	fe = append(fe, checkName(name)...)
	fe = append(fe, checkSlug(slug)...)
	fe = append(fe, checkDescription(in.Description)...)
	if len(fe) > 0 {
		return Tag{}, platform.Validation("invalid tag", fe...)
	}
	now := s.clock.Now()
	t := Tag{ID: s.ids.New(), OwnerID: ownerID, Slug: slug, Name: name, Description: in.Description,
		Status: StatusActive, Aliases: []string{}, CreatedAt: now, UpdatedAt: now}
	err := s.mutate(ctx, ownerID, func(r Repos) error { return r.Tags.Create(ctx, ownerID, t) })
	if err != nil {
		return Tag{}, err
	}
	return t, nil
}

// Get resolves ref (ID, slug or alias) to a tag.
func (s *Service) Get(ctx context.Context, ownerID domain.ID, ref string) (Tag, error) {
	var t Tag
	err := s.tx.InTx(ctx, func(r Repos) (err error) {
		t, err = Resolve(ctx, r.Tags, ownerID, ref)
		return err
	})
	return t, err
}

// List returns one page of tags with the given status (active when empty) and
// the cursor for the next page ("" on the last one).
func (s *Service) List(ctx context.Context, ownerID domain.ID, status Status, p platform.Page) ([]Tag, string, error) {
	if status == "" {
		status = StatusActive
	}
	if !status.Valid() {
		return nil, "", platform.Validation("invalid status", platform.FieldError{Field: "status", Detail: "must be active, merged or retired"})
	}
	after, err := parseCursor(p.Cursor)
	if err != nil {
		return nil, "", err
	}
	var out []Tag
	err = s.tx.InTx(ctx, func(r Repos) (err error) {
		out, err = r.Tags.List(ctx, ownerID, ListFilter{Status: status, After: after, Limit: p.Limit + 1})
		return err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > p.Limit {
		out = out[:p.Limit]
		next = out[len(out)-1].ID.String()
	}
	return out, next, nil
}

// Update applies patch to the tag ref names. ifMatch must equal the tag's
// current ETag, else ErrPreconditionFailed. Changing the slug renames the tag:
// the old slug becomes an alias.
func (s *Service) Update(ctx context.Context, ownerID domain.ID, ref, ifMatch string, patch Patch) (Tag, error) {
	var fe []platform.FieldError
	if patch.Slug != nil {
		*patch.Slug = strings.ToLower(strings.TrimSpace(*patch.Slug))
		fe = append(fe, checkSlug(*patch.Slug)...)
	}
	if patch.Name != nil {
		*patch.Name = strings.TrimSpace(*patch.Name)
		fe = append(fe, checkName(*patch.Name)...)
	}
	if patch.Description != nil {
		fe = append(fe, checkDescription(*patch.Description)...)
	}
	if len(fe) > 0 {
		return Tag{}, platform.Validation("invalid tag", fe...)
	}
	var out Tag
	err := s.mutate(ctx, ownerID, func(r Repos) error {
		t, err := Resolve(ctx, r.Tags, ownerID, ref)
		if err != nil {
			return err
		}
		if !platform.IfMatchSatisfied(ifMatch, ETag(t)) {
			return ErrPreconditionFailed
		}
		if patch.Slug != nil {
			t.Slug = *patch.Slug
		}
		if patch.Name != nil {
			t.Name = *patch.Name
		}
		if patch.Description != nil {
			t.Description = *patch.Description
		}
		t.UpdatedAt = s.clock.Now()
		if err := r.Tags.Update(ctx, ownerID, t); err != nil {
			return err
		}
		out, err = r.Tags.Get(ctx, ownerID, t.ID)
		return err
	})
	return out, err
}

// ListProblems returns one page of the non-deleted problems carrying the tag
// ref names, with their current versions, and the next cursor.
func (s *Service) ListProblems(ctx context.Context, ownerID domain.ID, ref string, p platform.Page) ([]TaggedProblem, string, error) {
	after, err := parseCursor(p.Cursor)
	if err != nil {
		return nil, "", err
	}
	var out []TaggedProblem
	next := ""
	err = s.tx.InTx(ctx, func(r Repos) error {
		t, err := Resolve(ctx, r.Tags, ownerID, ref)
		if err != nil {
			return err
		}
		ids, err := r.Tags.ProblemIDs(ctx, ownerID, t.ID, after, p.Limit+1)
		if err != nil {
			return err
		}
		if len(ids) > p.Limit {
			ids = ids[:p.Limit]
			next = ids[len(ids)-1].String()
		}
		for _, id := range ids {
			pr, err := r.Problems.Get(ctx, ownerID, id)
			if err != nil {
				return err
			}
			v, err := r.Problems.GetVersion(ctx, ownerID, id, pr.CurrentVersion)
			if err != nil {
				return err
			}
			out = append(out, TaggedProblem{Problem: pr, Version: v})
		}
		return nil
	})
	return out, next, err
}

// ProblemTags returns the tags on the owner's problem, ordered by slug.
func (s *Service) ProblemTags(ctx context.Context, ownerID, problemID domain.ID) ([]Tag, error) {
	var out []Tag
	err := s.tx.InTx(ctx, func(r Repos) error {
		if _, err := r.Problems.Get(ctx, ownerID, problemID); err != nil {
			return err
		}
		var err error
		out, err = r.Tags.ProblemTags(ctx, ownerID, problemID)
		return err
	})
	return out, err
}

// ResolveTagFilter turns ref (ID, slug or alias) into the ID of the tag whose
// problems a list should show. A merged tag stands for the tag it was merged
// into. found is false when the owner has no such tag. It lets the problems
// service filter by tag without importing this package.
func (s *Service) ResolveTagFilter(ctx context.Context, ownerID domain.ID, ref string) (id domain.ID, found bool, err error) {
	err = s.tx.InTx(ctx, func(r Repos) error {
		t, err := Resolve(ctx, r.Tags, ownerID, ref)
		if err != nil {
			return err
		}
		if t.Status == StatusMerged {
			if t, err = r.Tags.Get(ctx, ownerID, t.MergedInto); err != nil {
				return err
			}
		}
		id = t.ID
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return domain.Nil, false, nil
	}
	return id, err == nil, err
}

// SetProblemTags replaces the tags on a problem with those refs name. A merged
// tag stands for the tag it was merged into; unknown and retired tags are
// validation errors. It returns the resulting tags ordered by slug.
func (s *Service) SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, refs []string) ([]Tag, error) {
	if len(refs) > maxProblemTags {
		return nil, platform.Validation("too many tags", platform.FieldError{
			Field: "tags", Detail: fmt.Sprintf("at most %d tags per problem", maxProblemTags)})
	}
	var out []Tag
	err := s.mutate(ctx, ownerID, func(r Repos) error {
		if _, err := r.Problems.Get(ctx, ownerID, problemID); err != nil {
			return err
		}
		ids, fe, err := resolveRefs(ctx, r.Tags, ownerID, refs)
		if err != nil {
			return err
		}
		if len(fe) > 0 {
			return platform.Validation("invalid tags", fe...)
		}
		if err := r.Tags.SetProblemTags(ctx, ownerID, problemID, ids); err != nil {
			return err
		}
		out, err = r.Tags.ProblemTags(ctx, ownerID, problemID)
		return err
	})
	return out, err
}

// Resolve finds the owner's tag that ref names: first as an ID, then as an
// active slug, then as an alias. Matching a slug or alias is case-insensitive.
// A merged or retired tag is found by ID or alias only.
func Resolve(ctx context.Context, r Repo, ownerID domain.ID, ref string) (Tag, error) {
	if id, err := domain.ParseID(ref); err == nil {
		t, err := r.Get(ctx, ownerID, id)
		if !errors.Is(err, ErrNotFound) {
			return t, err
		}
	}
	slug := strings.ToLower(ref)
	t, err := r.GetBySlug(ctx, ownerID, slug)
	if !errors.Is(err, ErrNotFound) {
		return t, err
	}
	return r.GetByAlias(ctx, ownerID, slug)
}

// Slugify derives a slug from a name: lowercase letters and digits, runs of
// anything else collapsed to one hyphen, truncated to the slug limit.
func Slugify(name string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
		default:
			hyphen = true
		}
	}
	s := b.String()
	if len(s) > maxSlugLen {
		s = strings.TrimRight(s[:maxSlugLen], "-")
	}
	return s
}

func checkSlug(slug string) []platform.FieldError {
	switch {
	case slug == "":
		return []platform.FieldError{{Field: "slug", Detail: "cannot be derived from the name; give a slug"}}
	case len(slug) > maxSlugLen:
		return []platform.FieldError{{Field: "slug", Detail: fmt.Sprintf("must be at most %d characters", maxSlugLen)}}
	case !slugRE.MatchString(slug):
		return []platform.FieldError{{Field: "slug", Detail: "must be lowercase letters and digits joined by single hyphens"}}
	}
	if _, err := domain.ParseID(slug); err == nil {
		return []platform.FieldError{{Field: "slug", Detail: "must not look like an ID"}}
	}
	return nil
}

func checkName(name string) []platform.FieldError {
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return []platform.FieldError{{Field: "name", Detail: fmt.Sprintf("must be 1 to %d characters", maxNameLen)}}
	}
	return nil
}

func checkDescription(d string) []platform.FieldError {
	if utf8.RuneCountInString(d) > maxDescriptionLen {
		return []platform.FieldError{{Field: "description", Detail: fmt.Sprintf("must be at most %d characters", maxDescriptionLen)}}
	}
	return nil
}

func parseCursor(c string) (domain.ID, error) {
	if c == "" {
		return domain.Nil, nil
	}
	id, err := domain.ParseID(c)
	if err != nil {
		return domain.Nil, platform.BadRequest("invalid cursor")
	}
	return id, nil
}
