// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
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
	for _, q := range []string{
		`INSERT INTO users (id, created_at) VALUES ('u', now())`,
		`INSERT INTO problems (id, owner_id, status, current_version, created_by_kind, created_at, updated_at)
			VALUES ('p', 'u', 'live', 1, 'system', now(), now())`,
		`INSERT INTO attempts (id, user_id, problem_id, problem_version, card_epoch, response, latency_ms, answered_at, client_kind)
			VALUES ('a', 'u', 'p', 1, 1, '{"text":"x"}', 5, now(), 'human')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE attempts SET latency_ms = 9`); err == nil {
		t.Fatal("update of another column succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM attempts`); err == nil {
		t.Fatal("delete succeeded")
	}
	if _, err := db.ExecContext(ctx, `UPDATE attempts SET rating = 3, score = 0.5`); err != nil {
		t.Fatalf("rating fill rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attempts SET rating = 4`); err == nil {
		t.Fatal("second rating change succeeded")
	}
}
