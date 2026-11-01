// SPDX-License-Identifier: Apache-2.0

package tags_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func ptr(s string) *string { return &s }

func renameSetup(t *testing.T) (*tags.Service, domain.ID, domain.ID) {
	t.Helper()
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := domain.SystemClock{}
	ids := domain.NewIDGenerator(clock)
	var owners []domain.ID
	for _, n := range []string{"a", "b"} {
		u := auth.User{ID: ids.New(), DisplayName: n, Email: n + "@example.com", CreatedAt: clock.Now()}
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		owners = append(owners, u.ID)
	}
	return tags.NewService(store.TagsTx(s), clock, ids), owners[0], owners[1]
}

func TestRenameOldSlugResolvesToRenamedTag(t *testing.T) {
	ctx := context.Background()
	svc, owner, other := renameSetup(t)
	orig, err := svc.Create(ctx, owner, tags.CreateInput{Name: "Go Basics", Slug: "go-basics"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Rename(ctx, owner, tags.RenameInput{Tag: "go-basics", Slug: ptr("golang"), Name: ptr("Golang")})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != orig.ID || got.Slug != "golang" || got.Name != "Golang" ||
		!reflect.DeepEqual(got.Aliases, []string{"go-basics"}) || got.Status != tags.StatusActive {
		t.Fatalf("renamed = %+v", got)
	}

	for _, ref := range []string{"go-basics", "GO-BASICS", "golang", orig.ID.String()} {
		r, err := svc.Get(ctx, owner, ref)
		if err != nil || r.ID != orig.ID || r.Slug != "golang" {
			t.Fatalf("Get(%q) = %+v, %v", ref, r, err)
		}
	}
	id, found, err := svc.ResolveTagFilter(ctx, owner, "go-basics")
	if err != nil || !found || id != orig.ID {
		t.Fatalf("ResolveTagFilter = %v %v %v", id, found, err)
	}
	// Another owner's lookups never see the alias.
	if _, err := svc.Get(ctx, other, "go-basics"); !errors.Is(err, tags.ErrNotFound) {
		t.Fatalf("other owner: %v", err)
	}
}

func TestRenameNameOnlyAddsNoAlias(t *testing.T) {
	ctx := context.Background()
	svc, owner, _ := renameSetup(t)
	if _, err := svc.Create(ctx, owner, tags.CreateInput{Name: "Old", Slug: "old"}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Rename(ctx, owner, tags.RenameInput{Tag: "old", Name: ptr("  New Name ")})
	if err != nil || got.Slug != "old" || got.Name != "New Name" || len(got.Aliases) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRenameChainKeepsEveryOldSlug(t *testing.T) {
	ctx := context.Background()
	svc, owner, _ := renameSetup(t)
	orig, _ := svc.Create(ctx, owner, tags.CreateInput{Name: "A", Slug: "a"})
	if _, err := svc.Rename(ctx, owner, tags.RenameInput{Tag: "a", Slug: ptr("b")}); err != nil {
		t.Fatal(err)
	}
	// Rename via the alias, then back to the original slug.
	if _, err := svc.Rename(ctx, owner, tags.RenameInput{Tag: "a", Slug: ptr("c")}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Rename(ctx, owner, tags.RenameInput{Tag: "c", Slug: ptr("a")})
	if err != nil || got.ID != orig.ID || !reflect.DeepEqual(got.Aliases, []string{"b", "c"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRenameRejections(t *testing.T) {
	ctx := context.Background()
	svc, owner, _ := renameSetup(t)
	svc.Create(ctx, owner, tags.CreateInput{Name: "A", Slug: "a"})
	svc.Create(ctx, owner, tags.CreateInput{Name: "B", Slug: "b"})
	if _, err := svc.Rename(ctx, owner, tags.RenameInput{Tag: "b", Slug: ptr("b2")}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   tags.RenameInput
		want error
	}{
		{"slug of another tag", tags.RenameInput{Tag: "a", Slug: ptr("b2")}, tags.ErrSlugTaken},
		{"alias of another tag", tags.RenameInput{Tag: "a", Slug: ptr("b")}, tags.ErrSlugTaken},
		{"unknown tag", tags.RenameInput{Tag: "nope", Slug: ptr("x")}, tags.ErrNotFound},
	}
	for _, c := range cases {
		if _, err := svc.Rename(ctx, owner, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}

	invalid := []tags.RenameInput{
		{Tag: "a"},
		{Tag: "a", Slug: ptr("Bad Slug!")},
		{Tag: "a", Name: ptr("  ")},
		{Slug: ptr("x")},
	}
	for i, in := range invalid {
		var p *platform.Problem
		if _, err := svc.Rename(ctx, owner, in); !errors.As(err, &p) || p.Status != 422 {
			t.Errorf("invalid[%d]: err = %v, want 422", i, err)
		}
	}

	// The failed attempts changed nothing.
	a, err := svc.Get(ctx, owner, "a")
	if err != nil || a.Slug != "a" || a.Name != "A" || len(a.Aliases) != 0 {
		t.Fatalf("a = %+v, %v", a, err)
	}
}
