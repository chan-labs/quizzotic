// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
)

func runRebuild(t *testing.T, newStore NewStore) {
	t.Run("list user ids", func(t *testing.T) { runListUserIDs(t, newStore) })
	t.Run("rebuild all", func(t *testing.T) { runRebuildAll(t, newStore) })
	// SQLite under the race detector is about 30x slower, so the shared suite
	// scales the log down there; RunRebuild10k keeps the full size for Postgres.
	n := attempts10k
	if raceEnabled {
		n = 1_000
	}
	t.Run(fmt.Sprintf("rebuild all, %d attempts", n), func(t *testing.T) { runRebuildLargeLog(t, newStore, n) })
}

func runListUserIDs(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})
	s, _ := newStore(t)

	if got, err := s.Users().ListIDs(ctx, domain.Nil, 10); err != nil || len(got) != 0 {
		t.Fatalf("no users: %v, %v", got, err)
	}
	var want []domain.ID
	for i := 0; i < 5; i++ {
		u := newUser(ids, fmt.Sprintf("u%d", i))
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		want = append(want, u.ID)
	}
	if got, err := s.Users().ListIDs(ctx, domain.Nil, 100); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("all: %v, %v (want %v)", got, err, want)
	}
	// Paging by the last ID seen visits everyone once, in order.
	var paged []domain.ID
	after := domain.Nil
	for {
		page, err := s.Users().ListIDs(ctx, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page...)
		if len(page) < 2 {
			break
		}
		after = page[len(page)-1]
	}
	if !reflect.DeepEqual(paged, want) {
		t.Fatalf("paged = %v, want %v", paged, want)
	}
	if got, err := s.Users().ListIDs(ctx, want[4], 2); err != nil || len(got) != 0 {
		t.Fatalf("after the last: %v, %v", got, err)
	}
	if got, err := s.Users().ListIDs(ctx, domain.Nil, 0); err != nil || len(got) != 0 {
		t.Fatalf("limit 0: %v, %v", got, err)
	}
}

