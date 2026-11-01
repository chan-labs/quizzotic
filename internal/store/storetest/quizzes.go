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
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/store"
)

func runQuizzes(t *testing.T, newStore NewStore) {
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
	mk := func(t *testing.T, s store.Store, owner auth.User, title string) quizzes.Quiz {
		t.Helper()
		q := quizzes.Quiz{ID: ids.New(), OwnerID: owner.ID, Title: title, Description: "d", Status: quizzes.StatusDraft,
			CreatedBy: domain.Actor{Kind: domain.ActorAgent, UserID: owner.ID, ClientID: "agent-1"},
			CreatedAt: t0, UpdatedAt: t0}
		if err := s.Quizzes().Create(ctx, owner.ID, q); err != nil {
			t.Fatal(err)
		}
		q.Items = []domain.ID{}
		return q
	}
	mkProblem := func(t *testing.T, s store.Store, owner auth.User) domain.ID {
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
		return id
	}
	// setItems runs SetItems in a transaction, as the service does.
	setItems := func(s store.Store, owner auth.User, qid domain.ID, pids []domain.ID, at time.Time) error {
		return s.WithTx(ctx, func(tx store.Store) error {
			return tx.Quizzes().SetItems(ctx, owner.ID, qid, pids, at)
		})
	}
	is := func(t *testing.T, err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}

	t.Run("create and get round trip", func(t *testing.T) {
		s, a, _ := setup(t)
		q := mk(t, s, a, "Basics")
		got, err := s.Quizzes().Get(ctx, a.ID, q.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != q.ID || got.OwnerID != a.ID || got.Title != "Basics" || got.Description != "d" ||
			got.Status != quizzes.StatusDraft || got.CreatedBy != q.CreatedBy ||
			!got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0) || len(got.Items) != 0 {
			t.Fatalf("got %+v, want %+v", got, q)
		}
		_, err = s.Quizzes().Get(ctx, a.ID, ids.New())
		is(t, err, quizzes.ErrNotFound)
	})

	t.Run("items keep their order", func(t *testing.T) {
		s, a, _ := setup(t)
		q := mk(t, s, a, "Ordered")
		p1, p2, p3 := mkProblem(t, s, a), mkProblem(t, s, a), mkProblem(t, s, a)
		at := t0.Add(time.Minute)

		want := []domain.ID{p3, p1, p2}
		if err := setItems(s, a, q.ID, want, at); err != nil {
			t.Fatal(err)
		}
		got, err := s.Quizzes().Get(ctx, a.ID, q.ID)
		if err != nil || !slices.Equal(got.Items, want) || !got.UpdatedAt.Equal(at) {
			t.Fatalf("got %v (updated %v), %v; want %v", got.Items, got.UpdatedAt, err, want)
		}

		// Replacing reorders and drops.
		want = []domain.ID{p2, p3}
		if err := setItems(s, a, q.ID, want, at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Quizzes().Get(ctx, a.ID, q.ID)
		if !slices.Equal(got.Items, want) {
			t.Fatalf("items = %v, want %v", got.Items, want)
		}

		// An empty list clears the quiz.
		if err := setItems(s, a, q.ID, nil, at.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Quizzes().Get(ctx, a.ID, q.ID)
		if len(got.Items) != 0 {
			t.Fatalf("items = %v, want none", got.Items)
		}
	})

	t.Run("items reject other owners, missing, deleted and duplicate problems", func(t *testing.T) {
		s, a, b := setup(t)
		q := mk(t, s, a, "Guarded")
		mine, theirs := mkProblem(t, s, a), mkProblem(t, s, b)
		gone := mkProblem(t, s, a)
		if err := s.Problems().SoftDelete(ctx, a.ID, gone, t0); err != nil {
			t.Fatal(err)
		}
		if err := setItems(s, a, q.ID, []domain.ID{mine}, t0); err != nil {
			t.Fatal(err)
		}
		for name, pids := range map[string][]domain.ID{
			"other owner": {mine, theirs},
			"missing":     {mine, ids.New()},
			"deleted":     {gone},
			"duplicate":   {mine, mine},
		} {
			err := setItems(s, a, q.ID, pids, t0.Add(time.Hour))
			if !errors.Is(err, quizzes.ErrItemNotFound) {
				t.Errorf("%s: err = %v, want ErrItemNotFound", name, err)
			}
			// The failed transaction left the old items alone.
			got, _ := s.Quizzes().Get(ctx, a.ID, q.ID)
			if !slices.Equal(got.Items, []domain.ID{mine}) || !got.UpdatedAt.Equal(t0) {
				t.Errorf("%s: quiz changed: %v updated %v", name, got.Items, got.UpdatedAt)
			}
		}
	})

	t.Run("a problem can sit in several quizzes", func(t *testing.T) {
		s, a, _ := setup(t)
		q1, q2 := mk(t, s, a, "One"), mk(t, s, a, "Two")
		p := mkProblem(t, s, a)
		for _, q := range []quizzes.Quiz{q1, q2} {
			if err := setItems(s, a, q.ID, []domain.ID{p}, t0); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("update stores title, description and status", func(t *testing.T) {
		s, a, b := setup(t)
		q := mk(t, s, a, "Old")
		q.Title, q.Description, q.Status, q.UpdatedAt = "New", "nd", quizzes.StatusLive, t0.Add(time.Minute)
		if err := s.Quizzes().Update(ctx, a.ID, q); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Quizzes().Get(ctx, a.ID, q.ID)
		if got.Title != "New" || got.Description != "nd" || got.Status != quizzes.StatusLive || !got.UpdatedAt.Equal(q.UpdatedAt) {
			t.Fatalf("got %+v", got)
		}
		is(t, s.Quizzes().Update(ctx, b.ID, q), quizzes.ErrNotFound)
		q.ID = ids.New()
		is(t, s.Quizzes().Update(ctx, a.ID, q), quizzes.ErrNotFound)
	})

	t.Run("owner scoping", func(t *testing.T) {
		s, a, b := setup(t)
		q := mk(t, s, a, "Mine")
		p := mkProblem(t, s, a)
		if err := setItems(s, a, q.ID, []domain.ID{p}, t0); err != nil {
			t.Fatal(err)
		}
		_, err := s.Quizzes().Get(ctx, b.ID, q.ID)
		is(t, err, quizzes.ErrNotFound)
		is(t, setItems(s, b, q.ID, nil, t0), quizzes.ErrNotFound)
		is(t, s.Quizzes().SoftDelete(ctx, b.ID, q.ID, t0), quizzes.ErrNotFound)
		bl, err := s.Quizzes().List(ctx, b.ID, quizzes.ListFilter{})
		if err != nil || len(bl) != 0 {
			t.Fatalf("b list = %v, %v", bl, err)
		}
		if got, err := s.Quizzes().Get(ctx, a.ID, q.ID); err != nil || !slices.Equal(got.Items, []domain.ID{p}) {
			t.Fatalf("a's quiz changed: %+v %v", got, err)
		}
		other := mk(t, s, b, "Theirs")
		if err := s.Quizzes().Create(ctx, b.ID, quizzes.Quiz{ID: ids.New(), OwnerID: a.ID, Title: "x", Status: quizzes.StatusDraft,
			CreatedBy: other.CreatedBy, CreatedAt: t0, UpdatedAt: t0}); err == nil {
			t.Error("create with a mismatched owner succeeded")
		}
	})

	t.Run("soft delete hides the quiz", func(t *testing.T) {
		s, a, _ := setup(t)
		q := mk(t, s, a, "Doomed")
		if err := s.Quizzes().SoftDelete(ctx, a.ID, q.ID, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		_, err := s.Quizzes().Get(ctx, a.ID, q.ID)
		is(t, err, quizzes.ErrNotFound)
		is(t, s.Quizzes().SoftDelete(ctx, a.ID, q.ID, t0), quizzes.ErrNotFound)
		is(t, s.Quizzes().Update(ctx, a.ID, q), quizzes.ErrNotFound)
		is(t, setItems(s, a, q.ID, nil, t0), quizzes.ErrNotFound)
		l, err := s.Quizzes().List(ctx, a.ID, quizzes.ListFilter{})
		if err != nil || len(l) != 0 {
			t.Fatalf("list = %v, %v", l, err)
		}
	})

	t.Run("list filters, pages and carries items", func(t *testing.T) {
		s, a, _ := setup(t)
		q1, q2, q3 := mk(t, s, a, "1"), mk(t, s, a, "2"), mk(t, s, a, "3")
		q2.Status = quizzes.StatusLive
		if err := s.Quizzes().Update(ctx, a.ID, q2); err != nil {
			t.Fatal(err)
		}
		p1, p2 := mkProblem(t, s, a), mkProblem(t, s, a)
		if err := setItems(s, a, q3.ID, []domain.ID{p2, p1}, t0); err != nil {
			t.Fatal(err)
		}

		all, err := s.Quizzes().List(ctx, a.ID, quizzes.ListFilter{})
		if err != nil || len(all) != 3 || all[0].ID != q1.ID || all[1].ID != q2.ID || all[2].ID != q3.ID {
			t.Fatalf("list = %v, %v", all, err)
		}
		if !slices.Equal(all[2].Items, []domain.ID{p2, p1}) || len(all[0].Items) != 0 {
			t.Errorf("items = %v / %v", all[2].Items, all[0].Items)
		}
		live, _ := s.Quizzes().List(ctx, a.ID, quizzes.ListFilter{Status: quizzes.StatusLive})
		if len(live) != 1 || live[0].ID != q2.ID {
			t.Errorf("live = %v", live)
		}
		page, _ := s.Quizzes().List(ctx, a.ID, quizzes.ListFilter{After: q1.ID, Limit: 1})
		if len(page) != 1 || page[0].ID != q2.ID {
			t.Errorf("page = %v", page)
		}
	})
}
