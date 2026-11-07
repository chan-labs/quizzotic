// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"context"
	"encoding/json"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
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
func (s *Service) ForAnswer(ctx context.Context, ownerID, id domain.ID) (_ AnswerSubject, err error) {
	ctx, end := platform.TraceMethod(ctx, "problems.ForAnswer")
	defer end(&err)
	p, v, err := s.current(ctx, ownerID, id)
	if err != nil {
		return AnswerSubject{}, err
	}
	versions, err := s.repo.ListVersions(ctx, ownerID, id)
	if err != nil {
		return AnswerSubject{}, notFound(err)
	}
	epoch := CardEpoch(versions, p.CurrentVersion)
	return AnswerSubject{ID: p.ID, Status: p.Status, Version: p.CurrentVersion, Type: v.Type,
		Payload: v.Payload, Explanation: v.Explanation, CardEpoch: epoch}, nil
}

// CardEpoch is the card epoch of a problem whose current version is current:
// the number of major versions up to it, at least 1. A major edit starts a new
// epoch; a minor edit keeps it.
func CardEpoch(versions []Version, current int) int {
	epoch := 0
	for _, v := range versions {
		if v.Version <= current && v.ChangeKind == ChangeMajor {
			epoch++
		}
	}
	return max(epoch, 1)
}
