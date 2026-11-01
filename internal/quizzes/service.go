// SPDX-License-Identifier: Apache-2.0

package quizzes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// Field limits, mirrored in api/openapi.yaml.
const (
	maxTitleLen       = 200
	maxDescriptionLen = 2000
	maxItems          = 500
)

// Transactor runs fn atomically over a transaction-bound Repo. The store
// package provides the implementation.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repo) error) error
}

// ProblemChecker is the part of the problems service this feature needs: it
// answers whether the owner has a (non-deleted) problem. Features call each
// other only through services, so quizzes never touches problems.Repo.
type ProblemChecker interface {
	// Get returns the owner's problem, or a 404 platform.Problem when it is
	// missing, deleted or another owner's.
	Get(ctx context.Context, ownerID, id domain.ID) (problems.View, error)
}

// Service holds the quiz rules.
type Service struct {
	tx       Transactor
	problems ProblemChecker
	clock    domain.Clock
	ids      *domain.IDGenerator
}

// NewService builds a Service.
func NewService(tx Transactor, p ProblemChecker, clock domain.Clock, ids *domain.IDGenerator) *Service {
	return &Service{tx: tx, problems: p, clock: clock, ids: ids}
}

// CreateInput is the body of a quiz creation.
type CreateInput struct {
	Title       string
	Description string
	Status      Status   // draft when empty
	ProblemIDs  []string // optional initial items, in order
}

// Patch holds the fields to change; nil leaves a field alone.
type Patch struct {
	Title       *string
	Description *string
	Status      *string
}

