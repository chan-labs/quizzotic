// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/store/driver"
)

const adminPack = `
version: 1
problems:
  - {key: p, type: open, prompt: "Admin seeded?", payload: {reference_answer: yes}}
quizzes:
  - {key: q, title: Admin quiz, problems: [p]}
`

func seededDB(t *testing.T, users int) (url, file string, ids []domain.ID) {
	t.Helper()
	file = filepath.Join(t.TempDir(), "seed.db")
	url = "sqlite://" + file
	if code, _, errs := run(t, "migrate", "--storage-url", url); code != 0 {
		t.Fatalf("migrate: %s", errs)
	}
	st, err := driver.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g := domain.NewIDGenerator(domain.SystemClock{})
	for range users {
		id := g.New()
		u := auth.User{ID: id, DisplayName: "u", Email: "u@example.com", CreatedAt: time.Now()}
		if err := st.Users().Create(context.Background(), u); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return url, file, ids
}

func count(t *testing.T, file, q string) int {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSeedFileDefaultsToTheOnlyUserAndIsIdempotent(t *testing.T) {
	url, file, ids := seededDB(t, 1)
	src := filepath.Join(t.TempDir(), "pack.yaml")
	writeFile(t, src, adminPack)

	for i := range 2 {
		code, out, errs := run(t, "seed", src, "--storage-url", url)
		if code != 0 {
			t.Fatalf("run %d: exit %d: %s", i+1, code, errs)
		}
		want := "created 1, updated 0, unchanged 0"
		if i == 1 {
			want = "created 0, updated 0, unchanged 1"
		}
		if !strings.Contains(out, want) || !strings.Contains(out, ids[0].String()) {
			t.Fatalf("run %d output = %q", i+1, out)
		}
	}
	if n := count(t, file, `SELECT count(*) FROM problem_versions`); n != 1 {
		t.Errorf("versions = %d, want 1", n)
	}
	if n := count(t, file, `SELECT count(*) FROM problems WHERE created_by_kind = 'system'`); n != 1 {
		t.Errorf("system problems = %d, want 1", n)
	}
}

func TestSeedBuiltinWithOwnerFlag(t *testing.T) {
	url, file, ids := seededDB(t, 2)
	// Two users: the owner must be named.
	if code, _, errs := run(t, "seed", "builtin:fsrs-basics", "--storage-url", url); code == 0 || !strings.Contains(errs, "--owner") {
		t.Fatalf("ambiguous owner: code=%d stderr=%q", code, errs)
	}
	code, _, errs := run(t, "seed", "--owner", ids[1].String(), "builtin:fsrs-basics", "--storage-url", url)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if n := count(t, file, `SELECT count(*) FROM problems WHERE owner_id = '`+ids[1].String()+`'`); n == 0 {
		t.Error("nothing seeded for the named owner")
	}
	if n := count(t, file, `SELECT count(*) FROM problems WHERE owner_id = '`+ids[0].String()+`'`); n != 0 {
		t.Errorf("%d problems seeded for the other user", n)
	}
}

func TestSeedErrors(t *testing.T) {
	url, _, _ := seededDB(t, 1)
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	writeFile(t, bad, "version: 1\nproblems:\n  - {type: open}\n")
	for name, tc := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no source":       {[]string{"--storage-url", url}, 2, "usage"},
		"two sources":     {[]string{"a.yaml", "b.yaml", "--storage-url", url}, 2, "usage"},
		"missing file":    {[]string{"/nonexistent/x.yaml", "--storage-url", url}, 1, "no such file"},
		"unknown builtin": {[]string{"builtin:nope", "--storage-url", url}, 1, "no built-in pack"},
		"invalid file":    {[]string{bad, "--storage-url", url}, 1, "key: required"},
		"bad owner":       {[]string{"builtin:fsrs-basics", "--owner", "zzz", "--storage-url", url}, 1, "not a user ID"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, errs := run(t, append([]string{"seed"}, tc.args...)...)
			if code != tc.code || !strings.Contains(errs, tc.want) {
				t.Fatalf("code=%d stderr=%q, want %d / %q", code, errs, tc.code, tc.want)
			}
		})
	}
}

func TestSeedNoUsers(t *testing.T) {
	url, _, _ := seededDB(t, 0)
	if code, _, errs := run(t, "seed", "builtin:fsrs-basics", "--storage-url", url); code == 0 || !strings.Contains(errs, "no users yet") {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
}
