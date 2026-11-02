// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/metrics"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func runMastery(t *testing.T, newStore NewStore) {
	t.Run("repo", func(t *testing.T) { runMasteryRepo(t, newStore) })
	t.Run("inputs", func(t *testing.T) { runMasteryInputs(t, newStore) })
	t.Run("user stats", func(t *testing.T) { runUserStats(t, newStore) })
	t.Run("metrics reads", func(t *testing.T) { runMetricsReads(t, newStore) })
	t.Run("full recompute equals incremental", func(t *testing.T) { runMasteryProperty(t, newStore) })
	t.Run("weak tags are introduced more often", func(t *testing.T) { runWeakTagWeighting(t, newStore) })
}

// masteryFixture is the card fixture with the attempts service wired to also
// maintain mastery, evaluated at a fixed time so that rows computed at
// different moments are comparable.
type masteryFixture struct {
	*cardFixture
	eval    *stepClock
	mastery store.Mastery
	ids     *domain.IDGenerator
}

func newMasteryFixture(t *testing.T, newStore NewStore) *masteryFixture {
	t.Helper()
	f := &masteryFixture{cardFixture: newCardFixture(t, newStore)}
	f.ids = domain.NewIDGenerator(f.clock)
	f.eval = &stepClock{t: t0.Add(10 * 365 * 24 * time.Hour)}
	f.mastery = store.NewMastery(f.cards, f.eval)
	probs := problems.NewService(f.s.Problems(), store.ProblemsEditTx(f.s, f.cards.MajorEdit), f.clock, f.ids)
	f.probs = probs
	f.atts = attempts.NewService(store.AttemptsCardTx(f.s, f.mastery.CardUpdater), probs, f.clock, f.ids)
	return f
}

func (f *masteryFixture) newTag(owner domain.ID, slug string) domain.ID {
	f.t.Helper()
	id := f.ids.New()
	tg := tags.Tag{ID: id, OwnerID: owner, Slug: slug, Name: slug, Status: tags.StatusActive,
		CreatedAt: t0, UpdatedAt: t0}
	if err := f.s.Tags().Create(f.ctx, owner, tg); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *masteryFixture) tag(owner, problem domain.ID, tagIDs ...domain.ID) {
	f.t.Helper()
	if err := f.s.Tags().SetProblemTags(f.ctx, owner, problem, tagIDs); err != nil {
		f.t.Fatal(err)
	}
}

