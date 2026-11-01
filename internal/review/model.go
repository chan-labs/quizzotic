// SPDX-License-Identifier: Apache-2.0

package review

import (
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
)

// Card is one user's scheduling state for one problem at one card epoch. It is
// a projection: a fold over the attempt log (see Fold), never a source of
// truth, so it can be dropped and rebuilt at any time.
//
// A user has at most one stored card per problem, at the problem's current
// epoch. A problem with no scheduled attempt at its current epoch has no row;
// that is a new card by definition, created lazily by its first answer.
type Card struct {
	UserID    domain.ID
	ProblemID domain.ID
	// Epoch is the card epoch the card folds: it starts at 1 and grows by one
	// at every major problem version.
	Epoch int
	// The FSRS scheduling state: State, Step, Stability, Difficulty, Due and
	// LastReview.
	fsrs.Card
	// Reps is the number of reviews folded; Lapses is how many of them were
	// Again on a card in the Review state.
	Reps, Lapses int
}
