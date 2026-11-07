// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/store/migrate"
	"github.com/chan-labs/quizzotic/migrations"
)

const clozeMatchVersion = 20261102150000

// Stored cloze payloads with the old case_sensitive flag become match:
// "relaxed" (both old values), other payloads are left alone, blank order is
// kept and the immutability trigger is enabled again afterwards.
func TestClozeMatchMigrationRewritesLegacyPayloads(t *testing.T) {
	s := newSchemaStore(t)
	ctx := context.Background()
	db := s.DB()

	all, err := migrate.Load(migrations.FS, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	var target migrate.Migration
	for _, m := range all {
		if m.Version == clozeMatchVersion {
			target = m
			continue
		}
		if m.Version < clozeMatchVersion {
			if _, err := db.ExecContext(ctx, m.SQL); err != nil {
				t.Fatalf("migration %d: %v", m.Version, err)
			}
		}
	}
	if target.SQL == "" {
		t.Fatal("cloze match migration missing")
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, created_at) VALUES ('u', now())`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id, typ, payload string
		want             string // "" = unchanged
	}{
		{"p1", "cloze", `{"blanks":[{"id":"a","accepted":["x"],"case_sensitive":true,"max_edits":1},{"id":"b","accepted":["y","z"],"case_sensitive":false,"max_edits":0}]}`,
			`{"blanks":[{"id":"a","accepted":["x"],"max_edits":1,"match":"relaxed"},{"id":"b","accepted":["y","z"],"max_edits":0,"match":"relaxed"}]}`},
		{"p2", "cloze", `{"blanks":[{"id":"a","accepted":["x"],"max_edits":0}]}`, ""},
		{"p3", "cloze", `{"blanks":[{"id":"a","accepted":["x"],"match":"case-insensitive","max_edits":0}]}`, ""},
		{"p4", "cloze", `{"blanks":[{"id":"a","accepted":["x"],"case_sensitive":true,"match":"case-insensitive","max_edits":0}]}`,
			`{"blanks":[{"id":"a","accepted":["x"],"max_edits":0,"match":"case-insensitive"}]}`},
		{"p5", "mcq", `{"choices":[{"id":"a","text":"case_sensitive"}],"correct":["a"],"multiple":false,"shuffle":false}`, ""},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, `INSERT INTO problems (id, owner_id, status, current_version, created_by_kind, created_at, updated_at)
			VALUES ($1, 'u', 'live', 2, 'human', now(), now())`, r.id); err != nil {
			t.Fatal(err)
		}
		for v := 1; v <= 2; v++ { // every version, not only the current one
			if _, err := db.ExecContext(ctx, `INSERT INTO problem_versions (problem_id, version, type, prompt, payload, change_kind, created_by_kind, created_at)
				VALUES ($1, $2, $3, 'q', $4::jsonb, 'major', 'human', now())`, r.id, v, r.typ, r.payload); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := db.ExecContext(ctx, target.SQL); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	for _, r := range rows {
		for v := 1; v <= 2; v++ {
			var got string
			if err := db.QueryRowContext(ctx, `SELECT payload::text FROM problem_versions WHERE problem_id = $1 AND version = $2`, r.id, v).Scan(&got); err != nil {
				t.Fatal(err)
			}
			want := r.want
			if want == "" {
				want = r.payload
			}
			if !sameJSON(t, got, want) {
				t.Errorf("%s v%d payload = %s, want %s", r.id, v, got, want)
			}
			if r.typ == "cloze" && strings.Contains(got, "case_sensitive") {
				t.Errorf("%s v%d still has case_sensitive: %s", r.id, v, got)
			}
		}
	}
	var first string
	if err := db.QueryRowContext(ctx, `SELECT payload #>> '{blanks,0,id}' FROM problem_versions WHERE problem_id = 'p1' AND version = 1`).Scan(&first); err != nil || first != "a" {
		t.Errorf("blank order changed, first = %q (%v)", first, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE problem_versions SET prompt = 'x'`); err == nil {
		t.Error("problem_versions is no longer immutable after the migration")
	}
	// Idempotent: a second run changes nothing and still succeeds.
	if _, err := db.ExecContext(ctx, target.SQL); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("bad json %q: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("bad json %q: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}
