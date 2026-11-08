// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/store"
)

// stepClock is a clock the test advances by hand. It stays on whole
// milliseconds, the finest precision every backend keeps.
type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time          { return c.t }
func (c *stepClock) Advance(d time.Duration) { c.t = c.t.Add(d).Truncate(time.Millisecond) }

func runCards(t *testing.T, newStore NewStore) {
	t.Run("repo", func(t *testing.T) { runCardRepo(t, newStore) })
	t.Run("projection", func(t *testing.T) { runCardProjection(t, newStore) })
	t.Run("rebuild equals incremental", func(t *testing.T) { runCardRebuildProperty(t, newStore) })
	t.Run("builder", func(t *testing.T) { runCardBuilder(t, newStore) })
	t.Run("quiz builder", func(t *testing.T) { runQuizBuilder(t, newStore) })
	t.Run("sessions", func(t *testing.T) { runSessions(t, newStore) })
}

func runCardRepo(t *testing.T, newStore NewStore) {
	ctx := context.Background()
	ids := domain.NewIDGenerator(domain.SystemClock{})

	setup := func(t *testing.T) (store.Store, auth.User, auth.User) {
		t.Helper()
		s, _ := newStore(t)
		a, b := newUser(ids, "a"), newUser(ids, "b")
		for _, u := range []auth.User{a, b} {
			if err := s.Users().Create(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		return s, a, b
	}
	mkProblem := func(t *testing.T, s store.Store, owner auth.User) domain.ID {
		t.Helper()
		act := domain.Actor{Kind: domain.ActorHuman, UserID: owner.ID}
		id := ids.New()
		p := problems.Problem{ID: id, OwnerID: owner.ID, Status: problems.StatusLive, CurrentVersion: 1,
			CreatedBy: act, CreatedAt: t0, UpdatedAt: t0}
		v := problems.Version{ProblemID: id, Version: 1, Type: problems.TypeOpen, Prompt: "q",
			Payload: json.RawMessage(`{"reference_answer":"a"}`), ChangeKind: problems.ChangeMajor, CreatedBy: act, CreatedAt: t0}
		if err := s.Problems().Create(ctx, owner.ID, p, v); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk := func(owner auth.User, problem domain.ID) review.Card {
		return review.Card{UserID: owner.ID, ProblemID: problem, Epoch: 2,
			Card: fsrs.Card{State: fsrs.Review, Step: 1, Stability: 12.625, Difficulty: 5.0625,
				Due: t0.Add(72 * time.Hour), LastReview: t0},
			Reps: 7, Lapses: 2}
	}

	t.Run("put and get round trip", func(t *testing.T) {
		s, a, _ := setup(t)
		pid := mkProblem(t, s, a)
		full := mk(a, pid)
		if err := s.Cards().Put(ctx, a.ID, full); err != nil {
			t.Fatal(err)
		}
		got, err := s.Cards().Get(ctx, a.ID, pid)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, full) {
			t.Errorf("round trip\n got  %+v\n want %+v", got, full)
		}

		// A card that has never been reviewed has no last review.
		fresh := review.Card{UserID: a.ID, ProblemID: pid, Epoch: 1, Card: fsrs.Card{State: fsrs.New, Due: t0}}
		if err := s.Cards().Put(ctx, a.ID, fresh); err != nil {
			t.Fatal(err)
		}
		if got, err = s.Cards().Get(ctx, a.ID, pid); err != nil || !reflect.DeepEqual(got, fresh) {
			t.Errorf("fresh card = %+v, %v; want %+v", got, err, fresh)
		}
	})

	t.Run("put replaces the card of the problem", func(t *testing.T) {
		s, a, _ := setup(t)
		pid := mkProblem(t, s, a)
		first, second := mk(a, pid), mk(a, pid)
		second.Epoch, second.Reps = 3, 1
		for _, c := range []review.Card{first, second} {
			if err := s.Cards().Put(ctx, a.ID, c); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.Cards().List(ctx, a.ID)
		if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], second) {
			t.Fatalf("list = %+v, %v; want only %+v", list, err, second)
		}
	})

	t.Run("get, delete and list", func(t *testing.T) {
		s, a, _ := setup(t)
		p1, p2 := mkProblem(t, s, a), mkProblem(t, s, a)
		if _, err := s.Cards().Get(ctx, a.ID, p1); !errors.Is(err, review.ErrNotFound) {
			t.Fatalf("Get of a missing card = %v, want ErrNotFound", err)
		}
		for _, p := range []domain.ID{p2, p1} {
			if err := s.Cards().Put(ctx, a.ID, mk(a, p)); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.Cards().List(ctx, a.ID)
		if err != nil || len(list) != 2 || list[0].ProblemID != p1 || list[1].ProblemID != p2 {
			t.Fatalf("list = %+v, %v; want ordered by problem id", list, err)
		}
		if err := s.Cards().Delete(ctx, a.ID, p1); err != nil {
			t.Fatal(err)
		}
		if err := s.Cards().Delete(ctx, a.ID, p1); err != nil {
			t.Fatalf("deleting a missing card = %v, want nil", err)
		}
		if list, _ = s.Cards().List(ctx, a.ID); len(list) != 1 {
			t.Fatalf("after delete: %+v", list)
		}
		if err := s.Cards().DeleteAll(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if list, _ = s.Cards().List(ctx, a.ID); len(list) != 0 {
			t.Fatalf("after delete all: %+v", list)
		}
	})

	t.Run("owner scoping", func(t *testing.T) {
		s, a, b := setup(t)
		pa, pb := mkProblem(t, s, a), mkProblem(t, s, b)
		if err := s.Cards().Put(ctx, a.ID, mk(a, pa)); err != nil {
			t.Fatal(err)
		}
		if err := s.Cards().Put(ctx, b.ID, mk(b, pb)); err != nil {
			t.Fatal(err)
		}

		// b cannot see, change or remove a's card; a cannot put a card on b's problem.
		if _, err := s.Cards().Get(ctx, b.ID, pa); !errors.Is(err, review.ErrNotFound) {
			t.Errorf("Get of a foreign card = %v, want ErrNotFound", err)
		}
		if err := s.Cards().Put(ctx, a.ID, mk(a, pb)); !errors.Is(err, review.ErrNotFound) {
			t.Errorf("Put on a foreign problem = %v, want ErrNotFound", err)
		}
		if err := s.Cards().Put(ctx, b.ID, mk(a, pa)); err == nil {
			t.Error("Put of a card whose user is not the owner must fail")
		}
		if err := s.Cards().Delete(ctx, b.ID, pa); err != nil {
			t.Fatal(err)
		}
		if err := s.Cards().DeleteAll(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		if list, _ := s.Cards().List(ctx, b.ID); len(list) != 0 {
			t.Errorf("b's list after b's DeleteAll = %+v", list)
		}
		if _, err := s.Cards().Get(ctx, a.ID, pa); err != nil {
			t.Errorf("a's card was touched by b: %v", err)
		}
		if list, _ := s.Cards().List(ctx, a.ID); len(list) != 1 {
			t.Errorf("a's list = %+v", list)
		}
	})

	t.Run("attempts for a card", func(t *testing.T) {
		s, a, b := setup(t)
		pa := mkProblem(t, s, a)
		other := mkProblem(t, s, a)
		at := func(owner auth.User, problem domain.ID, epoch int, ts time.Time) attempts.Attempt {
			x := attempts.Attempt{ID: ids.New(), UserID: owner.ID, ProblemID: problem, ProblemVersion: 1, CardEpoch: epoch,
				Response: json.RawMessage(`{"text":"x"}`), LatencyMS: 1, AnsweredAt: ts,
				Client: domain.Actor{Kind: domain.ActorHuman, UserID: owner.ID}}
			if err := s.Attempts().Create(ctx, owner.ID, x); err != nil {
				t.Fatal(err)
			}
			return x
		}
		late := at(a, pa, 1, t0.Add(2*time.Hour))
		early := at(a, pa, 1, t0)
		at(a, pa, 2, t0.Add(time.Hour)) // another epoch
		at(a, other, 1, t0)             // another problem
		tie1 := at(a, pa, 1, t0.Add(time.Hour))
		tie2 := at(a, pa, 1, t0.Add(time.Hour))

		got, err := s.Attempts().ForCard(ctx, a.ID, pa, 1)
		if err != nil {
			t.Fatal(err)
		}
		var order []domain.ID
		for _, x := range got {
			order = append(order, x.ID)
		}
		want := []domain.ID{early.ID, tie1.ID, tie2.ID, late.ID}
		if !reflect.DeepEqual(order, want) {
			t.Errorf("order = %v, want answered_at then id: %v", order, want)
		}
		if foreign, err := s.Attempts().ForCard(ctx, b.ID, pa, 1); err != nil || len(foreign) != 0 {
			t.Errorf("another owner sees %d attempts (%v)", len(foreign), err)
		}
	})
}

// cardFixture is the services over one store, wired as the server wires them.
type cardFixture struct {
	t     *testing.T
	ctx   context.Context
	s     store.Store
	clock *stepClock
	cards store.Cards
	probs *problems.Service
	atts  *attempts.Service
	svc   *review.Service
	user  domain.ID
	actor domain.Actor
	ids   *domain.IDGenerator
}

func newCardFixture(t *testing.T, newStore NewStore) *cardFixture {
	t.Helper()
	s, _ := newStore(t)
	clock := &stepClock{t: t0}
	ids := domain.NewIDGenerator(clock)
	u := newUser(ids, "ada")
	if err := s.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	cards := store.DefaultCards()
	probs := problems.NewService(s.Problems(), store.ProblemsEditTx(s, cards.MajorEdit), clock, ids)
	return &cardFixture{t: t, ctx: context.Background(), s: s, clock: clock, cards: cards, probs: probs,
		atts: attempts.NewService(store.AttemptsCardTx(s, cards.CardUpdater), probs, clock, ids),
		svc:  cards.Service(s), user: u.ID, actor: domain.Actor{Kind: domain.ActorHuman, UserID: u.ID}, ids: ids}
}

type pkind struct {
	typ, prompt, payload, right, wrong string
}

var pkinds = []pkind{
	{problems.TypeMCQ, "Pick b", `{"choices":[{"id":"a","text":"A"},{"id":"b","text":"B"}],"correct":["b"]}`,
		`{"selected":["b"]}`, `{"selected":["a"]}`},
	{problems.TypeCloze, "Capital: {{x}}", `{"blanks":[{"id":"x","accepted":["paris"]}]}`,
		`{"answers":{"x":"paris"}}`, `{"answers":{"x":"rome"}}`},
	{problems.TypeOpen, "Explain", `{"reference_answer":"r"}`, `{"text":"t"}`, `{"text":"t"}`},
}

func (f *cardFixture) newProblem(k pkind, status problems.Status) problems.View {
	f.t.Helper()
	v, err := f.probs.Create(f.ctx, f.actor, problems.CreateInput{Type: k.typ, Prompt: k.prompt,
		Payload: json.RawMessage(k.payload), Status: status})
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *cardFixture) answer(id domain.ID, response string, rating *int) (attempts.Result, error) {
	v, err := f.probs.Get(f.ctx, f.user, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.atts.Answer(f.ctx, f.actor, attempts.AnswerInput{ProblemID: id, ProblemVersion: v.Version,
		Response: json.RawMessage(response), LatencyMS: 5, Rating: rating})
}

func (f *cardFixture) edit(id domain.ID, kind problems.ChangeKind) {
	f.t.Helper()
	v, err := f.probs.Get(f.ctx, f.user, id)
	if err != nil {
		f.t.Fatal(err)
	}
	prompt := fmt.Sprintf("%s (v%d)", v.Prompt, v.Version+1)
	if v.Type == problems.TypeCloze {
		prompt = v.Prompt // a cloze prompt must keep its markers
	}
	in := problems.EditInput{ChangeKind: kind, Prompt: &prompt}
	if v.Type == problems.TypeCloze {
		ex := fmt.Sprintf("edit %d", v.Version+1)
		in = problems.EditInput{ChangeKind: kind, Explanation: &ex}
	}
	if _, err := f.probs.Edit(f.ctx, f.actor, id, v.ETag(), in); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cardFixture) archive(id domain.ID) {
	f.t.Helper()
	v, err := f.probs.Get(f.ctx, f.user, id)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.probs.Archive(f.ctx, f.user, id, v.ETag()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cardFixture) remove(id domain.ID) {
	f.t.Helper()
	v, err := f.probs.Get(f.ctx, f.user, id)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.probs.Delete(f.ctx, f.user, id, v.ETag()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cardFixture) list() []review.Card {
	f.t.Helper()
	l, err := f.s.Cards().List(f.ctx, f.user)
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

func (f *cardFixture) card(id domain.ID) (review.Card, bool) {
	f.t.Helper()
	c, err := f.s.Cards().Get(f.ctx, f.user, id)
	if errors.Is(err, review.ErrNotFound) {
		return review.Card{}, false
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return c, true
}

func ptrInt(n int) *int { return &n }

func runCardProjection(t *testing.T, newStore NewStore) {
	t.Run("answers create and advance the card", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[0], problems.StatusLive)
		if _, ok := f.card(p.ID); ok {
			t.Fatal("a card must not exist before the first answer")
		}
		res, err := f.answer(p.ID, pkinds[0].right, nil)
		if err != nil {
			t.Fatal(err)
		}
		c, ok := f.card(p.ID)
		if !ok || c.Reps != 1 || c.Epoch != 1 || c.State != fsrs.Learning || res.NextDue == nil || !res.NextDue.Equal(c.Due) {
			t.Fatalf("card = %+v, %v; next due %v", c, ok, res.NextDue)
		}
		f.clock.Advance(time.Hour)
		if _, err := f.answer(p.ID, pkinds[0].wrong, ptrInt(4)); err != nil {
			t.Fatal(err)
		}
		c, _ = f.card(p.ID)
		if c.Reps != 2 {
			t.Errorf("reps = %d, want 2", c.Reps)
		}
	})

	t.Run("a wrong answer is Again whatever rating was stored", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[0], problems.StatusLive)
		// Easy was sent with a wrong answer; the stored rating is 4 but the
		// effective one is Again, so the card must equal a single Again review.
		if _, err := f.answer(p.ID, pkinds[0].wrong, ptrInt(4)); err != nil {
			t.Fatal(err)
		}
		c, _ := f.card(p.ID)
		want := fsrs.Next(fsrs.Card{}, fsrs.Again, f.clock.Now())
		if !reflect.DeepEqual(c.Card, want) {
			t.Errorf("card %+v, want a single Again review %+v", c.Card, want)
		}
	})

	t.Run("an unrated open attempt waits; a late rating schedules it", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[2], problems.StatusLive)
		res, err := f.answer(p.ID, pkinds[2].right, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.card(p.ID); ok {
			t.Fatal("an unrated open attempt must not create a card")
		}
		f.clock.Advance(time.Minute)
		if _, err := f.atts.Rate(f.ctx, f.actor, res.Attempt.ID, 4); err != nil {
			t.Fatal(err)
		}
		c, ok := f.card(p.ID)
		// The card is folded at the attempt's answered_at, not at rating time.
		want := fsrs.Next(fsrs.Card{}, fsrs.Easy, res.Attempt.AnsweredAt)
		if !ok || c.Reps != 1 || !reflect.DeepEqual(c.Card, want) {
			t.Fatalf("card = %+v, %v; want %+v", c, ok, want)
		}
	})

	t.Run("a late rating on a right answer refolds the card", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[0], problems.StatusLive)
		res, err := f.answer(p.ID, pkinds[0].right, nil) // default Good
		if err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(time.Minute)
		if _, err := f.atts.Rate(f.ctx, f.actor, res.Attempt.ID, 4); err != nil {
			t.Fatal(err)
		}
		c, _ := f.card(p.ID)
		if want := fsrs.Next(fsrs.Card{}, fsrs.Easy, res.Attempt.AnsweredAt); !reflect.DeepEqual(c.Card, want) {
			t.Errorf("card %+v, want %+v", c.Card, want)
		}
	})

	t.Run("a minor edit keeps the card, a major edit starts a new epoch", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[0], problems.StatusLive)
		if _, err := f.answer(p.ID, pkinds[0].right, nil); err != nil {
			t.Fatal(err)
		}
		before, _ := f.card(p.ID)

		f.edit(p.ID, problems.ChangeMinor)
		if c, ok := f.card(p.ID); !ok || !reflect.DeepEqual(c, before) {
			t.Fatalf("a minor edit changed the card: %+v, %v", c, ok)
		}

		f.edit(p.ID, problems.ChangeMajor)
		if _, ok := f.card(p.ID); ok {
			t.Fatal("a major edit must drop the old epoch's card")
		}
		f.clock.Advance(time.Hour)
		if _, err := f.answer(p.ID, pkinds[0].right, nil); err != nil {
			t.Fatal(err)
		}
		c, ok := f.card(p.ID)
		if !ok || c.Epoch != 2 || c.Reps != 1 {
			t.Fatalf("new epoch card = %+v, %v; want epoch 2 with one rep", c, ok)
		}
		// The old attempt stayed attached to epoch 1.
		old, err := f.s.Attempts().ForCard(f.ctx, f.user, p.ID, 1)
		if err != nil || len(old) != 1 {
			t.Fatalf("epoch 1 attempts = %d, %v; want the original one", len(old), err)
		}
	})

	t.Run("a late rating on an old epoch does not resurrect its card", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[2], problems.StatusLive)
		res, err := f.answer(p.ID, pkinds[2].right, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.edit(p.ID, problems.ChangeMajor)
		f.clock.Advance(time.Minute)
		if _, err := f.atts.Rate(f.ctx, f.actor, res.Attempt.ID, 3); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.card(p.ID); ok {
			t.Error("rating an epoch-1 attempt created a card for the current epoch")
		}
	})

	t.Run("draft problems have no cards and rebuild skips archived ones", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		live := f.newProblem(pkinds[0], problems.StatusLive)
		if _, err := f.answer(live.ID, pkinds[0].right, nil); err != nil {
			t.Fatal(err)
		}
		v, _ := f.probs.Get(f.ctx, f.user, live.ID)
		if _, err := f.probs.Archive(f.ctx, f.user, live.ID, v.ETag()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.RebuildUser(f.ctx, f.user); err != nil {
			t.Fatal(err)
		}
		if l := f.list(); len(l) != 0 {
			t.Errorf("rebuild kept cards of an archived problem: %+v", l)
		}
	})

	t.Run("archiving or deleting a problem drops its card", func(t *testing.T) {
		for name, retire := range map[string]func(f *cardFixture, id domain.ID){
			"archive": (*cardFixture).archive, "delete": (*cardFixture).remove,
		} {
			t.Run(name, func(t *testing.T) {
				f := newCardFixture(t, newStore)
				p := f.newProblem(pkinds[0], problems.StatusLive)
				keep := f.newProblem(pkinds[0], problems.StatusLive)
				for _, id := range []domain.ID{p.ID, keep.ID} {
					if _, err := f.answer(id, pkinds[0].right, nil); err != nil {
						t.Fatal(err)
					}
				}
				retire(f, p.ID)
				if _, ok := f.card(p.ID); ok {
					t.Error("the retired problem still has a card")
				}
				if _, ok := f.card(keep.ID); !ok {
					t.Error("the other problem lost its card")
				}
				before := f.list()
				if _, err := f.svc.RebuildUser(f.ctx, f.user); err != nil {
					t.Fatal(err)
				}
				if after := f.list(); !reflect.DeepEqual(before, after) {
					t.Errorf("rebuild differs after retiring: %+v vs %+v", before, after)
				}
			})
		}
	})

	t.Run("a late rating on an archived problem does not create a card", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[2], problems.StatusLive)
		res, err := f.answer(p.ID, pkinds[2].right, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.archive(p.ID)
		f.clock.Advance(time.Minute)
		if _, err := f.atts.Rate(f.ctx, f.actor, res.Attempt.ID, 4); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.card(p.ID); ok {
			t.Error("rating an attempt of an archived problem created a card")
		}
		if got, err := f.s.Attempts().Get(f.ctx, f.user, res.Attempt.ID); err != nil || got.Rating == nil || *got.Rating != 4 {
			t.Errorf("the rating itself must still be stored: %+v, %v", got, err)
		}
	})

	t.Run("an empty fold removes a stale card", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[2], problems.StatusLive)
		if _, err := f.answer(p.ID, pkinds[2].right, nil); err != nil { // unrated: nothing scheduled
			t.Fatal(err)
		}
		stale := review.Card{UserID: f.user, ProblemID: p.ID, Epoch: 1,
			Card: fsrs.Card{State: fsrs.Review, Stability: 3, Difficulty: 5, Due: t0, LastReview: t0}, Reps: 1}
		if err := f.s.Cards().Put(f.ctx, f.user, stale); err != nil {
			t.Fatal(err)
		}
		err := f.s.WithTx(f.ctx, func(tx store.Store) error {
			c, err := f.cards.Projector(tx).Recompute(f.ctx, f.user, p.ID, 1, nil)
			if c != nil {
				t.Errorf("recompute returned a card %+v for an empty fold", c)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.card(p.ID); ok {
			t.Error("the stale card survived an empty fold")
		}
	})

	t.Run("recompute with a new retention changes the schedule", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[0], problems.StatusLive)
		for range 4 {
			if _, err := f.answer(p.ID, pkinds[0].right, nil); err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(36 * time.Hour)
		}
		before, _ := f.card(p.ID)
		if err := f.svc.RecomputeCards(f.ctx, f.user, 0.80); err != nil {
			t.Fatal(err)
		}
		lo, _ := f.card(p.ID)
		if !lo.Due.After(before.Due) || lo.Reps != before.Reps || lo.Stability != before.Stability {
			t.Errorf("0.80 retention should only push the due date out: before %+v after %+v", before, lo)
		}
		if _, err := f.svc.RebuildUser(f.ctx, f.user); err != nil { // back to the stored (default) retention
			t.Fatal(err)
		}
		if again, _ := f.card(p.ID); !reflect.DeepEqual(again, before) {
			t.Errorf("rebuild under the stored retention = %+v, want %+v", again, before)
		}
	})

	t.Run("a major edit made through the seed transaction starts a new epoch", func(t *testing.T) {
		f := newCardFixture(t, newStore)
		p := f.newProblem(pkinds[0], problems.StatusLive)
		if _, err := f.answer(p.ID, pkinds[0].right, nil); err != nil {
			t.Fatal(err)
		}
		ids := domain.NewIDGenerator(f.clock)
		err := store.SeedTx(f.s, f.clock, ids, store.NewMastery(f.cards, f.clock)).InTx(f.ctx, func(sv seed.Services) error {
			v, err := sv.Problems.Get(f.ctx, f.user, p.ID)
			if err != nil {
				return err
			}
			prompt := "Pick b, differently"
			_, err = sv.Problems.Edit(f.ctx, f.actor, p.ID, v.ETag(), problems.EditInput{ChangeKind: problems.ChangeMajor, Prompt: &prompt})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.card(p.ID); ok {
			t.Error("a seeded major edit did not reach the card projection")
		}
	})
}

// runCardRebuildProperty is the M4.3 property: after any history of answers,
// late ratings and edits, dropping every card and rebuilding from the attempt
// log gives exactly the rows the incremental updates produced.
func runCardRebuildProperty(t *testing.T, newStore NewStore) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			f := newCardFixture(t, newStore)
			rng := rand.New(rand.NewSource(seed))

			var probs []problems.View
			for i := range 4 {
				probs = append(probs, f.newProblem(pkinds[i%len(pkinds)], problems.StatusLive))
			}
			kindOf := func(v problems.View) pkind {
				for _, k := range pkinds {
					if k.typ == v.Type {
						return k
					}
				}
				panic("unknown type")
			}
			var unrated []domain.ID
			majors := 0

			for range 80 {
				p := probs[rng.Intn(len(probs))]
				switch n := rng.Intn(13); {
				case n < 7: // answer
					k := kindOf(p)
					response := k.right
					if rng.Intn(3) == 0 {
						response = k.wrong
					}
					var rating *int
					if r := rng.Intn(5); r > 0 {
						rating = ptrInt(r) // 1-4; an illegal one for a right answer is refused
					}
					if res, err := f.answer(p.ID, response, rating); err == nil && !res.Grade.Scheduled {
						unrated = append(unrated, res.Attempt.ID)
					}
				case n < 9: // late rating, sometimes outside the window
					if len(unrated) > 0 {
						i := rng.Intn(len(unrated))
						_, _ = f.atts.Rate(f.ctx, f.actor, unrated[i], 1+rng.Intn(4))
						unrated = append(unrated[:i], unrated[i+1:]...)
					}
				case n < 10:
					f.edit(p.ID, problems.ChangeMinor)
				case n < 11:
					f.edit(p.ID, problems.ChangeMajor)
					majors++
				default: // the problem leaves review; a new one takes its place
					i := 0
					for probs[i].ID != p.ID {
						i++
					}
					if rng.Intn(2) == 0 {
						f.archive(p.ID)
					} else {
						f.remove(p.ID)
					}
					probs[i] = f.newProblem(pkinds[rng.Intn(len(pkinds))], problems.StatusLive)
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

			incremental := f.list()
			if len(incremental) == 0 {
				t.Fatal("the history produced no cards; the property is vacuous")
			}
			if _, err := f.svc.RebuildUser(f.ctx, f.user); err != nil {
				t.Fatal(err)
			}
			rebuilt := f.list()
			if !reflect.DeepEqual(incremental, rebuilt) {
				t.Fatalf("rebuild differs from the incremental projection (%d major edits)\n incremental %+v\n rebuilt     %+v",
					majors, incremental, rebuilt)
			}
			// Rebuilding is idempotent.
			if _, err := f.svc.RebuildUser(f.ctx, f.user); err != nil {
				t.Fatal(err)
			}
			if again := f.list(); !reflect.DeepEqual(rebuilt, again) {
				t.Fatalf("a second rebuild changed the rows\n first  %+v\n second %+v", rebuilt, again)
			}
		})
	}
}
