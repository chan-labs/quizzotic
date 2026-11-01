// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"context"
	"fmt"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// transitions is the complete table of allowed status changes. The spec fixes
// only that a card exists for live problems and that drafts and archived
// problems never enter review; it is silent on which moves are legal, so the
// table is kept minimal:
//
//	draft    -> live      (publish)
//	live     -> archived  (archive)
//
// Everything else is refused with 409: no live -> draft (a published problem
// may already have cards and attempts), no archived -> live (restoring is a
// product decision the spec has not made), no draft -> archived (discard a
// draft by deleting it), and no same-status moves. Soft delete is separate
// and allowed from every status. A status change emits no event: the spec
// defines none, and cards are created lazily from live problems only.
var transitions = map[Status]map[Status]bool{
	StatusDraft:    {StatusLive: true},
	StatusLive:     {StatusArchived: true},
	StatusArchived: {},
}

// CanTransition reports whether a problem may move from one status to another.
func CanTransition(from, to Status) bool { return transitions[from][to] }

// Publish moves a draft problem to live. ifMatch is the request's If-Match
// value, checked against the current ETag inside the write transaction.
func (s *Service) Publish(ctx context.Context, ownerID, id domain.ID, ifMatch string) (View, error) {
	return s.transition(ctx, ownerID, id, ifMatch, StatusLive)
}

// Archive moves a live problem to archived.
func (s *Service) Archive(ctx context.Context, ownerID, id domain.ID, ifMatch string) (View, error) {
	return s.transition(ctx, ownerID, id, ifMatch, StatusArchived)
}

func (s *Service) transition(ctx context.Context, ownerID, id domain.ID, ifMatch string, to Status) (View, error) {
	var out View
	err := s.inEditTx(ctx, func(r Repo, h MajorEditHandler) error {
		p, v, err := currentIn(ctx, r, ownerID, id)
		if err != nil {
			return err
		}
		if err := platform.CheckIfMatch(ifMatch, newView(p, v).ETag()); err != nil {
			return err
		}
		if !CanTransition(p.Status, to) {
			return platform.Conflict(fmt.Sprintf("a %s problem cannot become %s", p.Status, to))
		}
		if err := r.SetStatus(ctx, ownerID, id, to, s.clock.Now()); err != nil {
			return notFound(err)
		}
		if to == StatusArchived {
			if err := retired(ctx, h, ownerID, id); err != nil {
				return err
			}
		}
		p, v, err = currentIn(ctx, r, ownerID, id)
		if err != nil {
			return err
		}
		out = newView(p, v)
		return nil
	})
	return out, err
}

// Delete soft-deletes the problem from any status. Versions are kept.
func (s *Service) Delete(ctx context.Context, ownerID, id domain.ID, ifMatch string) error {
	return s.inEditTx(ctx, func(r Repo, h MajorEditHandler) error {
		p, v, err := currentIn(ctx, r, ownerID, id)
		if err != nil {
			return err
		}
		if err := platform.CheckIfMatch(ifMatch, newView(p, v).ETag()); err != nil {
			return err
		}
		if err := r.SoftDelete(ctx, ownerID, id, s.clock.Now()); err != nil {
			return notFound(err)
		}
		return retired(ctx, h, ownerID, id)
	})
}

func currentIn(ctx context.Context, r Repo, ownerID, id domain.ID) (Problem, Version, error) {
	p, err := r.Get(ctx, ownerID, id)
	if err != nil {
		return Problem{}, Version{}, notFound(err)
	}
	v, err := r.GetVersion(ctx, ownerID, id, p.CurrentVersion)
	if err != nil {
		return Problem{}, Version{}, notFound(err)
	}
	return p, v, nil
}
