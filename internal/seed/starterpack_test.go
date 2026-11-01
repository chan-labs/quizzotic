// SPDX-License-Identifier: Apache-2.0

package seed_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/seedrun"
	"github.com/chan-labs/quizzotic/internal/store"
)

const starterSpec = seed.BuiltinPrefix + "fsrs-basics"

func loadStarter(t *testing.T) *seed.File {
	t.Helper()
	f, err := seed.Load(starterSpec)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestStarterPackShape checks the pack against the task: about 40 problems,
// all three types, the four tags, one quiz, and valid payloads per type.
func TestStarterPackShape(t *testing.T) {
	f := loadStarter(t)
	if n := len(f.Problems); n < 38 || n > 42 {
		t.Errorf("%d problems, want about 40", n)
	}
	byType := map[string]int{}
	used := map[string]bool{}
	prompts := map[string]bool{}
	for _, p := range f.Problems {
		byType[p.Type]++
		for _, tg := range p.Tags {
			used[tg] = true
		}
		if prompts[p.Prompt] {
			t.Errorf("%s: duplicate prompt", p.Key)
		}
		prompts[p.Prompt] = true
		if p.Status == "draft" {
			t.Errorf("%s: starter problems must be live", p.Key)
		}
		if len(p.Tags) == 0 {
			t.Errorf("%s: no tags", p.Key)
		}
		raw, err := json.Marshal(p.Payload)
		if err != nil {
			t.Fatalf("%s: %v", p.Key, err)
		}
		if errs := problems.ValidatePayload(p.Type, p.Prompt, raw); len(errs) > 0 {
			t.Errorf("%s: invalid payload: %v", p.Key, errs)
		}
	}
	for _, typ := range []string{problems.TypeMCQ, problems.TypeCloze, problems.TypeOpen} {
		if byType[typ] < 5 {
			t.Errorf("only %d %s problems", byType[typ], typ)
		}
	}
	slugs := map[string]string{}
	for _, tg := range f.Tags {
		slugs[tg.Slug] = tg.Key
	}
	for _, s := range []string{"spaced-repetition", "fsrs", "memory-models", "quizzotic-usage"} {
		k, ok := slugs[s]
		if !ok {
			t.Errorf("tag %q missing", s)
		} else if !used[k] {
			t.Errorf("tag %q is never used", s)
		}
	}
	if len(f.Quizzes) != 1 || len(f.Quizzes[0].Problems) < 5 {
		t.Errorf("quizzes = %+v, want one quiz with several problems", f.Quizzes)
	}
}

// TestStarterPackSession seeds the pack into a real SQLite store and completes
// a real review session: the session service builds and freezes the list (new
// cards only, capped by new_per_day), every served problem is answered through
// the attempts service (graded answers for mcq and cloze, a chosen rating for
// open) and the session is completed. It then checks the persisted attempts,
// cards and the daily new-card limit.
func TestStarterPackSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := loadStarter(t)
	if _, err := seedrun.ApplyFile(ctx, e.st, e.owner, f); err != nil {
		t.Fatal(err)
	}

	clock := domain.NewFakeClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	cards := store.DefaultCards()
	probs := problems.NewService(e.st.Problems(), store.ProblemsEditTx(e.st, cards.MajorEdit), clock, ids)
	answers := attempts.NewService(store.AttemptsCardTx(e.st, cards.CardUpdater), probs, clock, ids, attempts.WithReader(e.st.Attempts()))
	sessions := review.NewSessionService(cards.SessionTx(e.st), probs, nil, clock, ids)
	builder := cards.Builder(e.st)
	actor := domain.Actor{Kind: domain.ActorHuman, UserID: e.owner, ClientID: "starter-e2e"}

	today, err := builder.Today(ctx, e.owner, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantNew := min(today.NewPerDay, len(f.Problems))
	if today.NewPerDay <= 0 || wantNew != today.NewPerDay {
		t.Fatalf("new_per_day = %d with %d problems; the pack must be larger than the limit", today.NewPerDay, len(f.Problems))
	}

	sess, err := sessions.Create(ctx, e.owner, review.CreateSessionInput{Kind: review.SessionReview})
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Items) != wantNew {
		t.Fatalf("session has %d items, want new_per_day = %d", len(sess.Items), wantNew)
	}
	for _, it := range sess.Items {
		if it.Kind != review.ItemNew {
			t.Errorf("item %d is %s, want new", it.Position, it.Kind)
		}
	}

	// Answers come from the seed file, keyed by prompt: the prompt view the
	// session serves hides them.
	byPrompt := map[string]seed.Problem{}
	for _, p := range f.Problems {
		byPrompt[p.Prompt] = p
	}
	openRatings := []int{2, 3, 4}
	served := map[domain.ID]string{}
	types := map[string]int{}
	for n := 0; ; n++ {
		next, err := sessions.Next(ctx, e.owner, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if next.Item == nil {
			if next.Remaining != 0 {
				t.Fatalf("no item but %d remaining", next.Remaining)
			}
			break
		}
		if n >= len(sess.Items) {
			t.Fatalf("served more than the %d frozen items", len(sess.Items))
		}
		pv := next.Item.Problem
		sp, ok := byPrompt[pv.Prompt]
		if !ok {
			t.Fatalf("served an unknown prompt %q", pv.Prompt)
		}
		payload, err := json.Marshal(sp.Payload)
		if err != nil {
			t.Fatal(err)
		}
		in := attempts.AnswerInput{ProblemID: pv.ID, ProblemVersion: pv.Version, LatencyMS: 1500, SessionID: sess.ID.String()}
		switch pv.Type {
		case problems.TypeMCQ:
			var p problems.MCQPayload
			mustJSON(t, payload, &p)
			in.Response = mustMarshal(t, map[string]any{"selected": p.Correct})
		case problems.TypeCloze:
			var p problems.ClozePayload
			mustJSON(t, payload, &p)
			ans := map[string]string{}
			for _, b := range p.Blanks {
				ans[b.ID] = b.Accepted[0]
			}
			in.Response = mustMarshal(t, map[string]any{"answers": ans})
		case problems.TypeOpen:
			var p problems.OpenPayload
			mustJSON(t, payload, &p)
			in.Response = mustMarshal(t, map[string]any{"text": p.ReferenceAnswer})
			r := openRatings[types[problems.TypeOpen]%len(openRatings)]
			in.Rating = &r
		}
		types[pv.Type]++
		res, err := answers.Answer(ctx, actor, in)
		if err != nil {
			t.Fatalf("%q: %v", pv.Prompt, err)
		}
		if pv.Type != problems.TypeOpen && (res.Grade.Correct == nil || !*res.Grade.Correct) {
			t.Errorf("%q: the seeded answer was graded wrong: %+v", pv.Prompt, res.Grade)
		}
		if !res.Grade.Scheduled || res.NextDue == nil || !res.NextDue.After(clock.Now()) {
			t.Errorf("%q: not scheduled into the future: %+v", pv.Prompt, res)
		}
		served[pv.ID] = pv.Prompt
	}
	if len(served) != wantNew {
		t.Fatalf("answered %d distinct problems, want %d", len(served), wantNew)
	}
	for _, typ := range []string{problems.TypeMCQ, problems.TypeCloze, problems.TypeOpen} {
		if types[typ] == 0 {
			t.Errorf("the session served no %s problem", typ)
		}
	}

	done, err := sessions.Complete(ctx, e.owner, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.CompletedAt == nil || done.Answered != wantNew {
		t.Errorf("completed session = %+v", done)
	}
	if _, err := sessions.Next(ctx, e.owner, sess.ID); err == nil {
		t.Error("a completed session still serves items")
	}

	// Attempts: one per served problem, all in the session, as answered.
	atts, err := e.st.Attempts().List(ctx, e.owner, attempts.ListFilter{SessionID: sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != wantNew {
		t.Fatalf("%d attempts in the session, want %d", len(atts), wantNew)
	}
	snapshot := map[domain.ID]attempts.Attempt{}
	for _, a := range atts {
		if a.SessionID != sess.ID || a.UserID != e.owner || a.Score == nil || a.CardEpoch != 1 {
			t.Errorf("attempt = %+v", a)
		}
		if _, ok := served[a.ProblemID]; !ok {
			t.Errorf("attempt for unserved problem %s", a.ProblemID)
		}
		if (a.AutoCorrect == nil) != (a.Rating != nil) {
			t.Errorf("attempt %s: auto_correct %v with rating %v (only open attempts carry the chosen rating here)", a.ID, a.AutoCorrect, a.Rating)
		}
		snapshot[a.ID] = a
	}

	// Cards: one new card per answered problem, due in the future.
	cs, err := e.st.Cards().Query(ctx, e.owner, review.CardFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != wantNew {
		t.Fatalf("%d cards, want %d", len(cs), wantNew)
	}
	for _, c := range cs {
		if _, ok := served[c.ProblemID]; !ok || c.Reps != 1 || !c.Due.After(clock.Now()) || c.Stability <= 0 {
			t.Errorf("card = %+v", c)
		}
	}

	// The daily new-card limit now holds: nothing new is left, so a second
	// session is empty and the stored attempts are untouched (append-only).
	again, err := sessions.Create(ctx, e.owner, review.CreateSessionInput{Kind: review.SessionReview})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != 0 {
		t.Errorf("second session has %d items after new_per_day was used up", len(again.Items))
	}
	if td, err := builder.Today(ctx, e.owner, clock.Now()); err != nil || td.NewLeft != 0 || td.NewDone != wantNew {
		t.Errorf("today = %+v, %v", td, err)
	}
	after, err := e.st.Attempts().List(ctx, e.owner, attempts.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != wantNew {
		t.Errorf("%d attempts after the second session, want %d", len(after), wantNew)
	}
	for _, a := range after {
		if !reflect.DeepEqual(a, snapshot[a.ID]) {
			t.Errorf("attempt %s changed after it was stored", a.ID)
		}
	}
}

// TestStarterPackStored checks tags, the quiz and idempotence in the store.
func TestStarterPackStored(t *testing.T) {
	e := newEnv(t)
	f := loadStarter(t)
	apply := func() seed.Report {
		rep, err := seedrun.ApplyFile(context.Background(), e.st, e.owner, f)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	apply()
	before := e.rows()

	if before["problems"] != len(f.Problems) || before["tags"] != len(f.Tags) || before["quizzes"] != 1 ||
		before["quiz_items"] != len(f.Quizzes[0].Problems) {
		t.Errorf("rows = %v", before)
	}
	rows, err := e.st.DB().Query(`SELECT t.slug, count(*) FROM problem_tags pt JOIN tags t ON t.id = pt.tag_id GROUP BY t.slug`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			t.Fatal(err)
		}
		got[s] = n
	}
	for _, s := range []string{"spaced-repetition", "fsrs", "memory-models", "quizzotic-usage"} {
		if got[s] == 0 {
			t.Errorf("no problem is tagged %q", s)
		}
	}

	rep := apply()
	if rep.Tags.Created+rep.Problems.Created+rep.Quizzes.Created+rep.Problems.Updated != 0 ||
		rep.Problems.Unchanged != len(f.Problems) {
		t.Errorf("re-seed report = %+v", rep)
	}
	for k, v := range e.rows() {
		if before[k] != v {
			t.Errorf("%s: %d rows -> %d after re-seed", k, before[k], v)
		}
	}
}

func mustJSON(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
