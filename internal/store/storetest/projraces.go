// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

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
