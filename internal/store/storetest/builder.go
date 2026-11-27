// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// runCardBuilder runs the review session builder over the real stores.
func runCardBuilder(t *testing.T, newStore NewStore) {
	t.Run("builds from stored cards, attempts and problems", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		one, two, four := 1, 2, 4
		utc, zero := "UTC", 0
		setBudget := func(newPerDay *int) {
			t.Helper()
			if err := f.s.Settings().Replace(f.ctx, f.user, settings.Overrides{
				NewPerDay: newPerDay, MaxReviewsPerDay: &one, Timezone: &utc, RolloverHour: &zero}); err != nil {
				t.Fatal(err)
			}
		}
		setBudget(&two)

		k := pkinds[0]
		studied := f.newProblem(k, problems.StatusLive)
		var fresh []domain.ID
		for range 4 {
			fresh = append(fresh, f.newProblem(k, problems.StatusLive).ID)
		}
		f.newProblem(k, problems.StatusDraft)
		gone := f.newProblem(k, problems.StatusLive)
		for _, id := range []domain.ID{studied.ID, gone.ID} {
			if _, err := f.answer(id, k.right, nil); err != nil {
				t.Fatal(err)
			}
		}
		f.archive(gone.ID)
		f.clock.Advance(time.Hour) // the learning step has come due

		b := f.cards.Builder(f.s)
		session := domain.NewIDGenerator(f.clock).New()
		plan, err := b.Build(f.ctx, f.user, session, f.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		// Both first answers are new cards of today, the archived problem's included.
		if plan.Used.NewCards != 2 || plan.Used.Reviews != 0 {
			t.Fatalf("usage = %+v, want 2 new cards, 0 reviews", plan.Used)
		}
		// The new budget (2) is spent; the only item is the due card.
		if len(plan.Items) != 1 || plan.Items[0].ProblemID != studied.ID || plan.Items[0].Kind != review.ItemReview {
			t.Fatalf("items = %+v, want only the due card", plan.Items)
		}

		// A raised new budget adds new live problems only, reproducibly per session.
		setBudget(&four)
		p1, err := b.Build(f.ctx, f.user, session, f.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		p2, err := b.Build(f.ctx, f.user, session, f.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(p1.Items, p2.Items) || len(p1.Items) != 3 {
			t.Fatalf("items %+v vs %+v, want the same 3", p1.Items, p2.Items)
		}
		isFresh := map[domain.ID]bool{}
		for _, id := range fresh {
			isFresh[id] = true
		}
		for _, it := range p1.Items[1:] {
			if it.Kind != review.ItemNew || !isFresh[it.ProblemID] {
				t.Errorf("unexpected new item %+v", it)
			}
		}

		// Another user's data never leaks in.
		other := newUser(domain.NewIDGenerator(f.clock), "bob")
		if err := f.s.Users().Create(f.ctx, other); err != nil {
			t.Fatal(err)
		}
		if p, err := b.Build(f.ctx, other.ID, session, f.clock.Now()); err != nil || len(p.Items) != 0 {
			t.Errorf("other user's plan = %+v, %v; want empty", p, err)
		}
	})
}
