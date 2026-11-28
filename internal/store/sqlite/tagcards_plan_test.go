// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// seedTags gives the user nTags active tags and puts two of them on every
// live problem, returning the tag IDs.
func seedTags(tb testing.TB, s *Store, user domain.ID, nTags int) []domain.ID {
	tb.Helper()
	ctx := tb.Context()
	ids := domain.NewIDGenerator(domain.SystemClock{})
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tagIDs := make([]domain.ID, nTags)
	err := s.WithTx(ctx, func(tx store.Store) error {
		for i := range tagIDs {
			tagIDs[i] = ids.New()
			t := tags.Tag{ID: tagIDs[i], OwnerID: user, Slug: fmt.Sprintf("tag-%d", i), Name: fmt.Sprintf("Tag %d", i),
				Status: tags.StatusActive, CreatedAt: t0, UpdatedAt: t0}
			if err := tx.Tags().Create(ctx, user, t); err != nil {
				return err
			}
		}
		live, err := tx.Problems().List(ctx, user, problems.ListFilter{Status: problems.StatusLive})
		if err != nil {
			return err
		}
		for i, p := range live {
			on := []domain.ID{tagIDs[i%nTags], tagIDs[(i+1)%nTags]}
			if err := tx.Tags().SetProblemTags(ctx, user, p.ID, on); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	return tagIDs
}

// tagCardsPlan returns the EXPLAIN QUERY PLAN rows of TagCards.
func tagCardsPlan(t *testing.T, s *Store, user domain.ID, tagIDs []domain.ID) string {
	t.Helper()
	q, args := tagCardsQuery(user, tagIDs)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func TestTagCardsUsesTagIndex(t *testing.T) {
	check := func(t *testing.T, s *Store, user domain.ID, tagIDs []domain.ID) {
		t.Helper()
		for _, ids := range [][]domain.ID{tagIDs, tagIDs[:1], nil} {
			plan := tagCardsPlan(t, s, user, ids)
			if !strings.Contains(plan, "problem_tags_tag") {
				t.Errorf("TagCards(%d tags) plan does not use problem_tags_tag:\n%s", len(ids), plan)
			}
			// Problems are looked up by id per problem_tags row, never
			// searched by owner alone (which visits every owner problem).
			if strings.Contains(plan, "deleted_at=?)") || strings.Contains(plan, "SCAN ") {
				t.Errorf("TagCards(%d tags) plan scans the owner's problems:\n%s", len(ids), plan)
			}
		}
	}
	t.Run("empty", func(t *testing.T) {
		s, user := seedReview(t, 0, 0)
		check(t, s, user, seedTags(t, s, user, 3))
	})
	t.Run("populated", func(t *testing.T) {
		s, user := seedReview(t, 2000, 0)
		tagIDs := seedTags(t, s, user, 20)
		check(t, s, user, tagIDs)
		// Planner statistics must not change the choice either.
		if err := s.Optimize(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`ANALYZE`); err != nil {
			t.Fatal(err)
		}
		check(t, s, user, tagIDs)
	})
}

// BenchmarkAnswerMastery measures the mastery work an answer does
// (Projector.UpdateProblem: the answered problem's tags, recomputed from
// TagCards) at 2000 live problems and 20 tags, two per problem.
func BenchmarkAnswerMastery(b *testing.B) {
	s, user := seedReview(b, 2000, 0)
	seedTags(b, s, user, 20)
	ctx := b.Context()
	live, err := s.Problems().List(ctx, user, problems.ListFilter{Status: problems.StatusLive, Limit: 1})
	if err != nil {
		b.Fatal(err)
	}
	m := store.NewMastery(store.DefaultCards(), fixedClock{benchNow})
	for b.Loop() {
		err := s.WithTx(ctx, func(tx store.Store) error {
			return m.Projector(tx).UpdateProblem(ctx, user, live[0].ID)
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
