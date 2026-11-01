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

func runTags(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

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
		tg := tags.Tag{ID: ids.New(), OwnerID: owner.ID, Slug: slug, Name: "Name " + slug, Description: "d",
			Status: tags.StatusActive, CreatedAt: t0, UpdatedAt: t0}
		if err := s.Tags().Create(ctx, owner.ID, tg); err != nil {
			t.Fatal(err)
		}
		tg.Aliases = []string{}
		return tg
	}
	mkProblem := func(t *testing.T, s store.Store, owner auth.User) problems.Problem {
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
		return p
	}
	is := func(t *testing.T, err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}

	t.Run("create and get by id, slug", func(t *testing.T) {
		s, a, _ := setup(t)
		tg := mk(t, s, a, "go")
		byID, err := s.Tags().Get(ctx, a.ID, tg.ID)
		if err != nil || byID.Slug != "go" || byID.Name != "Name go" || byID.Status != tags.StatusActive ||
			!byID.MergedInto.IsNil() || len(byID.Aliases) != 0 || !byID.CreatedAt.Equal(t0) {
			t.Fatalf("get = %+v, %v", byID, err)
		}
		bySlug, err := s.Tags().GetBySlug(ctx, a.ID, "go")
		if err != nil || bySlug.ID != tg.ID {
			t.Fatalf("by slug = %+v, %v", bySlug, err)
		}
		_, err = s.Tags().GetBySlug(ctx, a.ID, "rust")
		is(t, err, tags.ErrNotFound)
		_, err = s.Tags().GetByAlias(ctx, a.ID, "go")
		is(t, err, tags.ErrNotFound)
	})

	t.Run("slug unique per owner", func(t *testing.T) {
		s, a, b := setup(t)
		mk(t, s, a, "go")
		dup := tags.Tag{ID: ids.New(), OwnerID: a.ID, Slug: "go", Name: "x", Status: tags.StatusActive, CreatedAt: t0, UpdatedAt: t0}
		is(t, s.Tags().Create(ctx, a.ID, dup), tags.ErrSlugTaken)
		// Another owner may use the same slug, and the two tags stay apart.
		other := mk(t, s, b, "go")
		got, err := s.Tags().GetBySlug(ctx, b.ID, "go")
		if err != nil || got.ID != other.ID {
			t.Fatalf("owner b slug = %+v, %v", got, err)
		}
	})

	t.Run("rename leaves the old slug as an alias", func(t *testing.T) {
		s, a, _ := setup(t)
		tg := mk(t, s, a, "golang")
		tg.Slug, tg.UpdatedAt = "go", t0.Add(time.Minute)
		if err := s.Tags().Update(ctx, a.ID, tg); err != nil {
			t.Fatal(err)
		}
		got, err := s.Tags().GetByAlias(ctx, a.ID, "golang")
		if err != nil || got.ID != tg.ID || got.Slug != "go" || !slices.Equal(got.Aliases, []string{"golang"}) ||
			!got.UpdatedAt.Equal(t0.Add(time.Minute)) {
			t.Fatalf("by alias = %+v, %v", got, err)
		}
		// The old slug is no longer a slug, and it cannot be claimed by a new tag.
		_, err = s.Tags().GetBySlug(ctx, a.ID, "golang")
		is(t, err, tags.ErrNotFound)
		dup := tags.Tag{ID: ids.New(), OwnerID: a.ID, Slug: "golang", Name: "x", Status: tags.StatusActive, CreatedAt: t0, UpdatedAt: t0}
		is(t, s.Tags().Create(ctx, a.ID, dup), tags.ErrSlugTaken)
		// The new slug itself is not an alias.
		_, err = s.Tags().GetByAlias(ctx, a.ID, "go")
		is(t, err, tags.ErrNotFound)
	})

	t.Run("renaming back takes the alias over again", func(t *testing.T) {
		s, a, _ := setup(t)
		tg := mk(t, s, a, "golang")
		tg.Slug = "go"
		if err := s.Tags().Update(ctx, a.ID, tg); err != nil {
			t.Fatal(err)
		}
		tg.Slug = "golang"
		if err := s.Tags().Update(ctx, a.ID, tg); err != nil {
			t.Fatal(err)
		}
		got, err := s.Tags().Get(ctx, a.ID, tg.ID)
		if err != nil || got.Slug != "golang" || !slices.Equal(got.Aliases, []string{"go"}) {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("rename onto another tag's slug or alias conflicts", func(t *testing.T) {
		s, a, _ := setup(t)
		x, y := mk(t, s, a, "x"), mk(t, s, a, "y")
		y.Slug = "x"
		is(t, s.Tags().Update(ctx, a.ID, y), tags.ErrSlugTaken)
		x.Slug = "z" // x becomes alias holder of "x"
		if err := s.Tags().Update(ctx, a.ID, x); err != nil {
			t.Fatal(err)
		}
		y.Slug = "x"
		is(t, s.Tags().Update(ctx, a.ID, y), tags.ErrSlugTaken)
		got, _ := s.Tags().Get(ctx, a.ID, y.ID)
		if got.Slug != "y" || len(got.Aliases) != 0 {
			t.Fatalf("failed rename changed the tag: %+v", got)
		}
	})

	t.Run("other owner's tags are not found", func(t *testing.T) {
		s, a, b := setup(t)
		tg := mk(t, s, a, "go")
		tg.Slug = "golang"
		if err := s.Tags().Update(ctx, a.ID, tg); err != nil {
			t.Fatal(err)
		}
		_, err := s.Tags().Get(ctx, b.ID, tg.ID)
		is(t, err, tags.ErrNotFound)
		_, err = s.Tags().GetBySlug(ctx, b.ID, "golang")
		is(t, err, tags.ErrNotFound)
		_, err = s.Tags().GetByAlias(ctx, b.ID, "go")
		is(t, err, tags.ErrNotFound)
		tg.Name = "hijacked"
		is(t, s.Tags().Update(ctx, b.ID, tg), tags.ErrNotFound)
		got, _ := s.Tags().Get(ctx, a.ID, tg.ID)
		if got.Name == "hijacked" {
			t.Fatal("update by another owner changed the tag")
		}
		l, err := s.Tags().List(ctx, b.ID, tags.ListFilter{Status: tags.StatusActive})
		if err != nil || len(l) != 0 {
			t.Fatalf("list as b = %v, %v", l, err)
		}
	})

	t.Run("list filters by status and pages by id", func(t *testing.T) {
		s, a, _ := setup(t)
		var all []tags.Tag
		for _, slug := range []string{"a", "b", "c"} {
			all = append(all, mk(t, s, a, slug))
		}
		got, err := s.Tags().List(ctx, a.ID, tags.ListFilter{Status: tags.StatusActive, Limit: 2})
		if err != nil || len(got) != 2 || got[0].ID != all[0].ID || got[1].ID != all[1].ID {
			t.Fatalf("page 1 = %+v, %v", got, err)
		}
		got, err = s.Tags().List(ctx, a.ID, tags.ListFilter{Status: tags.StatusActive, After: all[1].ID, Limit: 2})
		if err != nil || len(got) != 1 || got[0].ID != all[2].ID {
			t.Fatalf("page 2 = %+v, %v", got, err)
		}
		got, err = s.Tags().List(ctx, a.ID, tags.ListFilter{Status: tags.StatusRetired})
		if err != nil || len(got) != 0 {
			t.Fatalf("retired = %+v, %v", got, err)
		}
	})

	t.Run("problem tags replace, list and respect soft delete", func(t *testing.T) {
		s, a, _ := setup(t)
		t1, t2 := mk(t, s, a, "beta"), mk(t, s, a, "alpha")
		p1, p2 := mkProblem(t, s, a), mkProblem(t, s, a)

		if err := s.Tags().SetProblemTags(ctx, a.ID, p1.ID, []domain.ID{t1.ID, t2.ID}); err != nil {
			t.Fatal(err)
		}
		if err := s.Tags().SetProblemTags(ctx, a.ID, p2.ID, []domain.ID{t1.ID}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Tags().ProblemTags(ctx, a.ID, p1.ID)
		if err != nil || len(got) != 2 || got[0].Slug != "alpha" || got[1].Slug != "beta" {
			t.Fatalf("problem tags = %+v, %v", got, err)
		}
		pids, err := s.Tags().ProblemIDs(ctx, a.ID, t1.ID, domain.Nil, 0)
		if err != nil || !slices.Equal(pids, []domain.ID{p1.ID, p2.ID}) {
			t.Fatalf("problem ids = %v, %v", pids, err)
		}
		pids, _ = s.Tags().ProblemIDs(ctx, a.ID, t1.ID, p1.ID, 5)
		if !slices.Equal(pids, []domain.ID{p2.ID}) {
			t.Fatalf("after cursor = %v", pids)
		}

		// Replace, with a duplicate id in the input.
		if err := s.Tags().SetProblemTags(ctx, a.ID, p1.ID, []domain.ID{t2.ID, t2.ID}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Tags().ProblemTags(ctx, a.ID, p1.ID)
		if len(got) != 1 || got[0].ID != t2.ID {
			t.Fatalf("after replace = %+v", got)
		}
		if err := s.Tags().SetProblemTags(ctx, a.ID, p1.ID, nil); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Tags().ProblemTags(ctx, a.ID, p1.ID)
		if len(got) != 0 {
			t.Fatalf("after clear = %+v", got)
		}

		if err := s.Problems().SoftDelete(ctx, a.ID, p2.ID, t0); err != nil {
			t.Fatal(err)
		}
		pids, _ = s.Tags().ProblemIDs(ctx, a.ID, t1.ID, domain.Nil, 0)
		if len(pids) != 0 {
			t.Fatalf("soft-deleted problem listed: %v", pids)
		}
	})

	t.Run("problem tags refuse another owner's tag or problem", func(t *testing.T) {
		s, a, b := setup(t)
		ta, tb := mk(t, s, a, "go"), mk(t, s, b, "go")
		pa := mkProblem(t, s, a)
		if err := s.Tags().SetProblemTags(ctx, a.ID, pa.ID, []domain.ID{ta.ID}); err != nil {
			t.Fatal(err)
		}
		// b's tag on a's problem, and a's problem under b: nothing changes.
		is(t, s.Tags().SetProblemTags(ctx, a.ID, pa.ID, []domain.ID{tb.ID}), tags.ErrNotFound)
		is(t, s.Tags().SetProblemTags(ctx, b.ID, pa.ID, []domain.ID{tb.ID}), tags.ErrNotFound)
		got, _ := s.Tags().ProblemTags(ctx, a.ID, pa.ID)
		if len(got) != 1 || got[0].ID != ta.ID {
			t.Fatalf("tags changed: %+v", got)
		}
		if got, _ := s.Tags().ProblemTags(ctx, b.ID, pa.ID); len(got) != 0 {
			t.Fatalf("b sees a's problem tags: %+v", got)
		}
		if ids, _ := s.Tags().ProblemIDs(ctx, b.ID, ta.ID, domain.Nil, 0); len(ids) != 0 {
			t.Fatalf("b lists a's problems: %v", ids)
		}
	})
}
