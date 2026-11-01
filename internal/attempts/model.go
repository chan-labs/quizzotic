// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"encoding/json"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// Attempt is one stored answer. Attempts are append-only: the only change ever
// made to a stored attempt is the one-time fill of a missing rating (and the
// score that follows from it for an open problem).
type Attempt struct {
	ID             domain.ID
	UserID         domain.ID
	ProblemID      domain.ID
	ProblemVersion int
	CardEpoch      int
	SessionID      domain.ID // domain.Nil when the answer belongs to no session
	// Response is the answer given, as compact JSON shaped by the problem type.
	Response json.RawMessage
	// AutoCorrect is whether the answer was fully right; nil for open problems,
	// which are not auto-graded.
	AutoCorrect *bool
	// Score is 0-1; nil for an open attempt that has no rating yet.
	Score *float64
	// Rating is the rating the client sent (1-4); nil when it sent none.
	Rating     *int
	LatencyMS  int
	AnsweredAt time.Time
	Client     domain.Actor
}
