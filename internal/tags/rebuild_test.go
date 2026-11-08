// SPDX-License-Identifier: Apache-2.0

package tags_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// rebuildFx is a real sqlite store with real problems, attempts, cards and the
// mastery projection, and a tags service that rebuilds mastery on every
// applied or reverted operation.
type rebuildFx struct {
	t       *testing.T
	ctx     context.Context
	st      *sqlite.Store
	clock   *domain.FakeClock
	mastery store.Mastery
	svc     *tags.Service
	probs   *problems.Service
	atts    *attempts.Service
	owner   domain.ID
	actor   domain.Actor
	ids     *domain.IDGenerator
}

// newRebuildFx builds the fixture; tx wraps the tags transactor (nil: as is).
func newRebuildFx(t *testing.T, wrap func(tags.Transactor) tags.Transactor) *rebuildFx {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := domain.NewFakeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	cards := store.DefaultCards()
	mastery := store.NewMastery(cards, clock)
	u := auth.User{ID: ids.New(), DisplayName: "A", Email: "a@example.com", CreatedAt: clock.Now()}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	var tx tags.Transactor = mastery.TagsTx(st)
	if wrap != nil {
		tx = wrap(tx)
	}
	probs := problems.NewService(st.Problems(), store.ProblemsEditTx(st, cards.MajorEdit), clock, ids)
	return &rebuildFx{t: t, ctx: ctx, st: st, clock: clock, mastery: mastery, svc: tags.NewService(tx, clock, ids),
		probs: probs, atts: attempts.NewService(store.AttemptsCardTx(st, mastery.CardUpdater), probs, clock, ids),
		owner: u.ID, actor: domain.Actor{Kind: domain.ActorHuman, UserID: u.ID}, ids: ids}
}

func (f *rebuildFx) tag(slug string) tags.Tag {
	f.t.Helper()
	tg, err := f.svc.Create(f.ctx, f.owner, tags.CreateInput{Slug: slug, Name: slug})
	if err != nil {
		f.t.Fatal(err)
	}
	return tg
}

// problem creates a live multiple choice problem carrying the tags, and
// answers it right or wrong a few times so that it has a card and attempts.
func (f *rebuildFx) problem(rng *rand.Rand, slugs ...string) domain.ID {
	f.t.Helper()
	v, err := f.probs.Create(f.ctx, f.actor, problems.CreateInput{Type: problems.TypeMCQ, Prompt: "Pick b",
		Payload: json.RawMessage(`{"choices":[{"id":"a","text":"A"},{"id":"b","text":"B"}],"correct":["b"]}`),
		Status:  problems.StatusLive})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.svc.SetProblemTags(f.ctx, f.owner, v.ID, slugs); err != nil {
		f.t.Fatal(err)
	}
	for i := rng.Intn(4); i > 0; i-- {
		f.clock.Advance(time.Duration(1+rng.Intn(72)) * time.Hour)
		resp := `{"selected":["a"]}`
		if rng.Intn(3) > 0 {
			resp = `{"selected":["b"]}`
		}
		if _, err := f.atts.Answer(f.ctx, f.actor, attempts.AnswerInput{ProblemID: v.ID, ProblemVersion: v.Version,
			Response: json.RawMessage(resp), LatencyMS: 5}); err != nil {
			f.t.Fatal(err)
		}
	}
	return v.ID
}

