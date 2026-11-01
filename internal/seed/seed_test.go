// SPDX-License-Identifier: Apache-2.0

package seed_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/seedrun"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

const pack = `
version: 1
tags:
  - key: t.one
    slug: one
    name: One
    description: first
problems:
  - key: p.open
    type: open
    prompt: What is one?
    payload: {reference_answer: the number 1}
    tags: [t.one]
  - key: p.draft
    type: open
    status: draft
    prompt: A draft question?
    payload: {reference_answer: x}
  - key: p.mcq
    type: mcq
    prompt: Pick a
    payload:
      choices: [{id: a, text: A}, {id: b, text: B}]
      correct: [a]
quizzes:
  - key: q.all
    title: All of them
    problems: [p.mcq, p.open]
`

type env struct {
	t     *testing.T
	st    *sqlite.Store
	owner domain.ID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, owner: e2user(t, st, "owner")}
}

func e2user(t *testing.T, st store.Store, name string) domain.ID {
	t.Helper()
	id := domain.NewIDGenerator(domain.SystemClock{}).New()
	err := st.Users().Create(context.Background(), auth.User{ID: id, DisplayName: name, Email: name + "@example.com", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func parse(t *testing.T, src string) *seed.File {
	t.Helper()
	f, err := seed.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (e *env) apply(src string) seed.Report {
	e.t.Helper()
	rep, err := seedrun.ApplyFile(context.Background(), e.st, e.owner, parse(e.t, src))
	if err != nil {
		e.t.Fatal(err)
	}
	return rep
}

// rows counts every row and version the seeding touches, so "no new rows or
// versions" is checkable.
func (e *env) rows() map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"tags", "problems", "problem_versions", "problem_tags", "quizzes", "quiz_items", "seed_keys"} {
		var n int
		if err := e.st.DB().QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil {
			e.t.Fatalf("%s: %v", tbl, err)
		}
		out[tbl] = n
	}
	return out
}

func (e *env) problemByPrompt(prompt string) problems.View {
	e.t.Helper()
	svc := problems.NewService(e.st.Problems(), store.ProblemsTx(e.st), domain.SystemClock{}, domain.NewIDGenerator(domain.SystemClock{}))
	page, err := svc.List(context.Background(), e.owner, problems.ListQuery{Q: prompt})
	if err != nil || len(page.Items) != 1 {
		e.t.Fatalf("list %q: %v %d", prompt, err, len(page.Items))
	}
	return page.Items[0]
}

func TestApplyCreatesEverything(t *testing.T) {
	e := newEnv(t)
	rep := e.apply(pack)
	if rep.Tags.Created != 1 || rep.Problems.Created != 3 || rep.Quizzes.Created != 1 {
		t.Fatalf("report = %+v", rep)
	}
	open := e.problemByPrompt("What is one?")
	if open.Status != problems.StatusLive || open.CreatedBy.Kind != domain.ActorSystem ||
		open.CreatedBy.ClientID != seed.ClientID || open.CreatedBy.UserID != e.owner.String() {
		t.Errorf("open = %+v", open)
	}
	if d := e.problemByPrompt("A draft question?"); d.Status != problems.StatusDraft {
		t.Errorf("draft status = %s", d.Status)
	}
	var n int
	if err := e.st.DB().QueryRow(`SELECT count(*) FROM problem_tags`).Scan(&n); err != nil || n != 1 {
		t.Errorf("problem_tags = %d, %v", n, err)
	}
	// The quiz lists its problems in file order.
	var first string
	if err := e.st.DB().QueryRow(`SELECT problem_id FROM quiz_items WHERE position = 0`).Scan(&first); err != nil ||
		first != e.problemByPrompt("Pick a").ID.String() {
		t.Errorf("first item = %q, %v", first, err)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.apply(pack)
	before := e.rows()
	rep := e.apply(pack)
	if rep.Tags.Unchanged != 1 || rep.Problems.Unchanged != 3 || rep.Quizzes.Unchanged != 1 ||
		rep.Tags.Created+rep.Problems.Created+rep.Quizzes.Created+rep.Problems.Updated != 0 {
		t.Fatalf("second report = %+v", rep)
	}
	after := e.rows()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s: %d rows -> %d", k, v, after[k])
		}
	}
	if before["problem_versions"] != 3 {
		t.Errorf("versions = %d, want 3", before["problem_versions"])
	}
}

func TestChangedItemUpdatesInPlace(t *testing.T) {
	e := newEnv(t)
	e.apply(pack)
	id := e.problemByPrompt("What is one?").ID

	changed := strings.Replace(pack, "the number 1", "the number one", 1)
	changed = strings.Replace(changed, "status: draft", "status: live", 1) // publishes the draft
	rep := e.apply(changed)
	if rep.Problems.Updated != 2 || rep.Problems.Created != 0 {
		t.Fatalf("report = %+v", rep)
	}
	got := e.problemByPrompt("What is one?")
	if got.ID != id || got.Version != 2 || !strings.Contains(string(got.Payload), "the number one") {
		t.Errorf("got %+v", got)
	}
	if d := e.problemByPrompt("A draft question?"); d.Status != problems.StatusLive {
		t.Errorf("draft not published: %s", d.Status)
	}
	if r := e.rows(); r["problems"] != 3 || r["problem_versions"] != 4 {
		t.Errorf("rows = %v", r)
	}
}

func TestUnchangedFileLeavesApiEditsAlone(t *testing.T) {
	e := newEnv(t)
	e.apply(pack)
	ctx := context.Background()
	p := e.problemByPrompt("What is one?")
	svc := problems.NewService(e.st.Problems(), store.ProblemsTx(e.st), domain.SystemClock{}, domain.NewIDGenerator(domain.SystemClock{}))
	if _, err := svc.Archive(ctx, e.owner, p.ID, p.ETag()); err != nil {
		t.Fatal(err)
	}
	before := e.rows()
	e.apply(pack)
	if got := e.problemByPrompt("What is one?"); got.Status != problems.StatusArchived {
		t.Errorf("status = %s; seeding must not resurrect it", got.Status)
	}
	for k, v := range e.rows() {
		if before[k] != v {
			t.Errorf("%s: %d -> %d", k, before[k], v)
		}
	}
}

func TestDeletedItemStaysDeleted(t *testing.T) {
	e := newEnv(t)
	e.apply(pack)
	ctx := context.Background()
	p := e.problemByPrompt("Pick a")
	svc := problems.NewService(e.st.Problems(), store.ProblemsTx(e.st), domain.SystemClock{}, domain.NewIDGenerator(domain.SystemClock{}))
	if err := svc.Delete(ctx, e.owner, p.ID, p.ETag()); err != nil {
		t.Fatal(err)
	}
	// The pack changes, so the deleted problem's item is visited again.
	changed := strings.Replace(pack, "Pick a", "Pick a letter", 1)
	rep := e.apply(changed)
	if rep.Problems.Skipped != 1 || rep.Problems.Created != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if r := e.rows(); r["problems"] != 3 {
		t.Errorf("problems = %d", r["problems"])
	}
}

func TestExistingTagIsAdopted(t *testing.T) {
	e := newEnv(t)
	// Another file seeds the same slug under another key; nothing duplicates.
	e.apply("version: 1\ntags:\n  - {key: a, slug: one, name: One}\n")
	rep := e.apply("version: 1\ntags:\n  - {key: b, slug: one, name: Uno}\n")
	if rep.Tags.Created != 0 || rep.Tags.Unchanged != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if r := e.rows(); r["tags"] != 1 {
		t.Errorf("tags = %d", r["tags"])
	}
}

func TestOwnersAreIndependent(t *testing.T) {
	e := newEnv(t)
	e.apply(pack)
	other := e2user(t, e.st, "other")
	rep, err := seedrun.ApplyFile(context.Background(), e.st, other, parse(t, pack))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Problems.Created != 3 || rep.Tags.Created != 1 {
		t.Fatalf("second owner report = %+v", rep)
	}
	if r := e.rows(); r["problems"] != 6 || r["tags"] != 2 {
		t.Errorf("rows = %v", r)
	}
}

func TestInvalidContentIsRejectedAtomically(t *testing.T) {
	e := newEnv(t)
	bad := strings.Replace(pack, "quizzes:", `  - key: p.bad
    type: mcq
    prompt: no choices
    payload: {}
quizzes:`, 1)
	_, err := seedrun.ApplyFile(context.Background(), e.st, e.owner, parse(t, bad))
	if err == nil || !strings.Contains(err.Error(), "p.bad") || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("err = %v", err)
	}
	for k, v := range e.rows() {
		if v != 0 {
			t.Errorf("%s has %d rows after a failed run", k, v)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"empty":         {"", "empty"},
		"version":       {"version: 2\n", "version: must be 1"},
		"unknown field": {"version: 1\nbogus: 1\n", "bogus"},
		"missing key":   {"version: 1\nproblems:\n  - {type: open, prompt: x}\n", "problems[0].key: required"},
		"bad key":       {"version: 1\ntags:\n  - {key: Bad Key, name: x}\n", "tags[0].key"},
		"dup key":       {"version: 1\ntags:\n  - {key: a, name: x}\n  - {key: a, name: y}\n", "used twice"},
		"bad status":    {"version: 1\nproblems:\n  - {key: a, type: open, status: archived}\n", "status: must be live or draft"},
		"bad change":    {"version: 1\nproblems:\n  - {key: a, type: open, change: huge}\n", "change: must be minor or major"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := seed.Parse(strings.NewReader(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestUnknownQuizProblemKey(t *testing.T) {
	e := newEnv(t)
	_, err := seedrun.ApplyFile(context.Background(), e.st, e.owner,
		parse(t, "version: 1\nquizzes:\n  - {key: q, title: T, problems: [nope]}\n"))
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuiltinPacks(t *testing.T) {
	names := seed.BuiltinNames()
	if len(names) == 0 {
		t.Fatal("no built-in packs")
	}
	e := newEnv(t)
	for _, n := range names {
		f, err := seed.Load(seed.BuiltinPrefix + n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if _, err := seedrun.ApplyFile(context.Background(), e.st, e.owner, f); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	before := e.rows()
	for _, n := range names {
		f, _ := seed.Load(seed.BuiltinPrefix + n)
		if _, err := seedrun.ApplyFile(context.Background(), e.st, e.owner, f); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range e.rows() {
		if before[k] != v {
			t.Errorf("%s: %d -> %d on re-run", k, before[k], v)
		}
	}
	if _, err := seed.Load("builtin:nope"); err == nil || !strings.Contains(err.Error(), "no built-in pack") {
		t.Errorf("unknown builtin err = %v", err)
	}
	if _, err := seed.Load("builtin:../seed"); err == nil {
		t.Error("path in builtin name accepted")
	}
}

func TestStartup(t *testing.T) {
	ctx := context.Background()
	t.Run("no user yet is skipped, not a failure", func(t *testing.T) {
		st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		skipped, err := seedrun.Startup(ctx, st, []string{"builtin:fsrs-basics"}, nil)
		if err != nil || skipped == nil {
			t.Fatalf("skipped=%v err=%v", skipped, err)
		}
	})
	t.Run("sole user owns the rows and a second start changes nothing", func(t *testing.T) {
		e := newEnv(t)
		var applied []string
		logf := func(spec string, _ seed.Report) { applied = append(applied, spec) }
		if skipped, err := seedrun.Startup(ctx, e.st, []string{"builtin:fsrs-basics"}, logf); err != nil || skipped != nil {
			t.Fatalf("skipped=%v err=%v", skipped, err)
		}
		before := e.rows()
		if before["problems"] == 0 || len(applied) != 1 {
			t.Fatalf("rows=%v applied=%v", before, applied)
		}
		if _, err := seedrun.Startup(ctx, e.st, []string{"builtin:fsrs-basics"}, nil); err != nil {
			t.Fatal(err)
		}
		for k, v := range e.rows() {
			if before[k] != v {
				t.Errorf("%s: %d -> %d", k, before[k], v)
			}
		}
	})
	t.Run("two users is skipped", func(t *testing.T) {
		e := newEnv(t)
		e2user(t, e.st, "second")
		skipped, err := seedrun.Startup(ctx, e.st, []string{"builtin:fsrs-basics"}, nil)
		if err != nil || skipped == nil || !strings.Contains(skipped.Error(), "--owner") {
			t.Fatalf("skipped=%v err=%v", skipped, err)
		}
	})
	t.Run("an unknown pack fails before anything is written", func(t *testing.T) {
		e := newEnv(t)
		_, err := seedrun.Startup(ctx, e.st, []string{"builtin:fsrs-basics", "builtin:nope"}, nil)
		if err == nil {
			t.Fatal("want error")
		}
		if r := e.rows(); r["problems"] != 0 {
			t.Errorf("problems = %d", r["problems"])
		}
	})
}

func TestResolveOwner(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if id, err := seedrun.ResolveOwner(ctx, e.st, ""); err != nil || id != e.owner {
		t.Errorf("sole = %v, %v", id, err)
	}
	if id, err := seedrun.ResolveOwner(ctx, e.st, e.owner.String()); err != nil || id != e.owner {
		t.Errorf("explicit = %v, %v", id, err)
	}
	if _, err := seedrun.ResolveOwner(ctx, e.st, "nonsense"); err == nil {
		t.Error("bad id accepted")
	}
	if _, err := seedrun.ResolveOwner(ctx, e.st, domain.NewIDGenerator(domain.SystemClock{}).New().String()); err == nil {
		t.Error("unknown user accepted")
	}
	e2user(t, e.st, "second")
	if _, err := seedrun.ResolveOwner(ctx, e.st, ""); err == nil {
		t.Error("ambiguous owner accepted")
	}
}
