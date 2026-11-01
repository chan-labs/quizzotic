// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// MajorEdit is the signal that a problem's content changed so much that its
// card must start a new epoch. Attempts already recorded stay attached to the
// old epoch.
type MajorEdit struct {
	OwnerID     domain.ID
	ProblemID   domain.ID
	FromVersion int
	ToVersion   int
	At          time.Time
}

// MajorEditHandler is implemented by the review feature. Service calls it
// inside the edit's transaction, so an error aborts the edit and the card
// epoch can never diverge from the version history. The default is a no-op.
type MajorEditHandler interface {
	ProblemMajorEdit(ctx context.Context, e MajorEdit) error
}

type noopMajorEdit struct{}

func (noopMajorEdit) ProblemMajorEdit(context.Context, MajorEdit) error { return nil }

// EditTransactor is a Transactor that can also hand the edit's own
// transaction to the major-edit handler, so a handler that writes (the card
// projection) writes in the same transaction as the edit. When Service's
// Transactor implements it, the handler it supplies is used for that edit and
// the one set with WithMajorEditHandler is not.
type EditTransactor interface {
	Transactor
	InEditTx(ctx context.Context, fn func(r Repo, onMajor MajorEditHandler) error) error
}

// inEditTx runs fn in a transaction with the major-edit handler for it.
func (s *Service) inEditTx(ctx context.Context, fn func(Repo, MajorEditHandler) error) error {
	if et, ok := s.tx.(EditTransactor); ok {
		return et.InEditTx(ctx, fn)
	}
	return s.tx.InTx(ctx, func(r Repo) error { return fn(r, s.onMajor) })
}

// WithMajorEditHandler sets who is told about major edits.
func WithMajorEditHandler(h MajorEditHandler) Option {
	return func(s *Service) {
		if h != nil {
			s.onMajor = h
		}
	}
}

// EditInput is a partial update; nil fields keep the current version's value.
type EditInput struct {
	// ChangeKind is required: minor keeps the card, major starts a new epoch.
	ChangeKind  ChangeKind
	Type        *string
	Prompt      *string
	Payload     json.RawMessage // nil: unchanged
	Explanation *string
	Source      *string
}

// VersionView is one immutable revision of a problem, with answers.
type VersionView struct {
	Version     int             `json:"version"`
	Type        string          `json:"type"`
	Prompt      string          `json:"prompt"`
	Payload     json.RawMessage `json:"payload"`
	Explanation string          `json:"explanation"`
	Source      string          `json:"source"`
	ChangeKind  ChangeKind      `json:"change_kind"`
	CreatedBy   ActorView       `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Edit merges in onto the problem's current version and stores the result as
// a new version. ifMatch must satisfy the problem's current ETag; the check
// and the write share one transaction.
func (s *Service) Edit(ctx context.Context, actor domain.Actor, id domain.ID, ifMatch string, in EditInput) (View, error) {
	if err := actor.Validate(); err != nil || actor.UserID.IsNil() {
		return View{}, errors.New("problems: edit needs an actor with a user")
	}
	if !in.ChangeKind.Valid() {
		return View{}, platform.Validation("the edit is invalid",
			platform.FieldError{Field: "change_kind", Detail: "must be minor or major"})
	}
	if in.Type == nil && in.Prompt == nil && in.Payload == nil && in.Explanation == nil && in.Source == nil {
		return View{}, platform.Validation("the edit is invalid",
			platform.FieldError{Field: "body", Detail: "change at least one of type, prompt, payload, explanation, source"})
	}
	var out View
	err := s.inEditTx(ctx, func(r Repo, onMajor MajorEditHandler) error {
		p, err := r.Get(ctx, actor.UserID, id)
		if err != nil {
			return notFound(err)
		}
		cur, err := r.GetVersion(ctx, actor.UserID, id, p.CurrentVersion)
		if err != nil {
			return notFound(err)
		}
		if err := platform.CheckIfMatch(ifMatch, newView(p, cur).ETag()); err != nil {
			return err
		}

		next := Version{ProblemID: id, Type: cur.Type, Prompt: cur.Prompt, Payload: cur.Payload,
			Explanation: cur.Explanation, Source: cur.Source, ChangeKind: in.ChangeKind,
			CreatedBy: actor, CreatedAt: s.clock.Now()}
		if in.Type != nil {
			next.Type = *in.Type
		}
		if in.Prompt != nil {
			next.Prompt = *in.Prompt
		}
		if in.Explanation != nil {
			next.Explanation = *in.Explanation
		}
		if in.Source != nil {
			next.Source = *in.Source
		}
		if in.Payload != nil {
			var buf bytes.Buffer
			if err := json.Compact(&buf, in.Payload); err != nil {
				return platform.BadRequest("payload is not valid JSON")
			}
			next.Payload = buf.Bytes()
		}
		var fields []platform.FieldError
		if strings.TrimSpace(next.Prompt) == "" {
			fields = append(fields, platform.FieldError{Field: "prompt", Detail: "required"})
		}
		fields = append(fields, ValidatePayload(next.Type, next.Prompt, next.Payload)...)
		if len(fields) > 0 {
			return platform.Validation("the problem is invalid", fields...)
		}

		stored, err := r.AppendVersion(ctx, actor.UserID, id, next)
		if err != nil {
			return notFound(err)
		}
		if in.ChangeKind == ChangeMajor {
			err := onMajor.ProblemMajorEdit(ctx, MajorEdit{OwnerID: actor.UserID, ProblemID: id,
				FromVersion: cur.Version, ToVersion: stored.Version, At: stored.CreatedAt})
			if err != nil {
				return err
			}
		}
		p.CurrentVersion, p.UpdatedAt = stored.Version, stored.CreatedAt
		out = newView(p, stored)
		return nil
	})
	return out, err
}

// Versions returns every version of the owner's problem, oldest first.
func (s *Service) Versions(ctx context.Context, ownerID, id domain.ID) ([]VersionView, error) {
	vs, err := s.repo.ListVersions(ctx, ownerID, id)
	if err != nil {
		return nil, notFound(err)
	}
	out := make([]VersionView, 0, len(vs))
	for _, v := range vs {
		out = append(out, VersionView{Version: v.Version, Type: v.Type, Prompt: v.Prompt, Payload: v.Payload,
			Explanation: v.Explanation, Source: v.Source, ChangeKind: v.ChangeKind,
			CreatedBy: actorView(v.CreatedBy), CreatedAt: v.CreatedAt})
	}
	return out, nil
}
