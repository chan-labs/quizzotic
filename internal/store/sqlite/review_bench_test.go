// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
)

// countingExecer counts the statements a repo issues.
type countingExecer struct {
	q platform.SQLExecer
	n atomic.Int64
}

func (c *countingExecer) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	c.n.Add(1)
	return c.q.ExecContext(ctx, q, args...)
}

func (c *countingExecer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.n.Add(1)
	return c.q.QueryContext(ctx, q, args...)
}

func (c *countingExecer) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	c.n.Add(1)
	return c.q.QueryRowContext(ctx, q, args...)
}

// counted returns a view of s whose statements are counted.
func counted(s *Store) (*Store, *countingExecer) {
	c := &countingExecer{q: s.db}
	return &Store{db: s.db, q: platform.NewTracedExecer(c, "sqlite")}, c
}

// benchNow is the time the review benchmarks build at.
var benchNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// seedReview fills a fresh store with one user owning n live problems (every
// third with a major and a minor edit), a card for every other problem, and
// an answer an hour before benchNow on each of the first answered problems
// with a card, and returns the store and the user.
func seedReview(tb testing.TB, n, answered int) (*Store, domain.ID) {
	tb.Helper()
	ctx := tb.Context()
	s, err := Open(ctx, filepath.Join(tb.TempDir(), "bench.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(ctx); err != nil {
		tb.Fatal(err)
	}
	ids := domain.NewIDGenerator(domain.SystemClock{})
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	u := auth.User{ID: ids.New(), DisplayName: "bench", Email: "bench@example.com", CreatedAt: t0}
	actor := domain.Actor{Kind: domain.ActorHuman, UserID: u.ID}
	err = s.WithTx(ctx, func(tx store.Store) error {
		if err := tx.Users().Create(ctx, u); err != nil {
			return err
		}
		for i := range n {
			id := ids.New()
			p := problems.Problem{ID: id, OwnerID: u.ID, Status: problems.StatusLive, CurrentVersion: 1,
				CreatedBy: actor, CreatedAt: t0, UpdatedAt: t0}
			v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen,
				Prompt: fmt.Sprintf("Question %d?", i), Payload: json.RawMessage(`{"reference_answer":"` +
					strings.Repeat("x", 200) + `"}`), ChangeKind: problems.ChangeMajor, CreatedBy: actor, CreatedAt: t0}
			if err := tx.Problems().Create(ctx, u.ID, p, v); err != nil {
				return err
			}
			epoch := 1
			if i%3 == 0 {
				for _, k := range []problems.ChangeKind{problems.ChangeMajor, problems.ChangeMinor} {
					e := v
					e.Version, e.ChangeKind = 0, k
					if _, err := tx.Problems().AppendVersion(ctx, u.ID, id, e); err != nil {
						return err
					}
				}
				epoch = 2
			}
			if i%2 == 0 {
				c := review.Card{UserID: u.ID, ProblemID: id, Epoch: epoch, Card: fsrs.Card{
					State: fsrs.Learning, Due: t0.Add(time.Duration(i) * time.Minute), LastReview: t0}, Reps: 1}
				if err := tx.Cards().Put(ctx, u.ID, c); err != nil {
					return err
				}
				if i/2 < answered {
					right, score, rating := true, 1.0, 3
					a := attempts.Attempt{ID: ids.New(), UserID: u.ID, ProblemID: id, ProblemVersion: 1,
						CardEpoch: epoch, Response: json.RawMessage(`{"text":"x"}`), AutoCorrect: &right,
						Score: &score, Rating: &rating, AnsweredAt: benchNow.Add(-time.Hour), Client: actor}
					if err := tx.Attempts().Create(ctx, u.ID, a); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	return s, u.ID
}

func TestLiveEpochsIsOneQuery(t *testing.T) {
	for _, n := range []int{1, 50} {
		s, user := seedReview(t, n, 0)
		cs, c := counted(s)
		got, err := cs.Problems().LiveEpochs(t.Context(), user)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != n {
			t.Fatalf("n=%d: %d live epochs", n, len(got))
		}
		if q := c.n.Load(); q != 1 {
			t.Errorf("n=%d: LiveEpochs issued %d statements, want 1", n, q)
		}
	}
}

// TestBuildQueriesAreConstant checks the mastery-weighted session builder
// issues the same number of statements however many new candidates there are
// and however many cards were answered today (below the daily new limit of
// 20, so new problems are still sampled).
func TestBuildQueriesAreConstant(t *testing.T) {
	count := func(n, answered int) int64 {
		s, user := seedReview(t, n, answered)
		cs, c := counted(s)
		m := store.NewMastery(store.DefaultCards(), fixedClock{benchNow})
		if _, err := m.Builder(cs).Build(t.Context(), user, domain.Nil, benchNow); err != nil {
			t.Fatal(err)
		}
		return c.n.Load()
	}
	base := count(4, 1)
	for _, c := range []struct{ n, answered int }{{40, 1}, {4, 2}, {60, 15}} {
		if got := count(c.n, c.answered); got != base {
			t.Errorf("Build with %d problems, %d answered today: %d statements, want %d (as with 4 and 1)",
				c.n, c.answered, got, base)
		}
	}
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// BenchmarkReview2000 measures the review reads at 2000 live problems, 1000
// cards (all due), 1000 new candidates and 100 cards answered today.
func BenchmarkReview2000(b *testing.B) {
	s, user := seedReview(b, 2000, 100)
	ctx := b.Context()
	builder := store.DefaultCards().Builder(s)
	now := benchNow
	b.Run("AppendDue", func(b *testing.B) {
		for b.Loop() {
			got, err := s.Problems().LiveEpochs(ctx, user)
			if err != nil {
				b.Fatal(err)
			}
			live := make([]review.ProblemEpoch, len(got))
			for i, le := range got {
				live[i] = review.ProblemEpoch{ID: le.ID, Live: true, Epoch: le.Epoch}
			}
			if _, err := builder.AppendDue(ctx, user, live, nil, now); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Today", func(b *testing.B) {
		for b.Loop() {
			if _, err := builder.Today(ctx, user, now); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Build", func(b *testing.B) {
		for b.Loop() {
			if _, err := builder.Build(ctx, user, domain.Nil, now); err != nil {
				b.Fatal(err)
			}
		}
	})
	mastery := store.NewMastery(store.DefaultCards(), fixedClock{now})
	b.Run("BuildMastery", func(b *testing.B) {
		for b.Loop() {
			if _, err := mastery.Builder(s).Build(ctx, user, domain.Nil, now); err != nil {
				b.Fatal(err)
			}
		}
	})
}
