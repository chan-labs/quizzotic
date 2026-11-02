// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/chan-labs/quizzotic/internal/store/migrate"
	"github.com/chan-labs/quizzotic/migrations"
)

func TestRebind(t *testing.T) {
	cases := map[string]string{
		`SELECT 1`:                             `SELECT 1`,
		`a = ? AND b = ?`:                      `a = $1 AND b = $2`,
		`x LIKE ? ESCAPE '\'`:                  `x LIKE $1 ESCAPE '\'`,
		`SELECT '?' , ?`:                       `SELECT '?' , $1`,
		`INSERT INTO t VALUES (?, ?, ?::text)`: `INSERT INTO t VALUES ($1, $2, $3::text)`,
	}
	for in, want := range cases {
		if got := Rebind(in); got != want {
			t.Errorf("Rebind(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every SQLite migration needs a Postgres twin with the same version and name,
// so a new feature cannot ship on one backend only. No database needed.
func TestMigrationParity(t *testing.T) {
	sq, err := migrate.Load(migrations.FS, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	pg, err := migrate.Load(migrations.FS, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(sq) != len(pg) {
		t.Fatalf("sqlite has %d migrations, postgres %d", len(sq), len(pg))
	}
	for i := range sq {
		if sq[i].Version != pg[i].Version || sq[i].Name != pg[i].Name {
			t.Errorf("migration %d: sqlite %d_%s, postgres %d_%s",
				i, sq[i].Version, sq[i].Name, pg[i].Version, pg[i].Name)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := newSchemaStore(t)
	ctx := context.Background()
	first, err := s.Migrate(ctx)
	if err != nil || len(first) == 0 {
		t.Fatalf("first migrate = %v, %v", first, err)
	}
	again, err := s.Migrate(ctx)
	if err != nil || len(again) != 0 {
		t.Fatalf("second migrate = %v, %v", again, err)
	}
}

// Two instances starting together must not both apply (and fail on) the same
// migration: the advisory lock serializes them.
func TestMigrateConcurrent(t *testing.T) {
	s := newSchemaStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	applied := make([][]int, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied[i], errs[i] = s.Migrate(ctx)
		}()
	}
	wg.Wait()
	total := 0
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("migrator %d: %v", i, errs[i])
		}
		total += len(applied[i])
	}
	all, _ := migrate.Load(migrations.FS, "postgres")
	if total != len(all) {
		t.Fatalf("migrations applied %d times in total, want %d", total, len(all))
	}
}

func TestProblemVersionsAreImmutable(t *testing.T) {
	s := newSchemaStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	for _, q := range []string{
		`INSERT INTO users (id, created_at) VALUES ('u', now())`,
		`INSERT INTO problems (id, owner_id, status, current_version, created_by_kind, created_at, updated_at)
			VALUES ('p', 'u', 'live', 1, 'system', now(), now())`,
		`INSERT INTO problem_versions (problem_id, version, type, prompt, payload, change_kind, created_by_kind, created_at)
			VALUES ('p', 1, 'open', 'q', '{}', 'major', 'system', now())`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`UPDATE problem_versions SET prompt = 'changed'`,
		`DELETE FROM problem_versions`,
	} {
		if _, err := db.ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("%s: want immutable trigger error, got %v", q, err)
		}
	}
}

func TestOpenErrorDoesNotLeakPassword(t *testing.T) {
	_, err := Open(context.Background(), "postgres://u:hunter2secret@127.0.0.1:1/db?connect_timeout=1")
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if got := err.Error(); strings.Contains(got, "hunter2secret") {
		t.Fatalf("error leaks the password: %v", got)
	}
}
