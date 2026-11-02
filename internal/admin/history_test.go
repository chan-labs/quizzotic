// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/store/driver"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// withTags gives each user one active tag (slug "t") created four days ago and
// returns the tag IDs in user order.
func withTags(t *testing.T, url string, users []domain.ID) []domain.ID {
	t.Helper()
	st, err := driver.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g := domain.NewIDGenerator(domain.SystemClock{})
	born := time.Now().Add(-4 * 24 * time.Hour)
	var out []domain.ID
	for _, u := range users {
		id := g.New()
		tg := tags.Tag{ID: id, OwnerID: u, Slug: "t", Name: "t", Status: tags.StatusActive, CreatedAt: born, UpdatedAt: born}
		if err := st.Tags().Create(context.Background(), u, tg); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func snapshots(t *testing.T, file string, user domain.ID) int {
	t.Helper()
	return count(t, file, `SELECT count(*) FROM mastery_snapshots WHERE user_id = '`+user.String()+`'`)
}

func TestRebuildHistory(t *testing.T) {
	url, file, users := seededDB(t, 2)
	withTags(t, url, users)

	code, out, errs := run(t, "rebuild", "--history", "--storage-url", url)
	if code != 0 || !strings.Contains(out, "for 2 user(s)") {
		t.Fatalf("all: code=%d out=%q stderr=%q", code, out, errs)
	}
	// Created four days ago: that study day, the three after it, and today.
	n := snapshots(t, file, users[0])
	if n < 4 || n > 6 || n != snapshots(t, file, users[1]) {
		t.Fatalf("snapshots = %d and %d, want the same 4 to 6 each", n, snapshots(t, file, users[1]))
	}

	// Rewriting is idempotent, and one user can be rewritten alone.
	if code, _, errs = run(t, "rebuild", "--history", "--user", users[1].String(), "--storage-url", url); code != 0 {
		t.Fatalf("one user: %d %s", code, errs)
	}
	if got := snapshots(t, file, users[0]) + snapshots(t, file, users[1]); got != 2*n {
		t.Errorf("total snapshots = %d after a rewrite, want %d", got, 2*n)
	}
}

func TestRebuildHistoryOneTag(t *testing.T) {
	url, file, users := seededDB(t, 1)
	tagIDs := withTags(t, url, users)
	if code, _, errs := run(t, "rebuild", "--history", "--storage-url", url); code != 0 {
		t.Fatal(errs)
	}
	before := snapshots(t, file, users[0])

	for _, ref := range []string{"t", tagIDs[0].String()} {
		code, out, errs := run(t, "rebuild", "--history", "--user", users[0].String(), "--tag", ref, "--storage-url", url)
		if code != 0 || !strings.Contains(out, "of 1 tag(s)") {
			t.Fatalf("--tag %s: code=%d out=%q stderr=%q", ref, code, out, errs)
		}
		if got := snapshots(t, file, users[0]); got != before {
			t.Errorf("--tag %s: snapshots = %d, want %d", ref, got, before)
		}
	}
}

func TestRebuildHistoryErrors(t *testing.T) {
	url, _, users := seededDB(t, 1)
	withTags(t, url, users)
	unknown := strings.Replace(users[0].String(), users[0].String()[:8], "00000000", 1)
	for name, tc := range map[string]struct {
		args []string
		code int
		want string
	}{
		"tag without history": {[]string{"--user", users[0].String(), "--tag", "t"}, 2, "usage"},
		"tag without user":    {[]string{"--history", "--tag", "t"}, 2, "usage"},
		"unknown user":        {[]string{"--history", "--user", unknown}, 1, "no such user"},
		"unknown tag":         {[]string{"--history", "--user", users[0].String(), "--tag", "nope"}, 1, "no such tag"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, errs := run(t, append([]string{"rebuild", "--storage-url", url}, tc.args...)...)
			if code != tc.code || !strings.Contains(errs, tc.want) {
				t.Fatalf("code=%d stderr=%q, want %d / %q", code, errs, tc.code, tc.want)
			}
		})
	}
}
