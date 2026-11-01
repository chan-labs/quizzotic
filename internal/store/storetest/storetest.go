// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/chan-labs/quizzotic/internal/store"
)

// Scratch gives the suite observable state without depending on a real repo.
// It operates on whichever store.Store it is handed, so it works both on the
// root store and on the transactional store passed to WithTx.
type Scratch interface {
	// Insert adds one row with value n.
	Insert(ctx context.Context, s store.Store, n int) error
	// Count returns the number of rows.
	Count(ctx context.Context, s store.Store) (int, error)
	// CountUsers and CountIdentities return the total rows in those tables,
	// across all owners, so tests can detect orphans and duplicates.
	CountUsers(ctx context.Context, s store.Store) (int, error)
	CountIdentities(ctx context.Context, s store.Store) (int, error)
}

// NewStore returns a fresh, migrated, empty store plus its Scratch helper.
// It must register any cleanup with t.
type NewStore func(t *testing.T) (store.Store, Scratch)

// Run executes the conformance suite against the backend built by newStore.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	t.Run("Tx", func(t *testing.T) { runTx(t, newStore) })
	t.Run("Users", func(t *testing.T) { runUsers(t, newStore) })
	t.Run("Identities", func(t *testing.T) { runIdentities(t, newStore) })
	t.Run("Provision", func(t *testing.T) { runProvision(t, newStore) })
	t.Run("Settings", func(t *testing.T) { runSettings(t, newStore) })
	t.Run("WebSessions", func(t *testing.T) { runWebSessions(t, newStore) })
	t.Run("LoginStates", func(t *testing.T) { runLoginStates(t, newStore) })
	t.Run("Idempotency", func(t *testing.T) { runIdempotency(t, newStore) })
}

func runTx(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	boom := errors.New("boom")

	count := func(t *testing.T, s store.Store, sc Scratch) int {
		t.Helper()
		n, err := sc.Count(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("commit", func(t *testing.T) {
		s, sc := newStore(t)
		err := s.WithTx(ctx, func(tx store.Store) error { return sc.Insert(ctx, tx, 1) })
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, s, sc); n != 1 {
			t.Fatalf("count after commit = %d, want 1", n)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		s, sc := newStore(t)
		err := s.WithTx(ctx, func(tx store.Store) error {
			if err := sc.Insert(ctx, tx, 1); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if n := count(t, s, sc); n != 0 {
			t.Fatalf("count after rollback = %d, want 0", n)
		}
	})

	t.Run("sees own writes", func(t *testing.T) {
		s, sc := newStore(t)
		err := s.WithTx(ctx, func(tx store.Store) error {
			if err := sc.Insert(ctx, tx, 1); err != nil {
				return err
			}
			if n := count(t, tx, sc); n != 1 {
				t.Errorf("count inside tx = %d, want 1", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("nested error rolls back everything", func(t *testing.T) {
		s, sc := newStore(t)
		err := s.WithTx(ctx, func(tx store.Store) error {
			if err := sc.Insert(ctx, tx, 1); err != nil {
				return err
			}
			return tx.WithTx(ctx, func(inner store.Store) error {
				if err := sc.Insert(ctx, inner, 2); err != nil {
					return err
				}
				return boom
			})
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if n := count(t, s, sc); n != 0 {
			t.Fatalf("count after nested rollback = %d, want 0", n)
		}
	})

	t.Run("nested success joins outer commit", func(t *testing.T) {
		s, sc := newStore(t)
		err := s.WithTx(ctx, func(tx store.Store) error {
			return tx.WithTx(ctx, func(inner store.Store) error { return sc.Insert(ctx, inner, 1) })
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, s, sc); n != 1 {
			t.Fatalf("count = %d, want 1", n)
		}
	})

	t.Run("panic rolls back", func(t *testing.T) {
		s, sc := newStore(t)
		func() {
			defer func() { _ = recover() }()
			_ = s.WithTx(ctx, func(tx store.Store) error {
				_ = sc.Insert(ctx, tx, 1)
				panic("x")
			})
		}()
		if n := count(t, s, sc); n != 0 {
			t.Fatalf("count after panic = %d, want 0", n)
		}
	})
}
