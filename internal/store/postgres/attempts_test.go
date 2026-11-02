// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// Mirrors the SQLite triggers: attempts are never deleted, and the only update
// is the one-time rating fill.
func TestAttemptsAppendOnly(t *testing.T) {
	s := newSchemaStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	seedAttempt(t, db, "NULL")
	wantAppendOnly(t, db, `UPDATE attempts SET latency_ms = 9`)
	wantAppendOnly(t, db, `DELETE FROM attempts`)
	if _, err := db.ExecContext(ctx, `UPDATE attempts SET rating = 3, score = 0.5`); err != nil {
		t.Fatalf("rating fill rejected: %v", err)
	}
	wantAppendOnly(t, db, `UPDATE attempts SET rating = 4`)
}

// An already-scored attempt may take a rating but its score must not change,
// and a rating cannot be filled in while altering the score.
func TestAttemptsScoredScoreImmutable(t *testing.T) {
	s := newSchemaStore(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	seedAttempt(t, db, "1")
	wantAppendOnly(t, db, `UPDATE attempts SET score = 0`)
	wantAppendOnly(t, db, `UPDATE attempts SET rating = 3, score = 0`)
	if _, err := db.ExecContext(ctx, `UPDATE attempts SET rating = 3`); err != nil {
		t.Fatalf("rating fill on scored attempt rejected: %v", err)
	}
}

func seedAttempt(t *testing.T, db *sql.DB, score string) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO users (id, created_at) VALUES ('u', now())`,
		`INSERT INTO problems (id, owner_id, status, current_version, created_by_kind, created_at, updated_at)
			VALUES ('p', 'u', 'live', 1, 'system', now(), now())`,
		`INSERT INTO attempts (id, user_id, problem_id, problem_version, card_epoch, response, score, latency_ms, answered_at, client_kind)
			VALUES ('a', 'u', 'p', 1, 1, '{"text":"x"}', ` + score + `, 5, now(), 'human')`,
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
}

// wantAppendOnly requires the statement to fail with the trigger's own
// message, so an unrelated error cannot make a test pass.
func wantAppendOnly(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), q)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("%s: want append-only trigger error, got %v", q, err)
	}
}