func (f *rebuildFx) attemptCount() int {
	f.t.Helper()
	l, err := f.st.Attempts().List(f.ctx, f.owner, attempts.ListFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	return len(l)
}

func (f *rebuildFx) masteryRows() []metrics.TagMastery {
	f.t.Helper()
	l, err := f.st.Mastery().List(f.ctx, f.owner)
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

func (f *rebuildFx) snapshots() map[domain.ID][]metrics.MasterySnapshot {
	f.t.Helper()
	active, err := f.st.Mastery().ActiveTags(f.ctx, f.owner)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[domain.ID][]metrics.MasterySnapshot{}
	for _, tg := range active {
		l, err := f.st.MasterySnapshots().List(f.ctx, f.owner, tg.ID, "", "")
		if err != nil {
			f.t.Fatal(err)
		}
		out[tg.ID] = l
	}
	return out
}

func (f *rebuildFx) activeSlugs() []string {
	f.t.Helper()
	ts, err := f.st.Tags().List(f.ctx, f.owner, tags.ListFilter{Status: tags.StatusActive})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, tg := range ts {
		out = append(out, tg.Slug)
	}
	return out
}

func (f *rebuildFx) problemsOf(slug string) []domain.ID {
	f.t.Helper()
	tg, err := f.svc.Get(f.ctx, f.owner, slug)
	if err != nil {
		f.t.Fatal(err)
	}
	ids, err := f.st.Tags().ProblemIDs(f.ctx, f.owner, tg.ID, domain.Nil, 500)
	if err != nil {
		f.t.Fatal(err)
	}
	return ids
}

// TestRandomTagOperationsKeepMasteryEqualToRebuild applies random sequences of
// merges, splits and reverts through the lifecycle and checks, after every
// step, that no attempt was added or removed and that the mastery rows and the
// snapshot history equal what a from-scratch rebuild produces.
func TestRandomTagOperationsKeepMasteryEqualToRebuild(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4, 5, 6} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			f := newRebuildFx(t, nil)
			base := []string{"a", "b", "c", "d", "e"}
			for _, s := range base {
				f.tag(s)
			}
			for range 14 {
				var pick []string
				for _, s := range base {
					if rng.Intn(2) == 0 {
						pick = append(pick, s)
					}
				}
				f.problem(rng, pick...)
			}
			// Freeze time: recall is predicted at clock.Now(), so rows only
			// equal a later rebuild if now did not move since they were
			// written. Start from a baseline rebuilt at the frozen time.
			if err := f.mastery.Service(f.st).RebuildUser(f.ctx, f.owner); err != nil {
				t.Fatal(err)
			}
			if _, err := metricsSnapshotter(f).BackfillUser(f.ctx, f.owner, nil); err != nil {
				t.Fatal(err)
			}
			attemptsBefore := f.attemptCount()
			if attemptsBefore == 0 {
				t.Fatal("fixture has no attempts")
			}

			var applied []domain.ID
			next, done := 0, 0
			for step := range 24 {
				var err error
				switch rng.Intn(4) {
				case 0, 1:
					err = f.randomMerge(rng, &applied)
				case 2:
					err = f.randomSplit(rng, &next, &applied)
				default:
					err = f.randomRevert(rng, &applied)
				}
				var p *platform.Problem
				if err != nil && !errors.Is(err, errSkip) && !errors.Is(err, tags.ErrRevertConflict) && !errors.As(err, &p) {
					t.Fatalf("step %d: %v", step, err)
				}
				if err == nil {
					done++
				}
				if got := f.attemptCount(); got != attemptsBefore {
					t.Fatalf("step %d: %d attempts, want %d", step, got, attemptsBefore)
				}
				f.assertRebuildEqual(step)
			}
			if done < 8 {
				t.Fatalf("only %d of 24 steps did something; the sequence is too weak", done)
			}
		})
	}
}

var errSkip = errors.New("nothing to do")

func (f *rebuildFx) randomMerge(rng *rand.Rand, applied *[]domain.ID) error {
	act := f.activeSlugs()
	if len(act) < 2 {
		return errSkip
	}
	rng.Shuffle(len(act), func(i, j int) { act[i], act[j] = act[j], act[i] })
	n := 1 + rng.Intn(min(3, len(act)-1))
	return f.apply(rng, tags.OpMerge, tags.MergeInput{Sources: act[1 : 1+n], Target: act[0]}, applied)
}

func (f *rebuildFx) randomSplit(rng *rand.Rand, next *int, applied *[]domain.ID) error {
	act := f.activeSlugs()
	if len(act) == 0 {
		return errSkip
	}
	src := act[rng.Intn(len(act))]
	in := tags.SplitInput{Source: src, Assignments: map[domain.ID][]string{}, RetireSource: rng.Intn(2) == 0}
	var slugs []string
	for range 2 {
		*next++
		s := fmt.Sprintf("n%d", *next)
		slugs = append(slugs, s)
		in.Targets = append(in.Targets, tags.SplitTarget{New: &tags.CreateInput{Slug: s, Name: s}})
	}
	if rng.Intn(3) == 0 && len(act) > 1 { // an existing target as well
		for _, s := range act {
			if s != src {
				in.Targets = append(in.Targets, tags.SplitTarget{Ref: s})
				slugs = append(slugs, s)
				break
			}
		}
	}
	for _, pid := range f.problemsOf(src) {
		if in.RetireSource || rng.Intn(3) > 0 {
			in.Assignments[pid] = []string{slugs[rng.Intn(len(slugs))]}
		}
	}
	return f.apply(rng, tags.OpSplit, in, applied)
}

