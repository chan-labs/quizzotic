// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/storetest"
)

// scratch backs storetest.Scratch with a throwaway table.
type scratch struct{}

func (scratch) Insert(ctx context.Context, s store.Store, n int) error {
	_, err := s.(*Store).q.ExecContext(ctx, `INSERT INTO storetest_scratch VALUES (?)`, n)
	return err
}

func (scratch) Count(ctx context.Context, s store.Store) (int, error) {
	var n int
	err := s.(*Store).q.QueryRowContext(ctx, `SELECT count(*) FROM storetest_scratch`).Scan(&n)
	return n, err
}

func (scratch) CountUsers(ctx context.Context, s store.Store) (int, error) {
	var n int
	err := s.(*Store).q.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (scratch) CountIdentities(ctx context.Context, s store.Store) (int, error) {
	var n int
	err := s.(*Store).q.QueryRowContext(ctx, `SELECT count(*) FROM identities`).Scan(&n)
	return n, err
}

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) (store.Store, storetest.Scratch) {
		s, err := Open(context.Background(), filepath.Join(t.TempDir(), "c.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if _, err := s.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`CREATE TABLE storetest_scratch (n INTEGER)`); err != nil {
			t.Fatal(err)
		}
		return s, scratch{}
	})
}
