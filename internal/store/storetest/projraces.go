// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// holdOwnerLock runs during inside a transaction that holds the owner's lock
// (the one every tag, card and mastery unit of work takes before it reads),
// and returns when the transaction has committed. It is how the tests below
// force "another unit of work is in the middle of its work" without relying on
// timing.
func holdOwnerLock(t *testing.T, s store.Store, owner domain.ID, during func()) {
	t.Helper()
	ctx := context.Background()
	err := store.TagsTx(s).InTx(ctx, func(r tags.Repos) error {
		if err := r.Tags.LockOwner(ctx, owner); err != nil {
			return err
		}
		during()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// blockedWhileLockHeld starts run in a goroutine while another transaction
// holds the owner's lock and reports whether run was still waiting after a
// grace period. It then lets the holder commit and requires run to finish
// without error.
func blockedWhileLockHeld(t *testing.T, s store.Store, owner domain.ID, run func() error) (blocked bool) {
	t.Helper()
	done := make(chan error, 1)
	holdOwnerLock(t, s, owner, func() {
		go func() { done <- run() }()
		select {
		case err := <-done:
			done <- err // finished while the lock was held
		case <-time.After(500 * time.Millisecond):
			blocked = true
		}
	})
	select {
	case err := <-done:
		var p *platform.Problem
		if err != nil && !errors.As(err, &p) {
			t.Fatalf("unit of work failed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("unit of work still blocked after the lock was released")
	}
	return blocked
}

// runProjectionRaces checks that the card and mastery projections, which are
// refolded from the attempt log inside each attempt's transaction, still equal
// a full rebuild after concurrent writers (a lost update would leave a card or
// mastery row that misses an attempt). See docs/adr/0007-owner-tag-lock.md.
func runProjectionRaces(t *testing.T, newStore NewStore) {
	const rounds = 25
	right := pkinds[0].right // MCQ: graded, so every answer is scheduled

	// together runs fns behind one barrier and returns their errors.
	together := func(fns []func() error) []error {
		errs := make([]error, len(fns))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, fn := range fns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = fn()
			}()
		}
		close(start)
		wg.Wait()
		return errs
	}
	domainErr := func(t *testing.T, round int, errs []error) {
		t.Helper()
		for _, err := range errs {
			var p *platform.Problem
			if err != nil && !errors.As(err, &p) {
				t.Fatalf("round %d: unexpected error: %v", round, err)
			}
		}
	}

	t.Run("concurrent answers to one problem all reach its card", func(t *testing.T) {
		f := newMasteryFixture(t, newStore)
		for round := 0; round < rounds; round++ {
			p := f.newProblem(pkinds[0], problems.StatusLive)
			var fns []func() error
			for i := 0; i < 6; i++ {
				fns = append(fns, func() error { _, err := f.answer(p.ID, right, nil); return err })
			}
			domainErr(t, round, together(fns))
			c, ok := f.card(p.ID)
			if !ok {
				t.Fatalf("round %d: no card after answers", round)
			}
			if c.Reps != 6 {
				t.Fatalf("round %d: card has %d reps after 6 answers", round, c.Reps)
			}
		}
	})

	t.Run("mastery rows equal a rebuild after concurrent answers", func(t *testing.T) {
		f := newMasteryFixture(t, newStore)
		tag := f.newTag(f.user, "shared")
		for round := 0; round < rounds; round++ {
			var fns []func() error
			for i := 0; i < 6; i++ {
				p := f.newProblem(pkinds[0], problems.StatusLive)
				f.tag(f.user, p.ID, tag)
				fns = append(fns, func() error { _, err := f.answer(p.ID, right, nil); return err })
			}
			domainErr(t, round, together(fns))
			got := f.rows(f.user)
			f.rebuild(f.user)
			if want := f.rows(f.user); !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d: incremental mastery %+v differs from rebuild %+v", round, got, want)
			}
		}
	})

	// The card of the old epoch is dropped by the major edit. If the edit did
	// not take the owner's lock, an answer that read the old epoch could put
	// its card after the edit's delete and leave a card for a stale epoch. The
	// race itself is too narrow to hit by chance, so hold the lock as that
	// answer would and require the edit to wait for it.
	t.Run("a major edit waits for the owner's lock", func(t *testing.T) {
		f := newMasteryFixture(t, newStore)
		p := f.newProblem(pkinds[0], problems.StatusLive)
		if _, err := f.answer(p.ID, right, nil); err != nil {
			t.Fatal(err)
		}
		v, err := f.probs.Get(f.ctx, f.user, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		prompt := "edited"
		blocked := blockedWhileLockHeld(t, f.s, f.user, func() error {
			_, err := f.probs.Edit(f.ctx, f.actor, p.ID, v.ETag(), problems.EditInput{ChangeKind: problems.ChangeMajor, Prompt: &prompt})
			return err
		})
		if !blocked {
			t.Fatal("a major edit ran while another unit of work held the owner's lock")
		}
		if _, ok := f.card(p.ID); ok {
			t.Error("the old epoch's card survived the major edit")
		}
	})

	t.Run("a major edit and an answer leave no card for a stale epoch", func(t *testing.T) {
		f := newMasteryFixture(t, newStore)
		for round := 0; round < rounds; round++ {
			p := f.newProblem(pkinds[0], problems.StatusLive)
			if _, err := f.answer(p.ID, right, nil); err != nil {
				t.Fatal(err)
			}
			v, err := f.probs.Get(f.ctx, f.user, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			prompt := fmt.Sprintf("edited %d", round)
			errs := together([]func() error{
				func() error {
					_, err := f.probs.Edit(f.ctx, f.actor, p.ID, v.ETag(), problems.EditInput{ChangeKind: problems.ChangeMajor, Prompt: &prompt})
					return err
				},
				func() error { _, err := f.answer(p.ID, right, nil); return err },
				func() error { _, err := f.answer(p.ID, right, nil); return err },
			})
			domainErr(t, round, errs)
			got := f.list()
			if _, err := f.svc.RebuildUser(f.ctx, f.user); err != nil {
				t.Fatal(err)
			}
			if want := f.list(); !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d: incremental cards %+v differ from rebuild %+v", round, got, want)
			}
		}
	})
}
