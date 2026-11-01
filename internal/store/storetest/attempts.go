// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
)

func runAttempts(t *testing.T, newStore NewStore) {
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
	ptr := func(n int) *int { return &n }
	mk := func(owner auth.User, problem domain.ID) attempts.Attempt {
		right, score := true, 0.5
		return attempts.Attempt{
			ID: ids.New(), UserID: owner.ID, ProblemID: problem, ProblemVersion: 1, CardEpoch: 1,
			Response: json.RawMessage(`{"selected":["a"]}`), AutoCorrect: &right, Score: &score, Rating: ptr(3),
			LatencyMS: 1234, AnsweredAt: t0, Client: domain.Actor{Kind: domain.ActorHuman, UserID: owner.ID, ClientID: "web-session"},
		}
	}

	t.Run("create and get round trip", func(t *testing.T) {
		s, a, _ := setup(t)
		pid := mkProblem(t, s, a)

		full := mk(a, pid)
		full.SessionID = ids.New()
		// An unrated, unscored open attempt: every optional field is absent.
		bare := mk(a, pid)
		bare.AutoCorrect, bare.Score, bare.Rating = nil, nil, nil
		bare.Response = json.RawMessage(`{"text":"my answer"}`)

		for _, at := range []attempts.Attempt{full, bare} {
			if err := s.Attempts().Create(ctx, a.ID, at); err != nil {
				t.Fatal(err)
			}
			got, err := s.Attempts().Get(ctx, a.ID, at.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, at) {
				t.Errorf("round trip\n got  %+v\n want %+v", got, at)
			}
		}
	})

	t.Run("attempts are appended, never replaced", func(t *testing.T) {
		s, a, _ := setup(t)
		at := mk(a, mkProblem(t, s, a))
		if err := s.Attempts().Create(ctx, a.ID, at); err != nil {
			t.Fatal(err)
		}
		if err := s.Attempts().Create(ctx, a.ID, at); err == nil {
			t.Fatal("second create with the same ID succeeded")
		}
		got, err := s.Attempts().Get(ctx, a.ID, at.ID)
		if err != nil || !reflect.DeepEqual(got, at) {
			t.Fatalf("stored attempt changed: %+v %v", got, err)
		}
	})

	t.Run("get unknown", func(t *testing.T) {
		s, a, _ := setup(t)
		if _, err := s.Attempts().Get(ctx, a.ID, ids.New()); !errors.Is(err, attempts.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("owner scoping", func(t *testing.T) {
		s, a, b := setup(t)
		pa := mkProblem(t, s, a)
		at := mk(a, pa)
		if err := s.Attempts().Create(ctx, a.ID, at); err != nil {
			t.Fatal(err)
		}
		// B cannot read A's attempt.
		if _, err := s.Attempts().Get(ctx, b.ID, at.ID); !errors.Is(err, attempts.ErrNotFound) {
			t.Fatalf("B get = %v, want ErrNotFound", err)
		}
		// B cannot answer A's problem, even naming itself as the user.
		stolen := mk(b, pa)
		if err := s.Attempts().Create(ctx, b.ID, stolen); !errors.Is(err, attempts.ErrNotFound) {
			t.Fatalf("B create on A's problem = %v, want ErrNotFound", err)
		}
		if _, err := s.Attempts().Get(ctx, b.ID, stolen.ID); !errors.Is(err, attempts.ErrNotFound) {
			t.Fatalf("attempt on A's problem was stored: %v", err)
		}
		// The owner argument must match the attempt's user.
		if err := s.Attempts().Create(ctx, b.ID, mk(a, pa)); err == nil {
			t.Fatal("create with mismatched owner succeeded")
		}
	})

	t.Run("set rating fills once", func(t *testing.T) {
		s, a, _ := setup(t)
		pid := mkProblem(t, s, a)
		open := mk(a, pid)
		open.AutoCorrect, open.Score, open.Rating = nil, nil, nil
		auto := mk(a, pid)
		auto.Rating = nil // auto-graded and scored, but no rating sent
		for _, at := range []attempts.Attempt{open, auto} {
			if err := s.Attempts().Create(ctx, a.ID, at); err != nil {
				t.Fatal(err)
			}
		}

		score := 0.67
		if err := s.Attempts().SetRating(ctx, a.ID, open.ID, 3, &score); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Attempts().Get(ctx, a.ID, open.ID)
		if got.Rating == nil || *got.Rating != 3 || got.Score == nil || *got.Score != 0.67 {
			t.Fatalf("open after fill = %+v", got)
		}
		// An already-scored attempt keeps its score.
		other := 0.1
		if err := s.Attempts().SetRating(ctx, a.ID, auto.ID, 4, &other); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Attempts().Get(ctx, a.ID, auto.ID)
		if *got.Rating != 4 || *got.Score != 0.5 {
			t.Fatalf("auto after fill = %+v", got)
		}
		// A second fill is refused and changes nothing.
		if err := s.Attempts().SetRating(ctx, a.ID, open.ID, 1, nil); !errors.Is(err, attempts.ErrAlreadyRated) {
			t.Fatalf("second fill = %v, want ErrAlreadyRated", err)
		}
		got, _ = s.Attempts().Get(ctx, a.ID, open.ID)
		if *got.Rating != 3 {
			t.Fatalf("second fill changed the rating: %+v", got)
		}
		// An attempt that was rated on creation cannot be filled.
		rated := mk(a, pid)
		if err := s.Attempts().Create(ctx, a.ID, rated); err != nil {
			t.Fatal(err)
		}
		if err := s.Attempts().SetRating(ctx, a.ID, rated.ID, 2, nil); !errors.Is(err, attempts.ErrAlreadyRated) {
			t.Fatalf("fill of a rated attempt = %v, want ErrAlreadyRated", err)
		}
	})

	t.Run("set rating is owner scoped", func(t *testing.T) {
		s, a, b := setup(t)
		at := mk(a, mkProblem(t, s, a))
		at.Rating, at.Score, at.AutoCorrect = nil, nil, nil
		if err := s.Attempts().Create(ctx, a.ID, at); err != nil {
			t.Fatal(err)
		}
		score := 0.33
		if err := s.Attempts().SetRating(ctx, b.ID, at.ID, 2, &score); !errors.Is(err, attempts.ErrNotFound) {
			t.Fatalf("B sets A's rating = %v, want ErrNotFound", err)
		}
		if err := s.Attempts().SetRating(ctx, a.ID, ids.New(), 2, nil); !errors.Is(err, attempts.ErrNotFound) {
			t.Fatalf("unknown attempt = %v, want ErrNotFound", err)
		}
		got, _ := s.Attempts().Get(ctx, a.ID, at.ID)
		if got.Rating != nil || got.Score != nil {
			t.Fatalf("A's attempt changed: %+v", got)
		}
	})

	t.Run("rolls back with the transaction", func(t *testing.T) {
		s, a, _ := setup(t)
		at := mk(a, mkProblem(t, s, a))
		boom := errors.New("boom")
		err := s.WithTx(ctx, func(tx store.Store) error {
			if err := tx.Attempts().Create(ctx, a.ID, at); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if _, err := s.Attempts().Get(ctx, a.ID, at.ID); !errors.Is(err, attempts.ErrNotFound) {
			t.Fatalf("attempt survived a rollback: %v", err)
		}
	})
}
