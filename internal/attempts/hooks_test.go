// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
)

type countRecorder struct{ n int }

func (c *countRecorder) RecordAttempt() { c.n++ }

func TestAttemptRecorderCountsStoredAttemptsOnly(t *testing.T) {
	e, _ := rateEnv(t)
	rec := &countRecorder{}
	clock := fakeClock{now}
	e.svc = NewService(e.repo, e.svc.problems, clock, domain.NewIDGenerator(clock),
		WithCardUpdater(e.cards), WithAttemptRecorder(rec))

	answer(t, e, e.mcq, 2, `{"selected":["b"]}`, nil)
	answer(t, e, e.open, 1, `{"text":"unrated is still stored"}`, nil)
	if rec.n != 2 {
		t.Fatalf("recorded %d attempts, want 2", rec.n)
	}

	e.cards.failed = errors.New("card store down")
	if _, err := e.svc.Answer(context.Background(), human(e.user), AnswerInput{ProblemID: e.mcq, ProblemVersion: 2,
		Response: raw(`{"selected":["b"]}`)}); err == nil {
		t.Fatal("want the card failure")
	}
	if rec.n != 2 {
		t.Fatalf("a rolled-back attempt was recorded: %d", rec.n)
	}
}

// cardTx is a memRepo that also supplies its own card updater, as
// store.AttemptsCardTx does.
type cardTx struct {
	*memRepo
	cards CardUpdater
}

func (c cardTx) InCardTx(ctx context.Context, fn func(Repo, CardUpdater) error) error {
	return c.memRepo.InTx(ctx, func(r Repo) error { return fn(r, c.cards) })
}

func TestCardTransactorSuppliesTheUpdater(t *testing.T) {
	e, _ := rateEnv(t)
	bound := &recordingCards{}
	clock := fakeClock{now}
	e.svc = NewService(cardTx{e.repo, bound}, e.svc.problems, clock, domain.NewIDGenerator(clock), WithCardUpdater(e.cards))

	due := now.Add(time.Hour)
	bound.next = &due
	res := answer(t, e, e.mcq, 2, `{"selected":["b"]}`, nil)
	if len(bound.calls) != 1 || len(e.cards.calls) != 0 || res.NextDue == nil || !res.NextDue.Equal(due) {
		t.Fatalf("bound=%v option=%v next=%v; the transaction's updater must win", bound.calls, e.cards.calls, res.NextDue)
	}
}
