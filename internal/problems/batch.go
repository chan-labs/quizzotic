// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"context"
	"errors"
	"fmt"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// MaxBatch is the most problems one batch request may create.
const MaxBatch = 100

// maxTagsPerProblem mirrors the limit of PUT /problems/{id}/tags.
const maxTagsPerProblem = 50

// TagBinder resolves and attaches tags inside a batch transaction. The tags
// package implements it (tags.NewBinder); problems cannot import tags.
type TagBinder interface {
	// ResolveTags turns refs (ID, slug or alias; owner-scoped; merged tags
	// followed) into distinct tag IDs. Bad refs come back as field errors
	// named tags[i]; they are not an error.
	ResolveTags(ctx context.Context, ownerID domain.ID, refs []string) ([]domain.ID, []platform.FieldError, error)
	// SetProblemTags replaces the problem's tag set with the resolved IDs.
	SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, tagIDs []domain.ID) error
}

// BatchScope is the pair of transaction-bound repositories a batch needs.
type BatchScope struct {
	Problems Repo
	Tags     TagBinder
}

// BatchTransactor runs fn atomically over transaction-bound repositories.
// store.ProblemsBatchTx adapts a store.Store to it.
type BatchTransactor interface {
	InBatchTx(ctx context.Context, fn func(BatchScope) error) error
}

// WithBatch enables CreateBatch.
func WithBatch(tx BatchTransactor) Option { return func(s *Service) { s.batch = tx } }

// BatchItem is one problem of a batch: a create plus the tags to attach.
type BatchItem struct {
	CreateInput
	// Tags are tag IDs, slugs or aliases.
	Tags []string
}

// CreateBatch creates every item, and attaches its tags, in one transaction.
// If any item is invalid nothing is written and the result is a 422 whose
// errors are the invalid fields of every bad item, each path prefixed with
// items[i]. The views come back in request order.
func (s *Service) CreateBatch(ctx context.Context, actor domain.Actor, items []BatchItem) ([]View, error) {
	if err := checkCreateActor(actor); err != nil {
		return nil, err
	}
	if s.batch == nil {
		return nil, errors.New("problems: batch create is not configured")
	}
	switch n := len(items); {
	case n == 0:
		return nil, platform.Validation("the batch is invalid",
			platform.FieldError{Field: "items", Detail: "at least one item is required"})
	case n > MaxBatch:
		return nil, platform.Validation("the batch is invalid",
			platform.FieldError{Field: "items", Detail: fmt.Sprintf("at most %d items per batch, got %d", MaxBatch, n)})
	}

	var out []View
	err := s.batch.InBatchTx(ctx, func(sc BatchScope) error {
		var (
			fields []platform.FieldError
			tagIDs = make([][]domain.ID, len(items))
		)
		for i, it := range items {
			bad := validateCreate(it.CreateInput)
			if len(it.Tags) > maxTagsPerProblem {
				bad = append(bad, platform.FieldError{Field: "tags",
					Detail: fmt.Sprintf("at most %d tags per problem", maxTagsPerProblem)})
			} else {
				ids, tagErrs, err := sc.Tags.ResolveTags(ctx, actor.UserID, it.Tags)
				if err != nil {
					return err
				}
				bad = append(bad, tagErrs...)
				tagIDs[i] = ids
			}
			for _, f := range bad {
				f.Field = fmt.Sprintf("items[%d].%s", i, f.Field)
				fields = append(fields, f)
			}
		}
		if len(fields) > 0 {
			return platform.Validation("the batch is invalid", fields...)
		}
		out = make([]View, 0, len(items))
		for i, it := range items {
			p, v, err := s.newProblem(actor, it.CreateInput)
			if err != nil {
				return err
			}
			if err := sc.Problems.Create(ctx, actor.UserID, p, v); err != nil {
				return err
			}
			if len(tagIDs[i]) > 0 {
				if err := sc.Tags.SetProblemTags(ctx, actor.UserID, p.ID, tagIDs[i]); err != nil {
					return err
				}
			}
			out = append(out, newView(p, v))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
