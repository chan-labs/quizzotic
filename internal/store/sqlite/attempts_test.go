// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAttemptsAreAppendOnly(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "a.db"))
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
		// a1 is rated and scored (auto-graded); a2 is an unrated open attempt.
		`INSERT INTO attempts (id, user_id, problem_id, problem_version, card_epoch, response, auto_correct, score,
			rating, latency_ms, answered_at, client_kind) VALUES ('a1', 'u', 'p', 1, 1, '{}', 1, 1, 3, 10, 0, 'human')`,
		`INSERT INTO attempts (id, user_id, problem_id, problem_version, card_epoch, response, latency_ms,
			answered_at, client_kind) VALUES ('a2', 'u', 'p', 1, 1, '{}', 10, 0, 'human')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`DELETE FROM attempts`,
		`UPDATE attempts SET response = '{"x":1}' WHERE id = 'a2'`,
		`UPDATE attempts SET latency_ms = 99 WHERE id = 'a2'`,
		`UPDATE attempts SET problem_version = 2 WHERE id = 'a2'`,
		`UPDATE attempts SET rating = 4 WHERE id = 'a1'`, // already rated
		`UPDATE attempts SET score = 0 WHERE id = 'a1'`,  // already scored
		`UPDATE attempts SET rating = 4, response = '{"x":1}' WHERE id = 'a2'`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s succeeded; attempts must be append-only", q)
		}
	}
	// The one permitted change: fill a missing rating (and the score it implies).
	if _, err := db.Exec(`UPDATE attempts SET rating = 2, score = 0.33 WHERE id = 'a2'`); err != nil {
		t.Fatalf("rating fill refused: %v", err)
	}
	if _, err := db.Exec(`UPDATE attempts SET rating = 4 WHERE id = 'a2'`); err == nil {
		t.Error("a second rating fill succeeded")
	}
}