func (f *rebuildFx) randomRevert(rng *rand.Rand, applied *[]domain.ID) error {
	if len(*applied) == 0 {
		return errSkip
	}
	i := rng.Intn(len(*applied))
	id := (*applied)[i]
	if _, err := f.svc.RevertOperation(f.ctx, f.owner, f.actor, id); err != nil {
		return err
	}
	*applied = slices.Delete(*applied, i, i+1)
	return nil
}

// apply runs the operation through the lifecycle, either at once (apply:true)
// or as a proposal that is applied afterwards.
func (f *rebuildFx) apply(rng *rand.Rand, kind tags.OpKind, payload any, applied *[]domain.ID) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := rng.Intn(2) == 0
	d, err := f.svc.Propose(f.ctx, f.owner, f.actor, tags.ProposeInput{Kind: kind, Payload: raw, Apply: now})
	if err != nil {
		return err
	}
	if !now {
		if d, err = f.svc.ApplyOperation(f.ctx, f.owner, f.actor, d.Operation.ID); err != nil {
			return err
		}
	}
	*applied = append(*applied, d.Operation.ID)
	return nil
}

// assertRebuildEqual compares the incrementally maintained mastery rows and
// snapshots with a from-scratch rebuild of the same state.
func (f *rebuildFx) assertRebuildEqual(step int) {
	f.t.Helper()
	gotRows, gotSnaps := f.masteryRows(), f.snapshots()
	if err := f.mastery.Service(f.st).RebuildUser(f.ctx, f.owner); err != nil {
		f.t.Fatal(err)
	}
	if want := f.masteryRows(); !reflect.DeepEqual(gotRows, want) {
		f.t.Fatalf("step %d: mastery rows differ from a rebuild\n got  %+v\n want %+v", step, gotRows, want)
	}
	if _, err := metricsSnapshotter(f).BackfillUser(f.ctx, f.owner, nil); err != nil {
		f.t.Fatal(err)
	}
	if want := f.snapshots(); !reflect.DeepEqual(gotSnaps, want) {
		f.t.Fatalf("step %d: snapshot history differs from a backfill\n got  %+v\n want %+v", step, gotSnaps, want)
	}
}

func metricsSnapshotter(f *rebuildFx) *metrics.Snapshotter {
	return f.mastery.Snapshotter(f.st, nil)
}

// recorder is a MasteryRebuilder that remembers its calls.
type recorder struct{ calls [][]domain.ID }

func (r *recorder) UpdateTags(context.Context, domain.ID, []domain.ID) error { return nil }

func (r *recorder) ForgetTags(context.Context, domain.ID, []domain.ID) error { return nil }

func (r *recorder) RebuildTags(_ context.Context, _ domain.ID, ids []domain.ID) error {
	r.calls = append(r.calls, slices.Clone(ids))
	return nil
}

type recordingTx struct {
	inner tags.Transactor
	rec   *recorder
}

func (t recordingTx) InTx(ctx context.Context, fn func(tags.Repos) error) error {
	return t.inner.InTx(ctx, func(r tags.Repos) error {
		r.Mastery = t.rec
		return fn(r)
	})
}

