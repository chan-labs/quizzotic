// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"context"
	"encoding/json"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// AnswerSubject is what the answer flow needs to grade an attempt: the
// problem's current version with its answers, and the card epoch that version
// belongs to.
type AnswerSubject struct {
	ID          domain.ID
	Status      Status
	Version     int
	Type        string
	Payload     json.RawMessage
	Explanation string
	// CardEpoch starts at 1 and grows by one at every major version, so a
	// major edit starts a new card while a minor edit keeps it.
	CardEpoch int
}

// ForAnswer returns the owner's non-deleted problem as the answer flow sees
// it, or a 404 problem for a missing, deleted or foreign one.
func (s *Service) ForAnswer(ctx context.Context, ownerID, id domain.ID) (AnswerSubject, error) {
	p, v, err := s.current(ctx, ownerID, id)
	if err != nil {
		return AnswerSubject{}, err
	}
	versions, err := s.repo.ListVersions(ctx, ownerID, id)
	if err != nil {
		return AnswerSubject{}, notFound(err)
	}
	epoch := 0
	for _, ver := range versions {
		if ver.Version <= p.CurrentVersion && ver.ChangeKind == ChangeMajor {
			epoch++
		}
	}
	epoch = max(epoch, 1)
	return AnswerSubject{ID: p.ID, Status: p.Status, Version: p.CurrentVersion, Type: v.Type,
		Payload: v.Payload, Explanation: v.Explanation, CardEpoch: epoch}, nil
}
