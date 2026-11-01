// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/store"
)

func runSeedKeys(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	t.Run("round trip, upsert, and scoping", func(t *testing.T) {
		s, _ := newStore(t)
		a, b := newUser(ids, "a"), newUser(ids, "b")
		for _, u := range []auth.User{a, b} {
			if err := s.Users().Create(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		repo := s.SeedKeys()
		if _, err := repo.Get(ctx, a.ID, seed.KindProblem, "k"); !errors.Is(err, seed.ErrNotFound) {
			t.Fatalf("missing key err = %v", err)
		}
		e1 := seed.Entry{EntityID: ids.New(), Digest: "d1"}
		if err := repo.Put(ctx, a.ID, seed.KindProblem, "k", e1, t0); err != nil {
			t.Fatal(err)
		}
		if got, err := repo.Get(ctx, a.ID, seed.KindProblem, "k"); err != nil || got != e1 {
			t.Fatalf("got %+v, %v", got, err)
		}
		// Put replaces.
		e2 := seed.Entry{EntityID: e1.EntityID, Digest: "d2"}
		if err := repo.Put(ctx, a.ID, seed.KindProblem, "k", e2, t0); err != nil {
			t.Fatal(err)
		}
		if got, _ := repo.Get(ctx, a.ID, seed.KindProblem, "k"); got != e2 {
			t.Errorf("after replace = %+v", got)
		}
		// Keys are per owner and per kind.
		if _, err := repo.Get(ctx, b.ID, seed.KindProblem, "k"); !errors.Is(err, seed.ErrNotFound) {
			t.Errorf("another owner sees the key: %v", err)
		}
		if _, err := repo.Get(ctx, a.ID, seed.KindTag, "k"); !errors.Is(err, seed.ErrNotFound) {
			t.Errorf("another kind sees the key: %v", err)
		}
	})

	t.Run("a rolled back write leaves no key", func(t *testing.T) {
		s, _ := newStore(t)
		u := newUser(ids, "a")
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("boom")
		err := s.WithTx(ctx, func(tx store.Store) error {
			if err := tx.SeedKeys().Put(ctx, u.ID, seed.KindQuiz, "k", seed.Entry{EntityID: ids.New(), Digest: "d"}, t0); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
		if _, err := s.SeedKeys().Get(ctx, u.ID, seed.KindQuiz, "k"); !errors.Is(err, seed.ErrNotFound) {
			t.Errorf("key survived rollback: %v", err)
		}
	})
}

func runSoleUser(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})
	s, _ := newStore(t)

	if _, err := s.Users().Sole(ctx); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("no users: err = %v", err)
	}
	a := newUser(ids, "a")
	if err := s.Users().Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Users().Sole(ctx); err != nil || got.ID != a.ID || got.DisplayName != "a" {
		t.Fatalf("one user: %+v, %v", got, err)
	}
	if err := s.Users().Create(ctx, newUser(ids, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Users().Sole(ctx); !errors.Is(err, auth.ErrAmbiguous) {
		t.Fatalf("two users: err = %v", err)
	}
}