// Create makes a quiz, with its initial items when given.
func (s *Service) Create(ctx context.Context, actor domain.Actor, in CreateInput) (Quiz, error) {
	title := strings.TrimSpace(in.Title)
	status := in.Status
	if status == "" {
		status = StatusDraft
	}
	var fe []platform.FieldError
	fe = append(fe, checkTitle(title)...)
	fe = append(fe, checkDescription(in.Description)...)
	if !status.Valid() {
		fe = append(fe, platform.FieldError{Field: "status", Detail: "must be draft, live or archived"})
	}
	items, ife, err := s.checkItems(ctx, actor.UserID, in.ProblemIDs)
	if err != nil {
		return Quiz{}, err
	}
	fe = append(fe, ife...)
	if len(fe) > 0 {
		return Quiz{}, platform.Validation("invalid quiz", fe...)
	}
	now := s.clock.Now()
	q := Quiz{ID: s.ids.New(), OwnerID: actor.UserID, Title: title, Description: in.Description,
		Status: status, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	var out Quiz
	err = s.tx.InTx(ctx, func(r Repo) error {
		if err := r.Create(ctx, actor.UserID, q); err != nil {
			return err
		}
		if len(items) > 0 {
			if err := r.SetItems(ctx, actor.UserID, q.ID, items, now); err != nil {
				return err
			}
		}
		var err error
		out, err = r.Get(ctx, actor.UserID, q.ID)
		return err
	})
	return out, err
}

// Get returns the owner's quiz with its items.
func (s *Service) Get(ctx context.Context, ownerID, id domain.ID) (Quiz, error) {
	var q Quiz
	err := s.tx.InTx(ctx, func(r Repo) (err error) {
		q, err = r.Get(ctx, ownerID, id)
		return err
	})
	return q, err
}

// List returns one page of the owner's quizzes (optionally one status) and the
// cursor for the next page ("" on the last one).
func (s *Service) List(ctx context.Context, ownerID domain.ID, status Status, p platform.Page) ([]Quiz, string, error) {
	if status != "" && !status.Valid() {
		return nil, "", platform.Validation("invalid status", platform.FieldError{Field: "status", Detail: "must be draft, live or archived"})
	}
	after := domain.Nil
	if p.Cursor != "" {
		id, err := domain.ParseID(p.Cursor)
		if err != nil {
			return nil, "", platform.BadRequest("invalid cursor")
		}
		after = id
	}
	var out []Quiz
	err := s.tx.InTx(ctx, func(r Repo) (err error) {
		out, err = r.List(ctx, ownerID, ListFilter{Status: status, After: after, Limit: p.Limit + 1})
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

// Update applies patch. ifMatch must equal the quiz's current ETag (412
// otherwise); the check and the write share one transaction.
func (s *Service) Update(ctx context.Context, ownerID, id domain.ID, ifMatch string, patch Patch) (Quiz, error) {
	var fe []platform.FieldError
	if patch.Title != nil {
		*patch.Title = strings.TrimSpace(*patch.Title)
		fe = append(fe, checkTitle(*patch.Title)...)
	}
	if patch.Description != nil {
		fe = append(fe, checkDescription(*patch.Description)...)
	}
	if patch.Status != nil && !Status(*patch.Status).Valid() {
		fe = append(fe, platform.FieldError{Field: "status", Detail: "must be draft, live or archived"})
	}
	if len(fe) > 0 {
		return Quiz{}, platform.Validation("invalid quiz", fe...)
	}
	var out Quiz
	err := s.tx.InTx(ctx, func(r Repo) error {
		q, err := r.Get(ctx, ownerID, id)
		if err != nil {
			return err
		}
		if err := platform.CheckIfMatch(ifMatch, ETag(q)); err != nil {
			return err
		}
		if patch.Title != nil {
			q.Title = *patch.Title
		}
		if patch.Description != nil {
			q.Description = *patch.Description
		}
		if patch.Status != nil {
			q.Status = Status(*patch.Status)
		}
		q.UpdatedAt = s.clock.Now()
		if err := r.Update(ctx, ownerID, q); err != nil {
			return err
		}
		out, err = r.Get(ctx, ownerID, id)
		return err
	})
	return out, err
}

// SetItems replaces the quiz's items with the problems refs name, in order.
// Every ref must be a distinct problem of the owner; otherwise it is a 422
// that names the offending entries and changes nothing.
func (s *Service) SetItems(ctx context.Context, ownerID, id domain.ID, ifMatch string, refs []string) (Quiz, error) {
	// Existence first, so another owner's quiz is a 404 whatever the body says.
	if _, err := s.Get(ctx, ownerID, id); err != nil {
		return Quiz{}, err
	}
	items, fe, err := s.checkItems(ctx, ownerID, refs)
	if err != nil {
		return Quiz{}, err
	}
	if len(fe) > 0 {
		return Quiz{}, platform.Validation("invalid items", fe...)
	}
	var out Quiz
	err = s.tx.InTx(ctx, func(r Repo) error {
		q, err := r.Get(ctx, ownerID, id)
		if err != nil {
			return err
		}
		if err := platform.CheckIfMatch(ifMatch, ETag(q)); err != nil {
			return err
		}
		if err := r.SetItems(ctx, ownerID, id, items, s.clock.Now()); err != nil {
			return err
		}
		out, err = r.Get(ctx, ownerID, id)
		return err
	})
	if errors.Is(err, ErrItemNotFound) {
		// A problem vanished between the check and the write.
		return Quiz{}, platform.Validation("invalid items", platform.FieldError{Field: "problem_ids", Detail: "no such problem"})
	}
	return out, err
}

// Delete soft-deletes the quiz. ifMatch must equal its current ETag.
func (s *Service) Delete(ctx context.Context, ownerID, id domain.ID, ifMatch string) error {
	return s.tx.InTx(ctx, func(r Repo) error {
		q, err := r.Get(ctx, ownerID, id)
		if err != nil {
			return err
		}
		if err := platform.CheckIfMatch(ifMatch, ETag(q)); err != nil {
			return err
		}
		return r.SoftDelete(ctx, ownerID, id, s.clock.Now())
	})
}

// checkItems parses refs and verifies each is a distinct problem of the owner.
// The check runs before any quiz transaction opens, because the problems
// service has its own. Another owner's problem reads as "no such problem".
func (s *Service) checkItems(ctx context.Context, ownerID domain.ID, refs []string) ([]domain.ID, []platform.FieldError, error) {
	if len(refs) > maxItems {
		return nil, []platform.FieldError{{Field: "problem_ids", Detail: fmt.Sprintf("at most %d items per quiz", maxItems)}}, nil
	}
	var (
		ids  []domain.ID
		fe   []platform.FieldError
		seen = make(map[domain.ID]bool, len(refs))
	)
	for i, ref := range refs {
		field := fmt.Sprintf("problem_ids[%d]", i)
		id, err := domain.ParseID(ref)
		if err != nil {
			fe = append(fe, platform.FieldError{Field: field, Detail: "no such problem"})
			continue
		}
		if seen[id] {
			fe = append(fe, platform.FieldError{Field: field, Detail: "duplicate problem"})
			continue
		}
		seen[id] = true
		if _, err := s.problems.Get(ctx, ownerID, id); err != nil {
			var p *platform.Problem
			if errors.As(err, &p) && p.Status == http.StatusNotFound || errors.Is(err, problems.ErrNotFound) {
				fe = append(fe, platform.FieldError{Field: field, Detail: "no such problem"})
				continue
			}
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	return ids, fe, nil
}

func checkTitle(t string) []platform.FieldError {
	switch n := utf8.RuneCountInString(t); {
	case n == 0:
		return []platform.FieldError{{Field: "title", Detail: "is required"}}
	case n > maxTitleLen:
		return []platform.FieldError{{Field: "title", Detail: fmt.Sprintf("at most %d characters", maxTitleLen)}}
	}
	return nil
}

func checkDescription(d string) []platform.FieldError {
	if utf8.RuneCountInString(d) > maxDescriptionLen {
		return []platform.FieldError{{Field: "description", Detail: fmt.Sprintf("at most %d characters", maxDescriptionLen)}}
	}
	return nil
}
