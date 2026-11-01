// SPDX-License-Identifier: Apache-2.0

package problems_test

import (
	"context"
	"errors"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// failingBinder fails SetProblemTags on its n-th call, after earlier items
// were already created and tagged inside the transaction.
type failingBinder struct {
	problems.TagBinder
	calls, failOn int
}

func (b *failingBinder) SetProblemTags(ctx context.Context, o, p domain.ID, ids []domain.ID) error {
	b.calls++
	if b.calls == b.failOn {
		return errors.New("injected failure")
	}
	return b.TagBinder.SetProblemTags(ctx, o, p, ids)
}

type failingTx struct {
	st     store.Store
	failOn int
}

func (f failingTx) InBatchTx(ctx context.Context, fn func(problems.BatchScope) error) error {
	return f.st.WithTx(ctx, func(tx store.Store) error {
		return fn(problems.BatchScope{
			Problems: tx.Problems(),
			Tags:     &failingBinder{TagBinder: tags.NewBinder(tx.Tags()), failOn: f.failOn},
		})
	})
}

func TestCreateBatchRollsBackOnMidBatchFailure(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	owner := f.actor.UserID
	tag := tags.Tag{ID: domain.NewIDGenerator(f.clock).New(), OwnerID: owner, Slug: "bio", Name: "Bio",
		Status: tags.StatusActive, CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now()}
	if err := f.st.Tags().Create(ctx, owner, tag); err != nil {
		t.Fatal(err)
	}
	svc := problems.NewService(f.st.Problems(), store.ProblemsTx(f.st), f.clock,
		domain.NewIDGenerator(f.clock), problems.WithBatch(failingTx{st: f.st, failOn: 3}))

	var items []problems.BatchItem
	for i := 0; i < 4; i++ {
		in := mcqInput(false)
		in.Prompt = "Batch item"
		items = append(items, problems.BatchItem{CreateInput: in, Tags: []string{"bio"}})
	}
	_, err := svc.CreateBatch(ctx, f.actor, items)
	if err == nil || err.Error() != "injected failure" {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	var pb *platform.Problem
	if errors.As(err, &pb) {
		t.Fatalf("unexpected client problem %v", pb)
	}
	rows, err := f.st.Problems().List(ctx, owner, problems.ListFilter{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("problems after rollback = %d, err %v; want 0", len(rows), err)
	}
	ids, err := f.st.Tags().ProblemIDs(ctx, owner, tag.ID, domain.Nil, 0)
	if err != nil || len(ids) != 0 {
		t.Fatalf("tag attachments after rollback = %d, err %v; want 0", len(ids), err)
	}
}