// TestApplyAndRevertRebuildMastery checks that every path that changes
// mappings rebuilds the tags involved, and a proposal alone does not.
func TestApplyAndRevertRebuildMastery(t *testing.T) {
	rec := &recorder{}
	f := newRebuildFx(t, func(in tags.Transactor) tags.Transactor { return recordingTx{in, rec} })
	a, b, dst := f.tag("a"), f.tag("b"), f.tag("dst")
	f.problem(rand.New(rand.NewSource(1)), "a", "b")

	last := func() []domain.ID {
		t.Helper()
		if len(rec.calls) == 0 {
			t.Fatal("no rebuild call")
		}
		return rec.calls[len(rec.calls)-1]
	}
	same := func(got []domain.ID, want ...domain.ID) {
		t.Helper()
		g, w := slices.Clone(got), slices.Clone(want)
		slices.SortFunc(g, compareStr)
		slices.SortFunc(w, compareStr)
		if !slices.Equal(g, w) {
			t.Fatalf("rebuilt tags = %v, want %v", g, w)
		}
	}
	merge := mustJSON(t, tags.MergeInput{Sources: []string{"a"}, Target: "dst"})

	// A proposal changes nothing, so it rebuilds nothing.
	d, err := f.svc.Propose(f.ctx, f.owner, f.actor, tags.ProposeInput{Kind: tags.OpMerge, Payload: merge})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("proposal rebuilt %v", rec.calls)
	}
	// ApplyOperation rebuilds the source and the target.
	if _, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, d.Operation.ID); err != nil {
		t.Fatal(err)
	}
	same(last(), a.ID, dst.ID)

	// Reverting rebuilds the same tags again.
	n := len(rec.calls)
	if _, err := f.svc.RevertOperation(f.ctx, f.owner, f.actor, d.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != n+1 {
		t.Fatalf("revert made %d rebuild calls, want 1", len(rec.calls)-n)
	}
	same(last(), a.ID, dst.ID)

	// Propose with apply:true rebuilds.
	n = len(rec.calls)
	d, err = f.svc.Propose(f.ctx, f.owner, f.actor, tags.ProposeInput{Kind: tags.OpMerge,
		Payload: mustJSON(t, tags.MergeInput{Sources: []string{"b"}, Target: "dst"}), Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != n+1 {
		t.Fatalf("apply:true made %d rebuild calls, want 1", len(rec.calls)-n)
	}
	same(last(), b.ID, dst.ID)

	// Direct merge and split rebuild too.
	n = len(rec.calls)
	if _, err := f.svc.Merge(f.ctx, f.owner, tags.MergeInput{Sources: []string{"a"}, Target: "dst"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != n+1 {
		t.Fatalf("direct merge made %d rebuild calls, want 1", len(rec.calls)-n)
	}
	same(last(), a.ID, dst.ID)

	n = len(rec.calls)
	res, err := f.svc.Split(f.ctx, f.owner, f.actor, tags.SplitInput{Source: "dst",
		Targets: []tags.SplitTarget{{New: &tags.CreateInput{Slug: "part", Name: "Part"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != n+1 {
		t.Fatalf("direct split made %d rebuild calls, want 1", len(rec.calls)-n)
	}
	same(last(), dst.ID, res.Targets[0].ID)

	// A failed operation rolls back and rebuilds nothing.
	n = len(rec.calls)
	if _, err := f.svc.Merge(f.ctx, f.owner, tags.MergeInput{Sources: []string{"nope"}, Target: "dst"}); err == nil {
		t.Fatal("merge of a missing tag succeeded")
	}
	if len(rec.calls) != n {
		t.Fatal("a failed merge rebuilt mastery")
	}
	_ = d
}

func compareStr(a, b domain.ID) int {
	switch as, bs := a.String(), b.String(); {
	case as < bs:
		return -1
	case as > bs:
		return 1
	}
	return 0
}

// TestRevertRestoresMasteryRows checks the effect, not just the call: after a
// merge the sources have no mastery row, after the revert they have the rows
// they had before.
func TestRevertRestoresMasteryRows(t *testing.T) {
	f := newRebuildFx(t, nil)
	rng := rand.New(rand.NewSource(7))
	f.tag("x")
	f.tag("y")
	f.problem(rng, "x")
	f.problem(rng, "x", "y")
	f.problem(rng, "y")
	f.clock.Advance(time.Hour)
	if err := f.mastery.Service(f.st).RebuildUser(f.ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	before := f.masteryRows()
	if len(before) != 2 {
		t.Fatalf("rows before = %+v", before)
	}
	d, err := f.svc.Propose(f.ctx, f.owner, f.actor, tags.ProposeInput{Kind: tags.OpMerge, Apply: true,
		Payload: mustJSON(t, tags.MergeInput{Sources: []string{"x"}, Target: "y"})})
	if err != nil {
		t.Fatal(err)
	}
	if rows := f.masteryRows(); len(rows) != 1 {
		t.Fatalf("rows after merge = %+v, want only the target's", rows)
	}
	if _, err := f.svc.RevertOperation(f.ctx, f.owner, f.actor, d.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if after := f.masteryRows(); !reflect.DeepEqual(after, before) {
		t.Fatalf("rows after revert\n got  %+v\n want %+v", after, before)
	}
}
