// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// runTagRaces drives the tag service from many goroutines at once and checks
// the invariants that a read-then-write path can lose when two transactions
// interleave (Postgres runs at READ COMMITTED; SQLite serializes writers, so
// it passes trivially). Each round starts all goroutines behind one barrier.
// See docs/adr/0007-owner-tag-lock.md.
func runTagRaces(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})
	const rounds = 60

	type fx struct {
		s     store.Store
		svc   *tags.Service
		owner auth.User
	}
	setup := func(t *testing.T) *fx {
		t.Helper()
		s, _ := newStore(t)
		u := newUser(ids, "racer")
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		return &fx{s: s, svc: tags.NewService(store.TagsTx(s), domain.SystemClock{}, ids), owner: u}
	}
	actor := func(f *fx) domain.Actor { return domain.Actor{Kind: domain.ActorHuman, UserID: f.owner.ID} }
	mkTag := func(t *testing.T, f *fx, slug string) tags.Tag {
		t.Helper()
		tg, err := f.svc.Create(ctx, f.owner.ID, tags.CreateInput{Slug: slug, Name: "Name " + slug})
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	mkProblem := func(t *testing.T, f *fx) domain.ID {
		t.Helper()
		act := actor(f)
		id := ids.New()
		p := problems.Problem{ID: id, OwnerID: f.owner.ID, Status: problems.StatusLive, CurrentVersion: 1,
			CreatedBy: act, CreatedAt: t0, UpdatedAt: t0}
		v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: "q",
			Payload: json.RawMessage(`{"reference_answer":"a"}`), ChangeKind: problems.ChangeMajor, CreatedBy: act, CreatedAt: t0}
		if err := f.s.Problems().Create(ctx, f.owner.ID, p, v); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// race runs fns together behind a barrier and returns their errors.
	race := func(fns ...func() error) []error {
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
	// expected reports whether err is a domain outcome a caller can handle;
	// anything else (a raw driver error) is an unhandled race.
	expected := func(err error) bool {
		var p *platform.Problem
		return err == nil || errors.As(err, &p) || errors.Is(err, tags.ErrSlugTaken) ||
			errors.Is(err, tags.ErrNotFound) || errors.Is(err, tags.ErrRevertConflict)
	}
	check := func(t *testing.T, round int, errs []error) {
		t.Helper()
		for _, err := range errs {
			if !expected(err) {
				t.Fatalf("round %d: unexpected error: %v", round, err)
			}
		}
	}
	all := func(t *testing.T, f *fx) []tags.Tag {
		t.Helper()
		var out []tags.Tag
		for _, st := range []tags.Status{tags.StatusActive, tags.StatusMerged, tags.StatusRetired} {
			ts, err := f.s.Tags().List(ctx, f.owner.ID, tags.ListFilter{Status: st})
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, ts...)
		}
		return out
	}

	t.Run("slug namespace stays unique across tags and aliases", func(t *testing.T) {
		f := setup(t)
		for round := 0; round < rounds; round++ {
			var ts []tags.Tag
			for i := 0; i < 4; i++ {
				ts = append(ts, mkTag(t, f, fmt.Sprintf("r%d-t%d", round, i)))
			}
			slug := func(i int) string { return ts[i].Slug }
			s0, s1, s2 := slug(0), slug(1), slug(2)
			rename := func(ref, to string) func() error {
				return func() error {
					_, err := f.svc.Rename(ctx, f.owner.ID, tags.RenameInput{Tag: ref, Slug: &to})
					return err
				}
			}
			create := func(slug string) func() error {
				return func() error {
					_, err := f.svc.Create(ctx, f.owner.ID, tags.CreateInput{Slug: slug, Name: slug})
					return err
				}
			}
			merge := func(from, into string) func() error {
				return func() error {
					_, err := f.svc.Merge(ctx, f.owner.ID, tags.MergeInput{Sources: []string{from}, Target: into})
					return err
				}
			}
			check(t, round, race(
				rename(s0, "n-"+s0), // s0 becomes an alias of t0
				create(s0),          // takes the slug rename is vacating
				rename(s1, s0),      // takes it too
				merge(s2, s0),       // aliases s2 to t0 while t0 is being renamed
				merge(s2, s1),
				create(s2),
			))
			// Invariant: a slug is held at most once among active tags and aliases.
			held := map[string]string{}
			for _, tg := range all(t, f) {
				if tg.Status == tags.StatusActive {
					if prev, ok := held[tg.Slug]; ok {
						t.Fatalf("round %d: slug %q held by tag %s and %s", round, tg.Slug, prev, tg.ID)
					}
					held[tg.Slug] = tg.ID.String()
				}
			}
			for _, tg := range all(t, f) {
				for _, a := range tg.Aliases {
					if prev, ok := held[a]; ok && prev != tg.ID.String() {
						t.Fatalf("round %d: alias %q of tag %s is also held by %s", round, a, tg.ID, prev)
					}
				}
			}
		}
	})

	t.Run("merge and set-problem-tags never leave a mapping on a merged tag", func(t *testing.T) {
		f := setup(t)
		for round := 0; round < rounds; round++ {
			src := mkTag(t, f, fmt.Sprintf("m%d-src", round))
			dst := mkTag(t, f, fmt.Sprintf("m%d-dst", round))
			var ps []domain.ID
			for i := 0; i < 8; i++ {
				ps = append(ps, mkProblem(t, f))
			}
			fns := []func() error{func() error {
				_, err := f.svc.Merge(ctx, f.owner.ID, tags.MergeInput{Sources: []string{src.Slug}, Target: dst.Slug})
				return err
			}}
			for _, p := range ps {
				fns = append(fns, func() error {
					_, err := f.svc.SetProblemTags(ctx, f.owner.ID, p, []string{src.Slug})
					return err
				})
			}
			check(t, round, race(fns...))
			got, err := f.s.Tags().ProblemIDs(ctx, f.owner.ID, src.ID, domain.Nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("round %d: %d problems still map to the merged tag", round, len(got))
			}
		}
	})

	// A batch create resolves its tag references with Binder.ResolveTags and
	// tags the new problems in the same transaction. The resolve must take the
	// owner's lock, or a merge that commits in between leaves problems mapped
	// to the merged tag.
	resolveAndTag := func(f *fx, p domain.ID, ref string) error {
		return store.TagsTx(f.s).InTx(ctx, func(r tags.Repos) error {
			b := tags.NewBinder(r.Tags)
			ids, fe, err := b.ResolveTags(ctx, f.owner.ID, []string{ref})
			if err != nil || len(fe) > 0 {
				return err
			}
			return b.SetProblemTags(ctx, f.owner.ID, p, ids)
		})
	}
	t.Run("Binder.ResolveTags waits for the owner's lock", func(t *testing.T) {
		f := setup(t)
		tg := mkTag(t, f, "bound")
		blocked := blockedWhileLockHeld(t, f.s, f.owner.ID, func() error {
			return store.TagsTx(f.s).InTx(ctx, func(r tags.Repos) error {
				_, _, err := tags.NewBinder(r.Tags).ResolveTags(ctx, f.owner.ID, []string{tg.Slug})
				return err
			})
		})
		if !blocked {
			t.Fatal("ResolveTags ran while another unit of work held the owner's lock")
		}
	})
	t.Run("a merge racing a batch's tag resolve leaves no mapping on the merged tag", func(t *testing.T) {
		f := setup(t)
		for round := 0; round < rounds; round++ {
			src := mkTag(t, f, fmt.Sprintf("b%d-src", round))
			dst := mkTag(t, f, fmt.Sprintf("b%d-dst", round))
			var ps []domain.ID
			for i := 0; i < 8; i++ {
				ps = append(ps, mkProblem(t, f))
			}
			fns := []func() error{func() error {
				_, err := f.svc.Merge(ctx, f.owner.ID, tags.MergeInput{Sources: []string{src.Slug}, Target: dst.Slug})
				return err
			}}
			for _, p := range ps {
				fns = append(fns, func() error { return resolveAndTag(f, p, src.Slug) })
			}
			check(t, round, race(fns...))
			got, err := f.s.Tags().ProblemIDs(ctx, f.owner.ID, src.ID, domain.Nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("round %d: %d problems still map to the merged tag", round, len(got))
			}
		}
	})

	t.Run("opposing merges cannot merge both tags away", func(t *testing.T) {
		f := setup(t)
		for round := 0; round < rounds; round++ {
			a := mkTag(t, f, fmt.Sprintf("c%d-a", round))
			b := mkTag(t, f, fmt.Sprintf("c%d-b", round))
			merge := func(from, into tags.Tag) func() error {
				return func() error {
					_, err := f.svc.Merge(ctx, f.owner.ID, tags.MergeInput{Sources: []string{from.ID.String()}, Target: into.ID.String()})
					return err
				}
			}
			check(t, round, race(merge(a, b), merge(b, a)))
			active := 0
			for _, tg := range all(t, f) {
				if (tg.ID == a.ID || tg.ID == b.ID) && tg.Status == tags.StatusActive {
					active++
				}
			}
			if active != 1 {
				t.Fatalf("round %d: %d of the two tags are active after merging each into the other, want 1", round, active)
			}
		}
	})

	t.Run("revert cannot overwrite a later operation applied beside it", func(t *testing.T) {
		f := setup(t)
		for round := 0; round < rounds; round++ {
			tg := mkTag(t, f, fmt.Sprintf("v%d-a", round))
			first := "v" + fmt.Sprint(round) + "-first"
			second := "v" + fmt.Sprint(round) + "-second"
			rn := func(to string) json.RawMessage {
				b, _ := json.Marshal(tags.RenameInput{Tag: tg.ID.String(), Slug: &to})
				return b
			}
			d, err := f.svc.Propose(ctx, f.owner.ID, actor(f), tags.ProposeInput{Kind: tags.OpRename, Payload: rn(first), Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			var revErr, applyErr error
			race(
				func() error { _, revErr = f.svc.RevertOperation(ctx, f.owner.ID, actor(f), d.Operation.ID); return nil },
				func() error {
					_, applyErr = f.svc.Propose(ctx, f.owner.ID, actor(f), tags.ProposeInput{Kind: tags.OpRename, Payload: rn(second), Apply: true})
					return nil
				},
			)
			check(t, round, []error{revErr, applyErr})
			cur, err := f.svc.Get(ctx, f.owner.ID, tg.ID.String())
			if err != nil {
				t.Fatal(err)
			}
			// Either order is fine, but the later operation's result must win,
			// and a revert that raced it must have reported the conflict.
			if applyErr == nil && cur.Slug != second {
				t.Fatalf("round %d: second rename applied (err=%v) but tag is %q, want %q (revert err=%v)",
					round, applyErr, cur.Slug, second, revErr)
			}
		}
	})
}
