// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/storetest"
)

// TestURLEnv names the environment variable holding the URL of a Postgres
// server the tests may create schemas in. Unset, the Postgres tests skip
// locally; CI sets it (see .github/workflows/ci.yml) and also sets
// QUIZZOTIC_REQUIRE_POSTGRES so a missing URL fails instead of skipping.
const TestURLEnv = "QUIZZOTIC_TEST_POSTGRES_URL"

func testURL(t *testing.T) string {
	t.Helper()
	if u := os.Getenv(TestURLEnv); u != "" {
		return u
	}
	if os.Getenv("QUIZZOTIC_REQUIRE_POSTGRES") != "" {
		t.Fatalf("%s is not set but QUIZZOTIC_REQUIRE_POSTGRES is", TestURLEnv)
	}
	t.Skipf("%s not set; skipping Postgres tests", TestURLEnv)
	return ""
}

var schemaSeq atomic.Int64

// newSchemaStore returns a Store on a fresh, empty schema, dropped on cleanup.
func newSchemaStore(t *testing.T) *Store {
	t.Helper()
	url := testURL(t)
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("qz_t_%d_%d", os.Getpid(), schemaSeq.Add(1))
	if _, err := admin.DB().ExecContext(ctx, `CREATE SCHEMA `+name); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSchema(ctx, url, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.DB().ExecContext(context.Background(), `DROP SCHEMA `+name+` CASCADE`)
		admin.Close()
	})
	return s
}

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

// newConformanceStore is a fresh, migrated store in its own schema.
func newConformanceStore(t *testing.T) (store.Store, storetest.Scratch) {
	s := newSchemaStore(t)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TABLE storetest_scratch (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return s, scratch{}
}

// TestConformance runs the shared suite; every case gets its own schema.
func TestConformance(t *testing.T) {
	testURL(t)
	storetest.Run(t, newConformanceStore)
}

// TestConformanceRebuild10k is the M4.8 check at full size (the shared suite
// scales it down under the race detector, where SQLite is slow).
func TestConformanceRebuild10k(t *testing.T) {
	testURL(t)
	storetest.RunRebuild10k(t, newConformanceStore)
}
