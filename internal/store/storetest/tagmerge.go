// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func runTagMerge(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})
	now := t0.Add(time.Hour)

	setup := func(t *testing.T) (store.Store, auth.User, auth.User) {
		t.Helper()
		s, _ := newStore(t)
		a, b := newUser(ids, "a"), newUser(ids, "b")
		for _, u := range []auth.User{a, b} {
			if err := s.Users().Create(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		return s, a, b
	}
	mk := func(t *testing.T, s store.Store, owner auth.User, slug string) tags.Tag {
		t.Helper()
		tg := tags.Tag{ID: ids.New(), OwnerID: owner.ID, Slug: slug, Name: "Name " + slug,
			Status: tags.StatusActive, CreatedAt: t0, UpdatedAt: t0}
		if err := s.Tags().Create(ctx, owner.ID, tg); err != nil {
			t.Fatal(err)
		}
		return tg
	}
	mkProblem := func(t *testing.T, s store.Store, owner auth.User, tagIDs ...domain.ID) domain.ID {
		t.Helper()
		act := domain.Actor{Kind: domain.ActorHuman, UserID: owner.ID}
		id := ids.New()
		p := problems.Problem{ID: id, OwnerID: owner.ID, Status: problems.StatusLive, CurrentVersion: 1,
			CreatedBy: act, CreatedAt: t0, UpdatedAt: t0}
		v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: "q",
			Payload: json.RawMessage(`{"reference_answer":"a"}`), ChangeKind: problems.ChangeMajor, CreatedBy: act, CreatedAt: t0}
		if err := s.Problems().Create(ctx, owner.ID, p, v); err != nil {
			t.Fatal(err)
		}
		if err := s.Tags().SetProblemTags(ctx, owner.ID, id, tagIDs); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tagIDsOf := func(t *testing.T, s store.Store, owner auth.User, p domain.ID) []string {
		t.Helper()
		ts, err := s.Tags().ProblemTags(ctx, owner.ID, p)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tg := range ts {
			out = append(out, tg.Slug)
		}
		return out
	}

	t.Run("merge repoints mappings without duplicates and moves slugs to aliases", func(t *testing.T) {
		s, a, _ := setup(t)
		src1, src2, dst, other := mk(t, s, a, "golang"), mk(t, s, a, "go-lang"), mk(t, s, a, "go"), mk(t, s, a, "rust")
		// Give src1 an alias of its own via a rename.
		renamed := src1
		renamed.Slug, renamed.UpdatedAt = "golang-old", t0
		if err := s.Tags().Update(ctx, a.ID, renamed); err != nil {
			t.Fatal(err)
		}
		p1 := mkProblem(t, s, a, src1.ID)           // only a source
		p2 := mkProblem(t, s, a, src1.ID, dst.ID)   // source and target: overlap
		p3 := mkProblem(t, s, a, src1.ID, src2.ID)  // two sources: overlap
		p4 := mkProblem(t, s, a, src2.ID, other.ID) // source plus unrelated
		p5 := mkProblem(t, s, a, other.ID)          // untouched
		if err := s.Tags().Merge(ctx, a.ID, []domain.ID{src1.ID, src2.ID}, dst.ID, now); err != nil {
			t.Fatal(err)
		}
		for p, want := range map[domain.ID][]string{
			p1: {"go"}, p2: {"go"}, p3: {"go"}, p4: {"go", "rust"}, p5: {"rust"},
		} {
			if got := tagIDsOf(t, s, a, p); !slices.Equal(got, want) {
				t.Fatalf("tags of %v = %v, want %v", p, got, want)
			}
		}
		got, _ := s.Tags().ProblemIDs(ctx, a.ID, dst.ID, domain.Nil, 0)
		if len(got) != 4 {
			t.Fatalf("target problems = %v", got)
		}
		for _, src := range []tags.Tag{src1, src2} {
			m, err := s.Tags().Get(ctx, a.ID, src.ID)
			if err != nil || m.Status != tags.StatusMerged || m.MergedInto != dst.ID || !m.UpdatedAt.Equal(now) {
				t.Fatalf("source = %+v, %v", m, err)
			}
			if ids, _ := s.Tags().ProblemIDs(ctx, a.ID, src.ID, domain.Nil, 0); len(ids) != 0 {
				t.Fatalf("source keeps mappings: %v", ids)
			}
		}
		d, err := s.Tags().Get(ctx, a.ID, dst.ID)
		want := []string{"go-lang", "golang", "golang-old"}
		if err != nil || d.Status != tags.StatusActive || !slices.Equal(d.Aliases, want) || !d.UpdatedAt.Equal(now) {
			t.Fatalf("target = %+v, %v", d, err)
		}
		for _, slug := range want {
			if at, err := s.Tags().GetByAlias(ctx, a.ID, slug); err != nil || at.ID != dst.ID {
				t.Fatalf("alias %q -> %+v, %v", slug, at, err)
			}
		}
		// A merged tag no longer owns its slug as an active tag.
		if _, err := s.Tags().GetBySlug(ctx, a.ID, "golang"); !errors.Is(err, tags.ErrNotFound) {
			t.Fatalf("active lookup of merged slug: %v", err)
		}
		// The slug stays reserved by the alias.
		dup := tags.Tag{ID: ids.New(), OwnerID: a.ID, Slug: "golang", Name: "x", Status: tags.StatusActive, CreatedAt: t0, UpdatedAt: t0}
		if err := s.Tags().Create(ctx, a.ID, dup); !errors.Is(err, tags.ErrSlugTaken) {
			t.Fatalf("create over merged slug: %v", err)
		}
	})

	t.Run("merge refuses another owner's tag and changes nothing", func(t *testing.T) {
		s, a, b := setup(t)
		src, dst, foreign := mk(t, s, a, "x"), mk(t, s, a, "y"), mk(t, s, b, "z")
		p := mkProblem(t, s, a, src.ID)
		err := s.WithTx(ctx, func(tx store.Store) error {
			return tx.Tags().Merge(ctx, a.ID, []domain.ID{src.ID, foreign.ID}, dst.ID, now)
		})
		if !errors.Is(err, tags.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
		if err := s.Tags().Merge(ctx, a.ID, []domain.ID{src.ID}, foreign.ID, now); !errors.Is(err, tags.ErrNotFound) {
			t.Fatalf("foreign target: %v", err)
		}
		if got := tagIDsOf(t, s, a, p); !slices.Equal(got, []string{"x"}) {
			t.Fatalf("mappings changed: %v", got)
		}
		if g, _ := s.Tags().Get(ctx, a.ID, src.ID); g.Status != tags.StatusActive {
			t.Fatalf("source = %+v", g)
		}
	})
}
