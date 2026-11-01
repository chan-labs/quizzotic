// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/settings"
	"github.com/chan-labs/quizzotic/internal/store"
)

// runWeakTagWeighting proves that the session builder, wired to real tag
// mastery, introduces problems of weak tags more often, and that doing so
// leaves every card (and so every due date) untouched.
func runWeakTagWeighting(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	live := store.NewMastery(f.cards, f.clock) // recall predicted at the fixture's own clock
	k := pkinds[0]
	weak := f.newTag(f.user, "weak")
	strong := f.newTag(f.user, "strong")
	empty := f.newTag(f.user, "empty")

	// Studied problems: the weak tag is answered wrong and rated Again, the
	// strong one right and rated Easy.
	for range 3 {
		w := f.newProblem(k, problems.StatusLive)
		f.tag(f.user, w.ID, weak)
		if _, err := f.answer(w.ID, k.wrong, ptrInt(1)); err != nil {
			t.Fatal(err)
		}
		s := f.newProblem(k, problems.StatusLive)
		f.tag(f.user, s.ID, strong)
		if _, err := f.answer(s.ID, k.right, ptrInt(4)); err != nil {
			t.Fatal(err)
		}
	}
	f.clock.Advance(30 * 24 * time.Hour)

	// Fresh problems: five per tag, the "empty" tag having no studied card.
	kind := map[domain.ID]string{}
	names := []struct {
		name string
		tag  domain.ID
	}{{"weak", weak}, {"strong", strong}, {"empty", empty}}
	for range 5 {
		for _, n := range names {
			p := f.newProblem(k, problems.StatusLive)
			f.tag(f.user, p.ID, n.tag)
			kind[p.ID] = n.name
		}
	}

	utc, zero, one, boost := "UTC", 0, 1, 8.0 // a strong boost keeps the sample small
	if err := f.s.Settings().Replace(f.ctx, f.user, settings.Overrides{
		NewPerDay: &one, Timezone: &utc, RolloverHour: &zero, WeakTagBoost: &boost}); err != nil {
		t.Fatal(err)
	}

	reader := metrics.NewRecallReader(live.Scope(f.s), f.clock)
	recall := func(name string) float64 {
		t.Helper()
		for id, n := range kind {
			if n == name {
				r, err := reader.ProblemTagRecall(f.ctx, f.user, id)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
		}
		t.Fatalf("no problem for %s", name)
		return 0
	}
	rw, rs, re := recall("weak"), recall("strong"), recall("empty")
	if !(rw < 0.5 && rs > 0.5) || re != metrics.NoDataRecall {
		t.Fatalf("recall weak %.3f strong %.3f empty %.3f; want weak < 0.5 < strong, empty exactly 0.5", rw, rs, re)
	}

	before := f.list()
	count := func(b *review.Builder) map[string]int {
		t.Helper()
		got := map[string]int{}
		for i := range 900 {
			// Fixed session IDs make the sample, and so the test, deterministic.
			var sid domain.ID
			binary.BigEndian.PutUint64(sid[8:], uint64(i+1))
			plan, err := b.Build(f.ctx, f.user, sid, f.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range plan.Items {
				if it.Kind == review.ItemNew {
					got[kind[it.ProblemID]]++
				}
			}
		}
		return got
	}
	weighted := count(live.Builder(f.s))
	stub := count(f.cards.Builder(f.s))
	t.Logf("recall weak %.3f strong %.3f; new introduced with mastery %v, with stub %v", rw, rs, weighted, stub)

	// With boost 8 the weights are about 5.3 (weak, recall 0.47), 5 (no data,
	// 0.5) and 2.7 (strong, 0.79), so shares near 0.41 / 0.38 / 0.21; the stub
	// weighs all alike (one third each). Bounds are several standard errors
	// out (n = 900, sd about 0.016).
	share := func(m map[string]int, name string) float64 {
		n := 0
		for _, v := range m {
			n += v
		}
		return float64(m[name]) / float64(n)
	}
	sw, ss, se := share(weighted, "weak"), share(weighted, "strong"), share(weighted, "empty")
	if sw < 0.36 || ss > 0.27 || sw-ss < 0.10 {
		t.Errorf("with mastery shares weak %.3f strong %.3f empty %.3f; want weak above strong, about 0.41 vs 0.21", sw, ss, se)
	}
	if got := share(stub, "weak"); got < 0.27 || got > 0.40 {
		t.Errorf("weak share with stub = %.3f, want about one third", got)
	}

	// Building sessions must not touch a single card, hence no due date.
	if after := f.list(); !reflect.DeepEqual(before, after) {
		t.Errorf("cards changed by building sessions:\nbefore %+v\nafter  %+v", before, after)
	}
}
