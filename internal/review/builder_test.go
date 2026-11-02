// SPDX-License-Identifier: Apache-2.0

package review_test

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/settings"
)

// world is an in-memory stand-in for the stores the builder reads.
type world struct {
	ids      *domain.IDGenerator
	user     domain.ID
	cfg      settings.Settings
	live     map[domain.ID]int // live problem -> epoch
	cards    map[domain.ID]review.Card
	attempts []attempts.Attempt
}

func newWorld() *world {
	return &world{
		ids:  domain.NewIDGenerator(domain.SystemClock{}),
		user: domain.NewIDGenerator(domain.SystemClock{}).New(),
		cfg: settings.Settings{DesiredRetention: 0.9, NewPerDay: 20, MaxReviewsPerDay: 200,
			NewInterval: 4, WeakTagBoost: 1, RolloverHour: 4, Timezone: "UTC"},
		live:  map[domain.ID]int{},
		cards: map[domain.ID]review.Card{},
	}
}

var now = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func (w *world) problem() domain.ID {
	id := w.ids.New()
	w.live[id] = 1
	return id
}

// dueCard stores a review-state card that is due, last reviewed daysAgo days
// before now (a longer gap means lower recall).
func (w *world) dueCard(daysAgo int) domain.ID {
	id := w.problem()
	w.cards[id] = review.Card{UserID: w.user, ProblemID: id, Epoch: 1, Reps: 1, Card: fsrs.Card{
		State: fsrs.Review, Stability: 5, Difficulty: 5,
		LastReview: now.AddDate(0, 0, -daysAgo), Due: now.Add(-time.Hour)}}
	w.attempts = append(w.attempts, attempts.Attempt{ID: w.ids.New(), UserID: w.user, ProblemID: id,
		CardEpoch: 1, AnsweredAt: now.AddDate(0, 0, -daysAgo)})
	return id
}

// answer records an attempt at the problem and updates its card like the
// projection would.
func (w *world) answer(id domain.ID, at time.Time) {
	w.attempts = append(w.attempts, attempts.Attempt{ID: w.ids.New(), UserID: w.user, ProblemID: id,
		CardEpoch: w.live[id], AnsweredAt: at})
	c := w.cards[id]
	c.UserID, c.ProblemID, c.Epoch = w.user, id, w.live[id]
	c.Card = fsrs.Next(c.Card, fsrs.Good, at)
	c.Reps++
	w.cards[id] = c
}

func (w *world) builder() *review.Builder {
	return review.NewBuilder(review.BuildSource{
		Cards: cardList{w}, Attempts: attemptLog{w}, Problems: problemList{w},
		Params: paramsFn(func() fsrs.Params { return fsrs.Default() }), Settings: settingsFn(func() settings.Settings { return w.cfg }),
	})
}

type cardList struct{ w *world }

func (cardList) Get(context.Context, domain.ID, domain.ID) (review.Card, error) {
	panic("unused")
}
func (cardList) Put(context.Context, domain.ID, review.Card) error { panic("unused") }
func (cardList) Delete(context.Context, domain.ID, domain.ID) error {
	panic("unused")
}
func (cardList) DeleteAll(context.Context, domain.ID) error { panic("unused") }
func (cardList) LockOwner(context.Context, domain.ID) error { panic("unused") }
func (cardList) Query(context.Context, domain.ID, review.CardFilter) ([]review.Card, error) {
	panic("unused")
}
func (c cardList) List(context.Context, domain.ID) ([]review.Card, error) {
	var out []review.Card
	for _, card := range c.w.cards {
		out = append(out, card)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].ProblemID[:], out[j].ProblemID[:]) < 0 })
	return out, nil
}

type attemptLog struct{ w *world }

func (a attemptLog) ForCard(_ context.Context, _, p domain.ID, epoch int) ([]attempts.Attempt, error) {
	var out []attempts.Attempt
	for _, x := range a.w.attempts {
		if x.ProblemID == p && x.CardEpoch == epoch {
			out = append(out, x)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AnsweredAt.Before(out[j].AnsweredAt) })
	return out, nil
}

func (a attemptLog) List(_ context.Context, _ domain.ID, f attempts.ListFilter) ([]attempts.Attempt, error) {
	var out []attempts.Attempt
	for _, x := range a.w.attempts {
		if (f.Since.IsZero() || !x.AnsweredAt.Before(f.Since)) && (f.Until.IsZero() || x.AnsweredAt.Before(f.Until)) {
			out = append(out, x)
		}
	}
	return out, nil
}

type problemList struct{ w *world }

func (problemList) Epoch(context.Context, domain.ID, domain.ID) (review.ProblemEpoch, error) {
	panic("unused")
}
func (p problemList) LiveEpochs(context.Context, domain.ID) ([]review.ProblemEpoch, error) {
	var out []review.ProblemEpoch
	for id, e := range p.w.live {
		out = append(out, review.ProblemEpoch{ID: id, Live: true, Epoch: e})
	}
	return out, nil
}

type paramsFn func() fsrs.Params

func (f paramsFn) Params(context.Context, domain.ID) (fsrs.Params, error) { return f(), nil }

