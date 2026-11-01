// SPDX-License-Identifier: Apache-2.0

package seed_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/problems"
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

// TestStarterPackSession seeds the pack and answers every stored problem the
// way a review session would, up to what exists on main: it grades a right and
// (for auto-graded types) a wrong response with the grading package, resolves
// the rating and schedules a new card with fsrs.Next. The review-session and
// attempt services are not on main yet, so nothing is persisted as attempts.
func TestStarterPackSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := loadStarter(t)
	rep, err := seedrun.ApplyFile(ctx, e.st, e.owner, f)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tags.Created != len(f.Tags) || rep.Problems.Created != len(f.Problems) || rep.Quizzes.Created != 1 {
		t.Fatalf("report = %+v", rep)
	}

	svc := problems.NewService(e.st.Problems(), store.ProblemsTx(e.st), domain.SystemClock{}, domain.NewIDGenerator(domain.SystemClock{}))
	page, err := svc.List(ctx, e.owner, problems.ListQuery{Status: string(problems.StatusLive), Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(f.Problems) {
		t.Fatalf("%d live problems stored, want %d", len(page.Items), len(f.Problems))
	}

	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	for _, v := range page.Items {
		correct := false
		switch v.Type {
		case problems.TypeMCQ:
			var p problems.MCQPayload
			mustJSON(t, v.Payload, &p)
			if !grading.GradeMCQ(p.Correct, p.Correct, p.Multiple).Correct {
				t.Errorf("%q: the correct choice does not grade as correct", v.Prompt)
			}
			var wrong []string
			for _, c := range p.Choices {
				if !slices.Contains(p.Correct, c.ID) {
					wrong = append(wrong, c.ID)
					break
				}
			}
			if grading.GradeMCQ(p.Correct, wrong, p.Multiple).Correct {
				t.Errorf("%q: a wrong choice grades as correct", v.Prompt)
			}
			correct = true
		case problems.TypeCloze:
			var p problems.ClozePayload
			mustJSON(t, v.Payload, &p)
			blanks := make([]grading.ClozeBlank, len(p.Blanks))
			right := map[string]string{}
			wrong := map[string]string{}
			for i, b := range p.Blanks {
				blanks[i] = grading.ClozeBlank{ID: b.ID, Accepted: b.Accepted, CaseSensitive: b.CaseSensitive, MaxEdits: b.MaxEdits}
				right[b.ID] = b.Accepted[0]
				wrong[b.ID] = "zzzz qqqq"
			}
			if !grading.GradeCloze(blanks, right).AllCorrect() {
				t.Errorf("%q: the accepted answers do not grade as correct", v.Prompt)
			}
			if grading.GradeCloze(blanks, wrong).AllCorrect() {
				t.Errorf("%q: nonsense grades as correct", v.Prompt)
			}
			correct = true
		case problems.TypeOpen:
			var p problems.OpenPayload
			mustJSON(t, v.Payload, &p)
			if strings.TrimSpace(p.ReferenceAnswer) == "" {
				t.Errorf("%q: no reference answer", v.Prompt)
			}
		}
		good := 3
		rating, err := grading.ResolveRating(v.Type, correct, &good)
		if err != nil {
			t.Fatalf("%q: %v", v.Prompt, err)
		}
		card := fsrs.Next(fsrs.Card{}, fsrs.Rating(rating), now)
		if card.Stability <= 0 || !card.Due.After(now) {
			t.Errorf("%q: card after Good = %+v", v.Prompt, card)
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
