// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"

	"github.com/chan-labs/quizzotic/internal/domain"
)

// ItemQuiz is a problem served by a quiz session, whether or not the user has
// a card for it yet.
const ItemQuiz ItemKind = "quiz"

// QuizSource reads a quiz's problems. The store adapts quizzes.Repo to it, so
// this package never imports the quizzes feature.
type QuizSource interface {
	// QuizProblems returns the problem IDs of the owner's quiz in authored
	// order, or ErrNotFound when the quiz is missing, deleted or not the
	// owner's.
	QuizProblems(ctx context.Context, ownerID, quizID domain.ID) ([]domain.ID, error)
}

// QuizBuilder builds quiz sessions (spec: Quiz sessions). Like Builder it only
// reads; the session service persists the Plan (M4.7). Answers go through the
// normal attempt path, so they update cards like any other attempt.
type QuizBuilder struct {
	quizzes  QuizSource
	problems ProblemSource
}

// NewQuizBuilder builds a QuizBuilder over the quiz and problem sources.
func NewQuizBuilder(q QuizSource, p ProblemSource) *QuizBuilder {
	return &QuizBuilder{quizzes: q, problems: p}
}

// Build returns the items of the quiz session sessionID: the quiz's live
// problems in authored order, or, when shuffle is set, in an order that is a
// pure function of sessionID and the quiz's contents. Draft, archived and
// deleted problems are skipped. Plan.Day and Plan.Used are zero: daily limits
// do not apply to a quiz.
func (b *QuizBuilder) Build(ctx context.Context, userID, sessionID, quizID domain.ID, shuffle bool) (Plan, error) {
	ids, err := b.quizzes.QuizProblems(ctx, userID, quizID)
	if err != nil {
		return Plan{}, err
	}
	live, err := b.problems.LiveEpochs(ctx, userID)
	if err != nil {
		return Plan{}, err
	}
	epoch := make(map[domain.ID]int, len(live))
	for _, pe := range live {
		if pe.Live {
			epoch[pe.ID] = pe.Epoch
		}
	}
	items := make([]Item, 0, len(ids))
	for _, id := range ids { // authored order is the stable base order
		if e, ok := epoch[id]; ok {
			items = append(items, Item{ProblemID: id, Epoch: e, Kind: ItemQuiz})
		}
	}
	if shuffle {
		sum := sha256.Sum256(sessionID[:])
		rng := rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16])))
		rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	}
	return Plan{Items: items}, nil
}