type settingsFn func() settings.Settings

func (f settingsFn) Get(context.Context, domain.ID) (settings.Settings, error) { return f(), nil }

func kinds(items []review.Item) string {
	s := ""
	for _, it := range items {
		if it.Kind == review.ItemNew {
			s += "N"
		} else {
			s += "R"
		}
	}
	return s
}

func ids(items []review.Item) []domain.ID {
	out := make([]domain.ID, len(items))
	for i, it := range items {
		out[i] = it.ProblemID
	}
	return out
}

func build(t *testing.T, w *world, session domain.ID, at time.Time) review.Plan {
	t.Helper()
	p, err := w.builder().Build(context.Background(), w.user, session, at)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildSameSessionSameList(t *testing.T) {
	w := newWorld()
	w.cfg.NewPerDay = 8
	for range 30 {
		w.problem()
	}
	for i := range 6 {
		w.dueCard(i + 1)
	}
	s1, s2 := w.ids.New(), w.ids.New()
	a, b := build(t, w, s1, now), build(t, w, s1, now)
	if len(a.Items) != 14 || !reflect.DeepEqual(a.Items, b.Items) {
		t.Fatalf("same session ID gave different lists (%d items):\n%v\n%v", len(a.Items), ids(a.Items), ids(b.Items))
	}
	if c := build(t, w, s2, now); reflect.DeepEqual(ids(a.Items), ids(c.Items)) {
		t.Error("different session IDs sampled the same new cards (30 candidates, 8 picked)")
	}
}

func TestBuildNewProblemsAreLiveWithoutCards(t *testing.T) {
	w := newWorld()
	fresh := w.problem()
	carded := w.dueCard(1)
	p := build(t, w, w.ids.New(), now)
	if len(p.Items) != 2 {
		t.Fatalf("items = %+v", p.Items)
	}
	got := map[domain.ID]review.ItemKind{}
	for _, it := range p.Items {
		got[it.ProblemID] = it.Kind
	}
	if got[fresh] != review.ItemNew || got[carded] != review.ItemReview {
		t.Errorf("kinds = %v", got)
	}

	// A card whose problem is no longer live is never served.
	delete(w.live, carded)
	if p = build(t, w, w.ids.New(), now); len(p.Items) != 1 || p.Items[0].ProblemID != fresh {
		t.Errorf("after the problem left live: %+v", p.Items)
	}
}

func TestBuildDueLowestRecallFirstAndCapped(t *testing.T) {
	w := newWorld()
	w.cfg.MaxReviewsPerDay = 3
	byAge := map[int]domain.ID{}
	for _, d := range []int{3, 40, 9, 20, 1} {
		byAge[d] = w.dueCard(d)
	}
	notDue := w.dueCard(2)
	c := w.cards[notDue]
	c.Due = now.Add(time.Hour)
	w.cards[notDue] = c

	p := build(t, w, w.ids.New(), now)
	want := []domain.ID{byAge[40], byAge[20], byAge[9]}
	if !reflect.DeepEqual(ids(p.Items), want) {
		t.Fatalf("due order = %v, want %v", ids(p.Items), want)
	}
}

func TestBuildInterleavesOneNewPerInterval(t *testing.T) {
	w := newWorld()
	w.cfg.NewInterval = 3
	for i := range 7 {
		w.dueCard(i + 1)
	}
	for range 3 {
		w.problem()
	}
	if got := kinds(build(t, w, w.ids.New(), now).Items); got != "RRRNRRRNRN" {
		t.Errorf("pattern = %s", got)
	}

	// More new cards than reviews leave the rest at the end; no reviews, only new.
	w2 := newWorld()
	w2.cfg.NewInterval = 4
	w2.dueCard(1)
	w2.dueCard(2)
	for range 3 {
		w2.problem()
	}
	if got := kinds(build(t, w2, w2.ids.New(), now).Items); got != "RRNNN" {
		t.Errorf("pattern = %s", got)
	}
}

func TestBuildDailyLimitsHoldAcrossSessions(t *testing.T) {
	w := newWorld()
	w.cfg.NewPerDay, w.cfg.MaxReviewsPerDay = 3, 4
	for range 10 {
		w.problem()
	}
	for i := range 6 {
		w.dueCard(i + 1)
	}

	// Session 1 gets the full budget; answer half of it.
	s1 := build(t, w, w.ids.New(), now)
	if k := kinds(s1.Items); count(k, 'R') != 4 || count(k, 'N') != 3 {
		t.Fatalf("session 1 = %s, want 4 reviews and 3 new", k)
	}
	reviewsDone, newDone := 0, 0
	at := now.Add(-3 * time.Hour) // 09:00, same study day
	for _, it := range s1.Items {
		switch {
		case it.Kind == review.ItemReview && reviewsDone < 1:
			reviewsDone++
		case it.Kind == review.ItemNew && newDone < 2:
			newDone++
		default:
			continue
		}
		w.answer(it.ProblemID, at)
		at = at.Add(time.Minute)
	}

	// Session 2 sees only what is left of the day's budget.
	s2 := build(t, w, w.ids.New(), now)
	if s2.Used.Reviews != 1 || s2.Used.NewCards != 2 {
		t.Errorf("usage = %+v, want 1 review and 2 new", s2.Used)
	}
	if k := kinds(s2.Items); count(k, 'N') != 1 || count(k, 'R') > 3 {
		t.Errorf("session 2 = %s, want 1 new and at most 3 reviews", k)
	}

	// Spend the rest: a third session is empty.
	for _, it := range s2.Items {
		w.answer(it.ProblemID, at)
		at = at.Add(time.Minute)
	}
	if s3 := build(t, w, w.ids.New(), now); len(s3.Items) != 0 {
		t.Errorf("session 3 = %s with usage %+v, want no new cards and no reviews left", kinds(s3.Items), s3.Used)
	}

	// The next study day starts afresh.
	next := now.Add(24 * time.Hour)
	if s := build(t, w, w.ids.New(), next); s.Used != (review.Usage{}) {
		t.Errorf("next day usage = %+v", s.Used)
	}
}

func TestBuildUsesStudyDayRollover(t *testing.T) {
	w := newWorld()
	p := w.problem()
	w.answer(p, time.Date(2026, 3, 10, 3, 30, 0, 0, time.UTC)) // before the 04:00 rollover
	// At 03:45 on the 10th this answer still counts for the 9th's study day...
	if got := build(t, w, w.ids.New(), time.Date(2026, 3, 10, 3, 45, 0, 0, time.UTC)).Used; got.NewCards != 1 {
		t.Errorf("same study day usage = %+v", got)
	}
	// ...and at noon it belongs to yesterday.
	if got := build(t, w, w.ids.New(), now).Used; got != (review.Usage{}) {
		t.Errorf("next study day usage = %+v", got)
	}
}

func TestAppendDue(t *testing.T) {
	w := newWorld()
	learning := w.problem()
	w.cards[learning] = review.Card{UserID: w.user, ProblemID: learning, Epoch: 1, Card: fsrs.Card{
		State: fsrs.Learning, Due: now.Add(-time.Minute), LastReview: now.Add(-2 * time.Minute)}}
	waiting := w.problem()
	w.cards[waiting] = review.Card{UserID: w.user, ProblemID: waiting, Epoch: 1, Card: fsrs.Card{
		State: fsrs.Relearning, Due: now.Add(-time.Minute), LastReview: now.Add(-2 * time.Minute)}}
	later := w.problem()
	w.cards[later] = review.Card{UserID: w.user, ProblemID: later, Epoch: 1, Card: fsrs.Card{
		State: fsrs.Learning, Due: now.Add(5 * time.Minute), LastReview: now}}
	dueReview := w.dueCard(3) // a plain review card is not a mid-session addition

	got, err := w.builder().AppendDue(context.Background(), w.user, []domain.ID{waiting}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []review.Item{{ProblemID: learning, Epoch: 1, Kind: review.ItemReview}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("appended = %+v, want %+v (dueReview %v, later %v)", got, want, dueReview, later)
	}
	// Once the five minutes pass, the later card comes due too.
	got, _ = w.builder().AppendDue(context.Background(), w.user, []domain.ID{waiting, learning}, now.Add(6*time.Minute))
	if len(got) != 1 || got[0].ProblemID != later {
		t.Errorf("later append = %+v", got)
	}
}

func count(s string, b byte) int {
	n := 0
	for i := range len(s) {
		if s[i] == b {
			n++
		}
	}
	return n
}

// TestTodayDueMatchesSessionBuilderAtBoundary pins the one due rule: a card
// due exactly at now is due for both Today and the session builder, one due a
// moment later is for neither, and the card of a non-live problem or of a
// stale epoch (a major edit since) counts for neither.
func TestTodayDueMatchesSessionBuilderAtBoundary(t *testing.T) {
	w := newWorld()
	w.cfg.NewPerDay = 0 // only review items, so the plan length is the due count
	setDue := func(id domain.ID, due time.Time) {
		c := w.cards[id]
		c.Due = due
		w.cards[id] = c
	}
	before, exact, after, retired, stale := w.dueCard(3), w.dueCard(3), w.dueCard(3), w.dueCard(3), w.dueCard(3)
	setDue(before, now.Add(-time.Nanosecond))
	setDue(exact, now)
	setDue(after, now.Add(time.Nanosecond))
	delete(w.live, retired) // card remains but its problem is no longer live
	w.live[stale] = 2       // major edit: the due card is of epoch 1, the problem now at 2

	b := w.builder()
	plan, err := b.Build(context.Background(), w.user, w.ids.New(), now)
	if err != nil {
		t.Fatal(err)
	}
	today, err := b.Today(context.Background(), w.user, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 || today.Due != 2 {
		t.Errorf("builder serves %d, Today counts %d; want both 2 (before and exactly-now)", len(plan.Items), today.Due)
	}
	got := map[domain.ID]bool{}
	for _, it := range plan.Items {
		got[it.ProblemID] = true
	}
	if !got[before] || !got[exact] || got[after] || got[retired] || got[stale] {
		t.Errorf("served set wrong: %v", got)
	}
}