func (f *masteryFixture) rows(owner domain.ID) []metrics.TagMastery {
	f.t.Helper()
	l, err := f.s.Mastery().List(f.ctx, owner)
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

func (f *masteryFixture) rebuild(owner domain.ID) {
	f.t.Helper()
	if err := f.mastery.Service(f.s).RebuildUser(f.ctx, owner); err != nil {
		f.t.Fatal(err)
	}
}

func runMasteryRepo(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	other := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	tagA, tagB := f.newTag(f.user, "a"), f.newTag(f.user, "b")
	tagOther := f.newTag(other.ID, "a")

	row := func(owner, tag domain.ID) metrics.TagMastery {
		return metrics.TagMastery{UserID: owner, TagID: tag, Recall: 0.8125, StabilityMedian: 12.5,
			CardsTotal: 5, CardsSeen: 3, Reviews: 9, Lapses: 2, LastAttemptAt: t0.Add(time.Hour)}
	}
	repo := f.s.Mastery()

	t.Run("put and get round trip", func(t *testing.T) {
		full := row(f.user, tagA)
		if err := repo.Put(f.ctx, f.user, full); err != nil {
			t.Fatal(err)
		}
		got, err := repo.Get(f.ctx, f.user, tagA)
		if err != nil || !reflect.DeepEqual(got, full) {
			t.Fatalf("Get = %+v, %v; want %+v", got, err, full)
		}
		if got.Coverage() != 0.6 || got.LapseRate() != 2.0/9 {
			t.Errorf("coverage %v lapse rate %v", got.Coverage(), got.LapseRate())
		}

		// A tag with no attempt has no last attempt time.
		bare := metrics.TagMastery{UserID: f.user, TagID: tagB}
		if err := repo.Put(f.ctx, f.user, bare); err != nil {
			t.Fatal(err)
		}
		if got, err = repo.Get(f.ctx, f.user, tagB); err != nil || !reflect.DeepEqual(got, bare) {
			t.Fatalf("bare = %+v, %v; want %+v", got, err, bare)
		}
	})

	t.Run("put replaces the row of the tag", func(t *testing.T) {
		second := row(f.user, tagA)
		second.Recall, second.CardsSeen = 0.5, 4
		if err := repo.Put(f.ctx, f.user, second); err != nil {
			t.Fatal(err)
		}
		list, err := repo.List(f.ctx, f.user)
		if err != nil || len(list) != 2 {
			t.Fatalf("list = %+v, %v; want 2 rows", list, err)
		}
		want := []domain.ID{tagA, tagB}
		slices.SortFunc(want, func(x, y domain.ID) int { return compareIDs(x, y) })
		if list[0].TagID != want[0] || list[1].TagID != want[1] {
			t.Errorf("list is not ordered by tag ID: %v", list)
		}
		if got, _ := repo.Get(f.ctx, f.user, tagA); !reflect.DeepEqual(got, second) {
			t.Errorf("replaced row = %+v, want %+v", got, second)
		}
	})

	t.Run("owner scoping", func(t *testing.T) {
		if err := repo.Put(f.ctx, other.ID, row(other.ID, tagA)); !errors.Is(err, metrics.ErrNotFound) {
			t.Errorf("Put of another owner's tag = %v, want ErrNotFound", err)
		}
		if err := repo.Put(f.ctx, f.user, row(other.ID, tagA)); err == nil {
			t.Error("Put with a mismatched user ID succeeded")
		}
		if err := repo.Put(f.ctx, other.ID, row(other.ID, tagOther)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Get(f.ctx, other.ID, tagA); !errors.Is(err, metrics.ErrNotFound) {
			t.Errorf("Get of another owner's row = %v, want ErrNotFound", err)
		}
		if l, _ := repo.List(f.ctx, other.ID); len(l) != 1 || l[0].TagID != tagOther {
			t.Errorf("other owner's list = %+v", l)
		}
		if ids, err := repo.ActiveTagIDs(f.ctx, other.ID); err != nil || !reflect.DeepEqual(ids, []domain.ID{tagOther}) {
			t.Errorf("ActiveTagIDs = %v, %v; want only the owner's tag", ids, err)
		}

		// Delete and DeleteAll touch only the owner's rows.
		if err := repo.Delete(f.ctx, other.ID, tagA); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Get(f.ctx, f.user, tagA); err != nil {
			t.Errorf("another owner's Delete removed my row: %v", err)
		}
		if err := repo.DeleteAll(f.ctx, other.ID); err != nil {
			t.Fatal(err)
		}
		if l, _ := repo.List(f.ctx, f.user); len(l) != 2 {
			t.Errorf("another owner's DeleteAll left %d of my rows, want 2", len(l))
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := repo.Delete(f.ctx, f.user, tagA); err != nil {
			t.Fatal(err)
		}
		if err := repo.Delete(f.ctx, f.user, tagA); err != nil {
			t.Errorf("deleting a missing row: %v", err)
		}
		if _, err := repo.Get(f.ctx, f.user, tagA); !errors.Is(err, metrics.ErrNotFound) {
			t.Errorf("Get after Delete = %v, want ErrNotFound", err)
		}
		if err := repo.DeleteAll(f.ctx, f.user); err != nil {
			t.Fatal(err)
		}
		if l, err := repo.List(f.ctx, f.user); err != nil || len(l) != 0 {
			t.Errorf("list after DeleteAll = %+v, %v", l, err)
		}
	})
}

func compareIDs(a, b domain.ID) int {
	switch sa, sb := a.String(), b.String(); {
	case sa < sb:
		return -1
	case sa > sb:
		return 1
	}
	return 0
}

// runMasteryInputs covers the reads the projection is computed from.
func runMasteryInputs(t *testing.T, newStore NewStore) {
	f := newMasteryFixture(t, newStore)
	other := newUser(f.ids, "bob")
	if err := f.s.Users().Create(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	otherActor := domain.Actor{Kind: domain.ActorHuman, UserID: other.ID}
	repo := f.s.Mastery()

	tagX, tagY := f.newTag(f.user, "x"), f.newTag(f.user, "y")
	tagOther := f.newTag(other.ID, "x")

	studied := f.newProblem(pkinds[0], problems.StatusLive)
	fresh := f.newProblem(pkinds[1], problems.StatusLive)
	draft := f.newProblem(pkinds[2], problems.StatusDraft)
	gone := f.newProblem(pkinds[0], problems.StatusLive)
	f.tag(f.user, studied.ID, tagX, tagY)
	f.tag(f.user, fresh.ID, tagX)
	f.tag(f.user, draft.ID, tagX)
	f.tag(f.user, gone.ID, tagX)

	theirs, err := f.probs.Create(f.ctx, otherActor, problems.CreateInput{Type: pkinds[0].typ, Prompt: pkinds[0].prompt,
		Payload: []byte(pkinds[0].payload), Status: problems.StatusLive})
	if err != nil {
		t.Fatal(err)
	}
	f.tag(other.ID, theirs.ID, tagOther)

	f.clock.Advance(time.Hour)
	if _, err := f.answer(studied.ID, pkinds[0].right, ptrInt(3)); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	if _, err := f.answer(gone.ID, pkinds[0].right, nil); err != nil {
		t.Fatal(err)
	}
	lastOnX := f.clock.Now()
	f.remove(gone.ID)
	f.clock.Advance(time.Hour)
	theirsAt := f.clock.Now()
	if _, err := f.atts.Answer(f.ctx, otherActor, attempts.AnswerInput{ProblemID: theirs.ID, ProblemVersion: theirs.Version,
		Response: []byte(pkinds[0].right), LatencyMS: 1}); err != nil {
		t.Fatal(err)
	}

	t.Run("tag cards", func(t *testing.T) {
		got, err := repo.TagCards(f.ctx, f.user, nil)
		if err != nil {
			t.Fatal(err)
		}
		// X: studied and fresh (not the draft, not the deleted problem);
		// Y: studied.
		type key struct{ tag, problem domain.ID }
		have := map[key]*review.Card{}
		for _, tc := range got {
			have[key{tc.TagID, tc.ProblemID}] = tc.Card
		}
		if len(got) != 3 || len(have) != 3 {
			t.Fatalf("TagCards = %+v, want 3 entries", got)
		}
		wantCard, _ := f.card(studied.ID)
		for _, tg := range []domain.ID{tagX, tagY} {
			c, ok := have[key{tg, studied.ID}]
			if !ok || c == nil || !reflect.DeepEqual(*c, wantCard) {
				t.Errorf("tag %s studied card = %+v (present %v), want %+v", tg, c, ok, wantCard)
			}
		}
		if c, ok := have[key{tagX, fresh.ID}]; !ok || c != nil {
			t.Errorf("fresh problem = %+v (present %v), want present with no card", c, ok)
		}
		if !slices.IsSortedFunc(got, func(a, b metrics.TagCard) int {
			if c := compareIDs(a.TagID, b.TagID); c != 0 {
				return c
			}
			return compareIDs(a.ProblemID, b.ProblemID)
		}) {
			t.Errorf("TagCards is not ordered by tag then problem: %+v", got)
		}

		only, err := repo.TagCards(f.ctx, f.user, []domain.ID{tagY})
		if err != nil || len(only) != 1 || only[0].TagID != tagY {
			t.Errorf("TagCards(tagY) = %+v, %v", only, err)
		}
		if l, err := repo.TagCards(f.ctx, f.user, []domain.ID{tagOther}); err != nil || len(l) != 0 {
			t.Errorf("TagCards of another owner's tag = %+v, %v; want none", l, err)
		}
	})

	t.Run("a tag that is not active has no cards", func(t *testing.T) {
		if err := f.s.Tags().Retire(f.ctx, f.user, tagY, f.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if l, err := repo.TagCards(f.ctx, f.user, []domain.ID{tagY}); err != nil || len(l) != 0 {
			t.Errorf("TagCards of a retired tag = %+v, %v; want none", l, err)
		}
		if ids, err := repo.ActiveTagIDs(f.ctx, f.user); err != nil || !reflect.DeepEqual(ids, []domain.ID{tagX}) {
			t.Errorf("ActiveTagIDs = %v, %v; want only tag x", ids, err)
		}
	})

	t.Run("problem tags", func(t *testing.T) {
		ids, err := repo.ProblemTagIDs(f.ctx, f.user, studied.ID)
		if err != nil || len(ids) != 2 {
			t.Errorf("ProblemTagIDs = %v, %v; want 2 tags", ids, err)
		}
		if ids, err := repo.ProblemTagIDs(f.ctx, other.ID, studied.ID); err != nil || len(ids) != 0 {
			t.Errorf("another owner's ProblemTagIDs = %v, %v; want none", ids, err)
		}
	})

	t.Run("last attempts", func(t *testing.T) {
		got, err := repo.LastAttempts(f.ctx, f.user, nil)
		if err != nil {
			t.Fatal(err)
		}
		// The deleted problem's attempt no longer counts, so X's latest is the
		// studied problem's, which is also Y's.
		studiedAt := lastOnX.Add(-time.Hour)
		want := map[domain.ID]time.Time{tagX: studiedAt, tagY: studiedAt}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("LastAttempts = %v, want %v", got, want)
		}
		if got, err := repo.LastAttempts(f.ctx, f.user, []domain.ID{tagY}); err != nil || len(got) != 1 {
			t.Errorf("LastAttempts(tagY) = %v, %v", got, err)
		}
		theirsGot, err := repo.LastAttempts(f.ctx, other.ID, nil)
		if err != nil || !reflect.DeepEqual(theirsGot, map[domain.ID]time.Time{tagOther: theirsAt}) {
			t.Errorf("another owner's LastAttempts = %v, %v", theirsGot, err)
		}
	})

	t.Run("an attempt writes the mastery of the problem's tags", func(t *testing.T) {
		x, err := repo.Get(f.ctx, f.user, tagX)
		if err != nil {
			t.Fatal(err)
		}
		// x holds the studied, fresh, and (after removal, not recomputed) rows
		// as of the last attempt on a tagged problem. The last attempt on x
		// was the (since deleted) problem's, so total is as it was then: the
		// studied, fresh, deleted live problems.
		if x.CardsSeen != 2 || x.CardsTotal != 3 || x.Reviews != 2 {
			t.Errorf("tag x = %+v, want 2 of 3 cards seen with 2 reviews", x)
		}
		if x.Recall <= 0 || x.Recall > 1 || x.StabilityMedian <= 0 {
			t.Errorf("tag x recall %v stability %v", x.Recall, x.StabilityMedian)
		}
		y, err := repo.Get(f.ctx, f.user, tagY)
		if err != nil || y.CardsSeen != 1 || y.CardsTotal != 1 || y.Coverage() != 1 {
			t.Errorf("tag y = %+v, %v; want 1 of 1 seen", y, err)
		}
		if !y.LastAttemptAt.Equal(lastOnX.Add(-time.Hour)) {
			t.Errorf("tag y last attempt %v, want %v", y.LastAttemptAt, lastOnX.Add(-time.Hour))
		}
	})
}

// runMasteryProperty is the Done-when for the projection: the incremental
// update after each attempt and the full recompute agree on a randomized
// history. Recall is predicted at one fixed time so rows written at different
// moments are comparable; a bug that skips a tag, reads a stale card or
// miscounts shows up as a difference.
func runMasteryProperty(t *testing.T, newStore NewStore) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			f := newMasteryFixture(t, newStore)
			rng := rand.New(rand.NewSource(seed))

			tagIDs := make([]domain.ID, 5)
			for i := range tagIDs {
				tagIDs[i] = f.newTag(f.user, fmt.Sprintf("tag-%d", i))
			}
			var probs []problems.View
			for i := 0; i < 8; i++ {
				p := f.newProblem(pkinds[i%len(pkinds)], problems.StatusLive)
				var mine []domain.ID
				for _, id := range tagIDs {
					if rng.Intn(3) == 0 {
						mine = append(mine, id)
					}
				}
				f.tag(f.user, p.ID, mine...) // possibly none
				probs = append(probs, p)
			}
			// A tagged draft counts toward nothing.
			draft := f.newProblem(pkinds[0], problems.StatusDraft)
			f.tag(f.user, draft.ID, tagIDs[0], tagIDs[1])

			// Another user's data in the same store must not leak either way.
			other := newUser(f.ids, "bob")
			if err := f.s.Users().Create(f.ctx, other); err != nil {
				t.Fatal(err)
			}
			otherActor := domain.Actor{Kind: domain.ActorHuman, UserID: other.ID}
			otherTag := f.newTag(other.ID, "tag-0")
			op, err := f.probs.Create(f.ctx, otherActor, problems.CreateInput{Type: pkinds[0].typ, Prompt: pkinds[0].prompt,
				Payload: []byte(pkinds[0].payload), Status: problems.StatusLive})
			if err != nil {
				t.Fatal(err)
			}
			f.tag(other.ID, op.ID, otherTag)

			kindOf := func(v problems.View) pkind {
				for _, k := range pkinds {
					if k.typ == v.Type {
						return k
					}
				}
				panic("unknown type")
			}
			var unrated []domain.ID
			for step := 0; step < 80; step++ {
				p := probs[rng.Intn(len(probs))]
				switch n := rng.Intn(9); {
				case n < 7:
					k := kindOf(p)
					response := k.right
					if rng.Intn(3) == 0 {
						response = k.wrong
					}
					var rating *int
					if r := rng.Intn(5); r > 0 {
						rating = ptrInt(r)
					}
					if res, err := f.answer(p.ID, response, rating); err == nil && !res.Grade.Scheduled {
						unrated = append(unrated, res.Attempt.ID)
					}
				default:
					if len(unrated) > 0 {
						i := rng.Intn(len(unrated))
						_, _ = f.atts.Rate(f.ctx, f.actor, unrated[i], 1+rng.Intn(4))
						unrated = append(unrated[:i], unrated[i+1:]...)
					}
				}
				switch rng.Intn(4) {
				case 0:
					f.clock.Advance(time.Duration(1+rng.Intn(120)) * time.Second)
				case 1:
					f.clock.Advance(time.Duration(1+rng.Intn(20)) * time.Minute)
				case 2:
					f.clock.Advance(time.Duration(1+rng.Intn(72)) * time.Hour)
				}
			}

			// The other user answers too, through the same hooks.
			if _, err := f.atts.Answer(f.ctx, otherActor, attempts.AnswerInput{ProblemID: op.ID, ProblemVersion: op.Version,
				Response: []byte(pkinds[0].right), LatencyMS: 1}); err != nil {
				t.Fatal(err)
			}
			otherIncremental := f.rows(other.ID)

			incremental := f.rows(f.user)
			seen := 0
			for _, m := range incremental {
				seen += m.CardsSeen
			}
			if seen == 0 {
				t.Fatal("the history studied no tagged card; the property is vacuous")
			}
			f.rebuild(f.user)
			rebuilt := f.rows(f.user)

			// Tags nothing ever touched have no incremental row; the full
			// recompute writes them too (all zeroes apart from the total).
			// Everything the incremental update wrote must match exactly.
			byTag := map[domain.ID]metrics.TagMastery{}
			for _, m := range rebuilt {
				byTag[m.TagID] = m
			}
			if len(rebuilt) != len(tagIDs) {
				t.Fatalf("rebuild wrote %d rows, want one per active tag (%d)", len(rebuilt), len(tagIDs))
			}
			for _, m := range incremental {
				if want := byTag[m.TagID]; !reflect.DeepEqual(m, want) {
					t.Errorf("tag %s differs\n incremental %+v\n rebuilt     %+v", m.TagID, m, want)
				}
			}
			for _, m := range rebuilt {
				if m.CardsSeen > 0 && !slices.ContainsFunc(incremental, func(x metrics.TagMastery) bool { return x.TagID == m.TagID }) {
					t.Errorf("tag %s has studied cards but the incremental update never wrote it", m.TagID)
				}
			}

			// Rebuilding is idempotent, and does not touch the other owner.
			f.rebuild(f.user)
			if again := f.rows(f.user); !reflect.DeepEqual(rebuilt, again) {
				t.Fatalf("a second rebuild changed the rows\n first  %+v\n second %+v", rebuilt, again)
			}
			if got := f.rows(other.ID); !reflect.DeepEqual(got, otherIncremental) || len(got) != 1 {
				t.Fatalf("rebuilding one user changed another's rows: %+v, want %+v", got, otherIncremental)
			}
			f.rebuild(other.ID)
			if got := f.rows(other.ID); !reflect.DeepEqual(got, otherIncremental) {
				t.Fatalf("other user's rebuild differs from incremental\n got  %+v\n want %+v", got, otherIncremental)
			}
		})
	}
}