// runRebuildAll: RebuildAll visits every user across pages, RebuildOne refuses
// an unknown user, and progress is logged.
func runRebuildAll(t *testing.T, newStore NewStore) {
	f := newCardFixture(t, newStore)
	ids := domain.NewIDGenerator(domain.SystemClock{})
	for i := 0; i < 3; i++ { // users with nothing to rebuild still count
		if err := f.s.Users().Create(f.ctx, newUser(ids, fmt.Sprintf("extra%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	p := f.newProblem(pkinds[0], problems.StatusLive)
	if _, err := f.answer(p.ID, pkinds[0].right, nil); err != nil {
		t.Fatal(err)
	}
	want := f.list()
	if len(want) != 1 {
		t.Fatalf("cards = %d", len(want))
	}
	if err := f.s.Cards().DeleteAll(f.ctx, f.user); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	svc := f.cards.Service(f.s, review.WithBatchSize(2), review.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	sum, err := svc.RebuildAll(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum != (review.RebuildSummary{Users: 4, Cards: 1}) {
		t.Errorf("summary = %+v", sum)
	}
	if got := f.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("cards = %+v, want %+v", got, want)
	}
	if n := strings.Count(logs.String(), "rebuild progress"); n != 2 {
		t.Errorf("progress lines = %d, want one per batch (2)\n%s", n, logs.String())
	}

	one, err := svc.RebuildOne(f.ctx, f.user)
	if err != nil || one != (review.RebuildSummary{Users: 1, Cards: 1}) {
		t.Errorf("RebuildOne = %+v, %v", one, err)
	}
	if _, err := svc.RebuildOne(f.ctx, ids.New()); err != review.ErrNotFound {
		t.Errorf("unknown user: %v", err)
	}
}

// onTx is the fixture with its problems and attempts services bound to tx, so
// several answers share one transaction.
func (f *cardFixture) onTx(tx store.Store) *cardFixture {
	g := *f
	g.s = tx
	g.probs = problems.NewService(tx.Problems(), store.ProblemsEditTx(tx, f.cards.MajorEdit), f.clock, f.ids)
	g.atts = attempts.NewService(store.AttemptsCardTx(tx, f.cards.CardUpdater), g.probs, f.clock, f.ids)
	return &g
}

// attempts10k is the size of the seeded log in the M4.8 check.
const attempts10k = 10_000

// RunRebuild10k is the M4.8 Done-when at its full size: on a database holding
// 10,000 attempts, rebuilding finishes and gives exactly the state the
// incremental updates maintained.
func RunRebuild10k(t *testing.T, newStore NewStore) { runRebuildLargeLog(t, newStore, attempts10k) }

func runRebuildLargeLog(t *testing.T, newStore NewStore, attempts int) {
	if testing.Short() {
		t.Skip("10k-attempt rebuild skipped in -short mode")
	}
	f := newCardFixture(t, newStore)
	rng := rand.New(rand.NewSource(48))

	type pv struct {
		v problems.View
		k pkind
	}
	var ps []pv
	for i := 0; i < 200; i++ {
		k := pkinds[i%len(pkinds)]
		ps = append(ps, pv{f.newProblem(k, problems.StatusLive), k})
	}
	// A couple of problems that have left review, whose attempts stay in the log.
	for i := 0; i < 2; i++ {
		p := f.newProblem(pkinds[0], problems.StatusLive)
		for j := 0; j < 5; j++ {
			if _, err := f.answer(p.ID, pkinds[0].right, nil); err != nil {
				t.Fatal(err)
			}
		}
		f.archive(p.ID)
	}

	// Every attempt goes through the real answer path, so each one updates its
	// card incrementally. They are answered in transactions of 500 only to keep
	// the run short; that does not change what each answer writes.
	seen := map[domain.ID]bool{} // problems with at least one scheduled attempt
	for answered := 0; answered < attempts; answered += 500 {
		err := f.s.WithTx(f.ctx, func(tx store.Store) error {
			g := f.onTx(tx)
			for i := 0; i < 500; i++ {
				p := ps[rng.Intn(len(ps))]
				response := p.k.right
				if rng.Intn(3) == 0 {
					response = p.k.wrong
				}
				var rating *int
				if p.k.typ == problems.TypeOpen || rng.Intn(2) == 0 {
					rating = ptrInt(2 + rng.Intn(3)) // 2-4: legal for a right or wrong answer
				}
				if _, err := g.answer(p.v.ID, response, rating); err != nil {
					return err
				}
				seen[p.v.ID] = true
				switch rng.Intn(3) {
				case 0:
					f.clock.Advance(time.Duration(1+rng.Intn(600)) * time.Second)
				case 1:
					f.clock.Advance(time.Duration(1+rng.Intn(96)) * time.Hour)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	incremental := f.list()
	if len(incremental) != len(seen) {
		t.Fatalf("incremental cards = %d, want %d (one per problem answered)", len(incremental), len(seen))
	}
	var logs bytes.Buffer
	svc := f.cards.Service(f.s, review.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	sum, err := svc.RebuildAll(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Users != 1 || sum.Cards != len(incremental) {
		t.Errorf("summary = %+v, want 1 user and %d cards", sum, len(incremental))
	}
	got := f.list()
	if len(got) != len(incremental) {
		t.Fatalf("rebuilt %d cards, incremental has %d", len(got), len(incremental))
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], incremental[i]) {
			t.Fatalf("rebuilt card %d differs\n incremental %+v\n rebuilt     %+v", i, incremental[i], got[i])
		}
	}
	if !strings.Contains(logs.String(), "rebuild progress") || !strings.Contains(logs.String(), "rebuild finished") {
		t.Errorf("no progress logged:\n%s", logs.String())
	}
}
