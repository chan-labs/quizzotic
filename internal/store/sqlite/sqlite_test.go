// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/store"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.DB().Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return s
}

func count(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPragmas(t *testing.T) {
	s := open(t)
	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	var bt, fk int
	if err := s.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&bt); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || bt != 5000 || fk != 1 {
		t.Fatalf("mode=%s busy_timeout=%d foreign_keys=%d", mode, bt, fk)
	}
}

func TestWithTxCommitAndRollback(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	err := s.WithTx(ctx, func(tx store.Store) error {
		_, err := tx.(*Store).q.ExecContext(ctx, `INSERT INTO t VALUES (1)`)
		return err
	})
	if err != nil || count(t, s) != 1 {
		t.Fatalf("commit: %v count=%d", err, count(t, s))
	}
	boom := errors.New("boom")
	err = s.WithTx(ctx, func(tx store.Store) error {
		if _, err := tx.(*Store).q.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
			return err
		}
		// nested WithTx joins the same transaction
		return tx.WithTx(ctx, func(inner store.Store) error { return boom })
	})
	if !errors.Is(err, boom) || count(t, s) != 1 {
		t.Fatalf("rollback: %v count=%d", err, count(t, s))
	}
}

func TestWithTxRollsBackOnPanic(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	func() {
		defer func() { _ = recover() }()
		_ = s.WithTx(ctx, func(tx store.Store) error {
			_, _ = tx.(*Store).q.ExecContext(ctx, `INSERT INTO t VALUES (1)`)
			panic("x")
		})
	}()
	if count(t, s) != 0 {
		t.Fatal("panic did not roll back")
	}
}

func TestMillisRoundTrip(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	if got := FromMillis(Millis(now)); !got.Equal(now) {
		t.Fatalf("got %v", got)
	}
}

func TestProblemVersionsAreImmutable(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	for _, q := range []string{
		`INSERT INTO users (id, created_at) VALUES ('u', 0)`,
		`INSERT INTO problems (id, owner_id, status, current_version, created_by_kind, created_at, updated_at)
			VALUES ('p', 'u', 'live', 1, 'system', 0, 0)`,
		`INSERT INTO problem_versions (problem_id, version, type, prompt, payload, change_kind, created_by_kind, created_at)
			VALUES ('p', 1, 'open', 'q', '{}', 'major', 'system', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE problem_versions SET prompt = 'changed'`); err == nil {
		t.Fatal("update of a version succeeded")
	}
	if _, err := db.Exec(`DELETE FROM problem_versions`); err == nil {
		t.Fatal("delete of a version succeeded")
	}
}
