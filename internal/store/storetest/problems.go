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

func runProblems(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	// setup returns a store with two users.
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
	actor := func(u auth.User) domain.Actor {
		return domain.Actor{Kind: domain.ActorAgent, UserID: u.ID, ClientID: "claude"}
	}
	mk := func(owner auth.User, status problems.Status) (problems.Problem, problems.Version) {
		id := ids.New()
		p := problems.Problem{ID: id, OwnerID: owner.ID, Status: status, CurrentVersion: 1,
			CreatedBy: actor(owner), CreatedAt: t0, UpdatedAt: t0}
		v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: "What is 2+2?",
			Payload: json.RawMessage(`{"reference_answer":"4"}`), Explanation: "Arithmetic.", Source: "book",
			ChangeKind: problems.ChangeMajor, CreatedBy: actor(owner), CreatedAt: t0}
		return p, v
	}
	create := func(t *testing.T, s store.Store, owner auth.User, status problems.Status) problems.Problem {
		t.Helper()
		p, v := mk(owner, status)
		if err := s.Problems().Create(ctx, owner.ID, p, v); err != nil {
			t.Fatal(err)
		}
		return p
	}
	extra := func(u auth.User, prompt string, kind problems.ChangeKind, at time.Time) problems.Version {
		return problems.Version{Type: problems.TypeOpen, Prompt: prompt, Payload: json.RawMessage(`{}`),
			ChangeKind: kind, CreatedBy: actor(u), CreatedAt: at}
	}
	notFound := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, problems.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	}

	t.Run("create and get round trip", func(t *testing.T) {
		s, a, _ := setup(t)
		p, v := mk(a, problems.StatusDraft)
		if err := s.Problems().Create(ctx, a.ID, p, v); err != nil {
			t.Fatal(err)
		}
		got, err := s.Problems().Get(ctx, a.ID, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != p.ID || got.OwnerID != a.ID || got.Status != problems.StatusDraft ||
			got.CurrentVersion != 1 || got.CreatedBy != p.CreatedBy || !got.CreatedAt.Equal(t0) ||
			!got.UpdatedAt.Equal(t0) || got.DeletedAt != nil {
			t.Fatalf("got %+v", got)
		}
		gv, err := s.Problems().GetVersion(ctx, a.ID, p.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if gv.ProblemID != p.ID || gv.Version != 1 || gv.Type != v.Type || gv.Prompt != v.Prompt ||
			string(gv.Payload) != string(v.Payload) || gv.Explanation != v.Explanation ||
			gv.Source != v.Source || gv.ChangeKind != problems.ChangeMajor || gv.CreatedBy != v.CreatedBy ||
			!gv.CreatedAt.Equal(t0) {
			t.Fatalf("version = %+v", gv)
		}
	})

	t.Run("system actor without user round trips", func(t *testing.T) {
		s, a, _ := setup(t)
		p, v := mk(a, problems.StatusLive)
		p.CreatedBy = domain.Actor{Kind: domain.ActorSystem}
		v.CreatedBy = p.CreatedBy
		if err := s.Problems().Create(ctx, a.ID, p, v); err != nil {
			t.Fatal(err)
		}
		got, err := s.Problems().Get(ctx, a.ID, p.ID)
		if err != nil || got.CreatedBy != p.CreatedBy {
			t.Fatalf("got %+v err %v", got, err)
		}
	})

	t.Run("create rejects bad input", func(t *testing.T) {
		s, a, b := setup(t)
		p, v := mk(a, problems.StatusDraft)
		if err := s.Problems().Create(ctx, b.ID, p, v); err == nil {
			t.Fatal("create under another owner id succeeded")
		}
		p.Status = "bogus"
		if err := s.Problems().Create(ctx, a.ID, p, v); err == nil {
			t.Fatal("create with invalid status succeeded")
		}
		p, v = mk(a, problems.StatusDraft)
		v.Version = 2
		if err := s.Problems().Create(ctx, a.ID, p, v); err == nil {
			t.Fatal("create with version 2 succeeded")
		}
		if got, _ := s.Problems().List(ctx, a.ID, problems.ListFilter{IncludeDeleted: true}); len(got) != 0 {
			t.Fatalf("failed creates left rows: %+v", got)
		}
	})

	t.Run("append version", func(t *testing.T) {
		s, a, _ := setup(t)
		p := create(t, s, a, problems.StatusLive)
		t1 := t0.Add(time.Hour)
		got, err := s.Problems().AppendVersion(ctx, a.ID, p.ID, extra(a, "What is 2 + 2?", problems.ChangeMinor, t1))
		if err != nil {
			t.Fatal(err)
		}
		if got.Version != 2 || got.ProblemID != p.ID {
			t.Fatalf("appended = %+v", got)
		}
		got, err = s.Problems().AppendVersion(ctx, a.ID, p.ID, extra(a, "What is 3 + 3?", problems.ChangeMajor, t1))
		if err != nil || got.Version != 3 {
			t.Fatalf("appended = %+v err %v", got, err)
		}
		cur, err := s.Problems().Get(ctx, a.ID, p.ID)
		if err != nil || cur.CurrentVersion != 3 || !cur.UpdatedAt.Equal(t1) {
			t.Fatalf("problem = %+v err %v", cur, err)
		}
		all, err := s.Problems().ListVersions(ctx, a.ID, p.ID)
		if err != nil || len(all) != 3 {
			t.Fatalf("versions = %+v err %v", all, err)
		}
		for i, v := range all {
			if v.Version != i+1 {
				t.Fatalf("versions out of order: %+v", all)
			}
		}
		// Old versions are untouched.
		v1, _ := s.Problems().GetVersion(ctx, a.ID, p.ID, 1)
		if v1.Prompt != "What is 2+2?" || v1.ChangeKind != problems.ChangeMajor {
			t.Fatalf("v1 changed: %+v", v1)
		}
		v2, _ := s.Problems().GetVersion(ctx, a.ID, p.ID, 2)
		if v2.Prompt != "What is 2 + 2?" || v2.ChangeKind != problems.ChangeMinor {
			t.Fatalf("v2 = %+v", v2)
		}
		_, err = s.Problems().GetVersion(ctx, a.ID, p.ID, 4)
		notFound(t, err)
	})

	t.Run("append version rolls back with its transaction", func(t *testing.T) {
		s, a, _ := setup(t)
		p := create(t, s, a, problems.StatusLive)
		boom := errors.New("boom")
		err := s.WithTx(ctx, func(tx store.Store) error {
			if _, err := tx.Problems().AppendVersion(ctx, a.ID, p.ID, extra(a, "x", problems.ChangeMinor, t0)); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
		got, _ := s.Problems().Get(ctx, a.ID, p.ID)
		if got.CurrentVersion != 1 {
			t.Fatalf("current_version = %d, want 1", got.CurrentVersion)
		}
	})

	t.Run("set status", func(t *testing.T) {
		s, a, _ := setup(t)
		p := create(t, s, a, problems.StatusDraft)
		t1 := t0.Add(time.Minute)
		if err := s.Problems().SetStatus(ctx, a.ID, p.ID, problems.StatusLive, t1); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Problems().Get(ctx, a.ID, p.ID)
		if got.Status != problems.StatusLive || !got.UpdatedAt.Equal(t1) || got.CurrentVersion != 1 {
			t.Fatalf("got %+v", got)
		}
		notFound(t, s.Problems().SetStatus(ctx, a.ID, ids.New(), problems.StatusLive, t1))
	})

	t.Run("soft delete", func(t *testing.T) {
		s, a, _ := setup(t)
		p := create(t, s, a, problems.StatusLive)
		keep := create(t, s, a, problems.StatusLive)
		t1 := t0.Add(time.Hour)
		if err := s.Problems().SoftDelete(ctx, a.ID, p.ID, t1); err != nil {
			t.Fatal(err)
		}
		_, err := s.Problems().Get(ctx, a.ID, p.ID)
		notFound(t, err)
		_, err = s.Problems().GetVersion(ctx, a.ID, p.ID, 1)
		notFound(t, err)
		_, err = s.Problems().ListVersions(ctx, a.ID, p.ID)
		notFound(t, err)
		_, err = s.Problems().AppendVersion(ctx, a.ID, p.ID, extra(a, "x", problems.ChangeMinor, t1))
		notFound(t, err)
		notFound(t, s.Problems().SetStatus(ctx, a.ID, p.ID, problems.StatusArchived, t1))
		notFound(t, s.Problems().SoftDelete(ctx, a.ID, p.ID, t1)) // already deleted

		live, err := s.Problems().List(ctx, a.ID, problems.ListFilter{})
		if err != nil || len(live) != 1 || live[0].ID != keep.ID {
			t.Fatalf("list = %+v err %v", live, err)
		}
		all, err := s.Problems().List(ctx, a.ID, problems.ListFilter{IncludeDeleted: true})
		if err != nil || len(all) != 2 {
			t.Fatalf("list incl. deleted = %+v err %v", all, err)
		}
		var found bool
		for _, g := range all {
			if g.ID == p.ID {
				found = g.DeletedAt != nil && g.DeletedAt.Equal(t1)
			}
		}
		if !found {
			t.Fatalf("deleted problem missing deleted_at: %+v", all)
		}
	})

	t.Run("list filters and pages", func(t *testing.T) {
		s, a, _ := setup(t)
		d1 := create(t, s, a, problems.StatusDraft)
		l1 := create(t, s, a, problems.StatusLive)
		l2 := create(t, s, a, problems.StatusLive)
		all, err := s.Problems().List(ctx, a.ID, problems.ListFilter{})
		if err != nil || len(all) != 3 || all[0].ID != d1.ID || all[1].ID != l1.ID || all[2].ID != l2.ID {
			t.Fatalf("all = %+v err %v", all, err)
		}
		live, _ := s.Problems().List(ctx, a.ID, problems.ListFilter{Status: problems.StatusLive})
		if len(live) != 2 {
			t.Fatalf("live = %+v", live)
		}
		page, _ := s.Problems().List(ctx, a.ID, problems.ListFilter{Limit: 2})
		if len(page) != 2 {
			t.Fatalf("page = %+v", page)
		}
		next, _ := s.Problems().List(ctx, a.ID, problems.ListFilter{After: page[1].ID, Limit: 2})
		if len(next) != 1 || next[0].ID != l2.ID {
			t.Fatalf("next = %+v", next)
		}
	})

	t.Run("list current filters by type, creator kind and prompt substring", func(t *testing.T) {
		s, a, b := setup(t)
		// mkWith creates a live problem whose first version has the given type and prompt.
		mkWith := func(owner auth.User, typ, prompt string, kind domain.ActorKind) problems.Problem {
			t.Helper()
			p, v := mk(owner, problems.StatusLive)
			p.CreatedBy.Kind, v.CreatedBy.Kind = kind, kind
			v.Type, v.Prompt = typ, prompt
			if err := s.Problems().Create(ctx, owner.ID, p, v); err != nil {
				t.Fatal(err)
			}
			return p
		}
		p1 := mkWith(a, problems.TypeMCQ, "Capital of Japan? 100% sure", domain.ActorHuman)
		p2 := mkWith(a, problems.TypeCloze, "The {{w}} of japan_city", domain.ActorAgent)
		p3 := mkWith(a, problems.TypeOpen, `Back\slash`, domain.ActorAgent)
		mkWith(b, problems.TypeMCQ, "Capital of Japan? (other owner)", domain.ActorAgent)

		// A later version changes the current type and prompt; filters follow it.
		pv := extra(a, "Capital of France?", problems.ChangeMinor, t0.Add(time.Hour))
		pv.Type = problems.TypeOpen
		if _, err := s.Problems().AppendVersion(ctx, a.ID, p1.ID, pv); err != nil {
			t.Fatal(err)
		}

		cases := []struct {
			name string
			f    problems.ListFilter
			want []problems.Problem
		}{
			{"type follows current version", problems.ListFilter{Type: problems.TypeOpen}, []problems.Problem{p1, p3}},
			{"type mcq: old version no longer matches", problems.ListFilter{Type: problems.TypeMCQ}, nil},
			{"creator kind", problems.ListFilter{CreatedByKind: domain.ActorAgent}, []problems.Problem{p2, p3}},
			{"query is case-insensitive", problems.ListFilter{Query: "JAPAN"}, []problems.Problem{p2}},
			{"query matches the current prompt only", problems.ListFilter{Query: "france"}, []problems.Problem{p1}},
			{"query percent is literal", problems.ListFilter{Query: "100%"}, nil},
			{"query underscore is literal", problems.ListFilter{Query: "japan_c"}, []problems.Problem{p2}},
			{"query underscore does not match any char", problems.ListFilter{Query: "japan-c"}, nil},
			{"query backslash is literal", problems.ListFilter{Query: `k\s`}, []problems.Problem{p3}},
			{"filters combine", problems.ListFilter{Type: problems.TypeOpen, CreatedByKind: domain.ActorAgent}, []problems.Problem{p3}},
			{"filters page", problems.ListFilter{Type: problems.TypeOpen, After: p1.ID}, []problems.Problem{p3}},
		}
		for _, c := range cases {
			cur, err := s.Problems().ListCurrent(ctx, a.ID, c.f)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			var got []domain.ID
			for _, x := range cur {
				got = append(got, x.Problem.ID)
				if x.Version.Version != x.Problem.CurrentVersion || x.Version.ProblemID != x.Problem.ID {
					t.Errorf("%s: version %d of %s paired with problem at v%d", c.name, x.Version.Version, x.Version.ProblemID, x.Problem.CurrentVersion)
				}
			}
			var want []domain.ID
			for _, p := range c.want {
				want = append(want, p.ID)
			}
			if len(got) != len(want) || (len(got) > 0 && !slices.Equal(got, want)) {
				t.Errorf("%s: got %v, want %v", c.name, got, want)
			}
			plain, err := s.Problems().List(ctx, a.ID, c.f)
			if err != nil || len(plain) != len(got) {
				t.Errorf("%s: List = %d rows (err %v), ListCurrent = %d", c.name, len(plain), err, len(got))
			}
		}
	})

	t.Run("list current filters by tag", func(t *testing.T) {
		s, a, b := setup(t)
		mkTag := func(owner auth.User, slug string) tags.Tag {
			t.Helper()
			tg := tags.Tag{ID: ids.New(), OwnerID: owner.ID, Slug: slug, Name: slug, Status: tags.StatusActive,
				CreatedAt: t0, UpdatedAt: t0}
			if err := s.Tags().Create(ctx, owner.ID, tg); err != nil {
				t.Fatal(err)
			}
			return tg
		}
		tagA, tagA2, tagB := mkTag(a, "x"), mkTag(a, "y"), mkTag(b, "x")
		p1 := create(t, s, a, problems.StatusLive)
		p2 := create(t, s, a, problems.StatusLive)
		p3 := create(t, s, a, problems.StatusLive)
		pb := create(t, s, b, problems.StatusLive)
		for _, c := range []struct {
			owner auth.User
			p     problems.Problem
			tags  []domain.ID
		}{{a, p1, []domain.ID{tagA.ID}}, {a, p2, []domain.ID{tagA.ID, tagA2.ID}}, {a, p3, []domain.ID{tagA2.ID}}, {b, pb, []domain.ID{tagB.ID}}} {
			if err := s.Tags().SetProblemTags(ctx, c.owner.ID, c.p.ID, c.tags); err != nil {
				t.Fatal(err)
			}
		}
		list := func(owner auth.User, f problems.ListFilter) []domain.ID {
			t.Helper()
			cur, err := s.Problems().ListCurrent(ctx, owner.ID, f)
			if err != nil {
				t.Fatal(err)
			}
			out := []domain.ID{}
			for _, c := range cur {
				out = append(out, c.Problem.ID)
			}
			return out
		}
		eq := func(name string, got []domain.ID, want ...problems.Problem) {
			t.Helper()
			w := []domain.ID{}
			for _, p := range want {
				w = append(w, p.ID)
			}
			if !slices.Equal(got, w) {
				t.Errorf("%s: got %v, want %v", name, got, w)
			}
		}
		eq("tag x", list(a, problems.ListFilter{TagID: tagA.ID}), p1, p2)
		eq("tag y", list(a, problems.ListFilter{TagID: tagA2.ID}), p2, p3)
		eq("tag paged", list(a, problems.ListFilter{TagID: tagA2.ID, After: p2.ID}), p3)
		eq("tag and status", list(a, problems.ListFilter{TagID: tagA.ID, Status: problems.StatusDraft}))
		// A tag ID of another owner matches nothing, even for that owner's problem.
		eq("foreign tag", list(a, problems.ListFilter{TagID: tagB.ID}))
		eq("owner b", list(b, problems.ListFilter{TagID: tagB.ID}), pb)
		// Soft-deleted problems stay hidden.
		if err := s.Problems().SoftDelete(ctx, a.ID, p1.ID, t0); err != nil {
			t.Fatal(err)
		}
		eq("deleted hidden", list(a, problems.ListFilter{TagID: tagA.ID}), p2)
	})

	t.Run("owner filtering", func(t *testing.T) {
		s, a, b := setup(t)
		pa := create(t, s, a, problems.StatusLive)
		pb := create(t, s, b, problems.StatusLive)

		// b cannot see or touch a's problem.
		_, err := s.Problems().Get(ctx, b.ID, pa.ID)
		notFound(t, err)
		_, err = s.Problems().GetVersion(ctx, b.ID, pa.ID, 1)
		notFound(t, err)
		_, err = s.Problems().ListVersions(ctx, b.ID, pa.ID)
		notFound(t, err)
		_, err = s.Problems().AppendVersion(ctx, b.ID, pa.ID, extra(b, "x", problems.ChangeMinor, t0))
		notFound(t, err)
		notFound(t, s.Problems().SetStatus(ctx, b.ID, pa.ID, problems.StatusArchived, t0))
		notFound(t, s.Problems().SoftDelete(ctx, b.ID, pa.ID, t0))

		// a's problem is intact.
		got, err := s.Problems().Get(ctx, a.ID, pa.ID)
		if err != nil || got.Status != problems.StatusLive || got.CurrentVersion != 1 || got.DeletedAt != nil {
			t.Fatalf("a's problem changed: %+v err %v", got, err)
		}

		// Lists are per owner, including with IncludeDeleted.
		for _, c := range []struct {
			u    auth.User
			want domain.ID
		}{{a, pa.ID}, {b, pb.ID}} {
			l, err := s.Problems().List(ctx, c.u.ID, problems.ListFilter{IncludeDeleted: true})
			if err != nil || len(l) != 1 || l[0].ID != c.want {
				t.Fatalf("list for %s = %+v err %v", c.u.DisplayName, l, err)
			}
		}
	})
}
