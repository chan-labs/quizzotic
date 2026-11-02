// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/attempts"
	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/export"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/review"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/settings"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// ExportFixture is the data RichFixture wrote.
type ExportFixture struct {
	// Owner is the user with a row in every exported table; Other is a second
	// user whose data must never appear in Owner's export.
	Owner, Other domain.ID
}

// RichFixture fills an empty, migrated store with data in every exported
// table, built through the real services where they exist: problems with
// several versions (minor and major), a draft, an archived and a deleted
// problem, tags with aliases and mappings, tag operations that were applied,
// reverted, proposed, rejected and split, quizzes (one deleted), review and
// quiz sessions, seed keys, per-user settings, identities and an attempt log
// that includes an open attempt rated afterwards. A second user owns a little
// of everything too.
func RichFixture(t testing.TB, st store.Store) ExportFixture {
	t.Helper()
	ctx := context.Background()
	clock := domain.NewFakeClock(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	tick := func() time.Time { clock.Advance(time.Minute); return clock.Now() }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	f64, i := func(v float64) *float64 { return &v }, func(v int) *int { return &v }
	str := func(v string) *string { return &v }
	owner := auth.User{ID: ids.New(), DisplayName: "Export Owner", Email: "owner@example.com", CreatedAt: tick(),
		Scheduling: auth.Scheduling{DesiredRetention: f64(0.85), LearningSteps: []string{"1m", "10m"}, NewPerDay: i(7),
			MaxReviewsPerDay: i(150), NewInterval: i(2), WeakTagBoost: f64(1.5), RolloverHour: i(4), Timezone: str("Europe/Berlin")}}
	other := auth.User{ID: ids.New(), DisplayName: "Someone Else", Email: "other@example.com", CreatedAt: tick()}
	for _, u := range []auth.User{owner, other} {
		must(st.Users().Create(ctx, u))
		_, err := st.Identities().InsertIfAbsent(ctx, auth.Identity{ID: ids.New(), UserID: u.ID,
			Issuer: "https://idp.example.com", Subject: "sub-" + u.DisplayName, CreatedAt: tick(), LastSeenAt: tick()})
		must(err)
	}
	human := domain.Actor{Kind: domain.ActorHuman, UserID: owner.ID, ClientID: "quizzotic-web"}
	agent := domain.Actor{Kind: domain.ActorAgent, UserID: owner.ID, ClientID: "curator"}
	system := domain.Actor{Kind: domain.ActorSystem}

	// Problems. Payload key order and spacing differ on purpose: Postgres
	// stores jsonb, so the export must canonicalize.
	newProblem := func(u auth.User, by domain.Actor, status problems.Status, typ, prompt, payload string) problems.Problem {
		p := problems.Problem{ID: ids.New(), OwnerID: u.ID, Status: status, CurrentVersion: 1, CreatedBy: by, CreatedAt: tick()}
		p.UpdatedAt = p.CreatedAt
		v := problems.Version{ProblemID: p.ID, Version: 1, Type: typ, Prompt: prompt, Payload: json.RawMessage(payload),
			Explanation: "because <b>reasons</b> & more", Source: "textbook p. 4", ChangeKind: problems.ChangeMajor, CreatedBy: by, CreatedAt: p.CreatedAt}
		must(st.Problems().Create(ctx, u.ID, p, v))
		return p
	}
	p1 := newProblem(owner, human, problems.StatusLive, problems.TypeMCQ, "Which is a Go keyword?",
		`{"choices":[{"id":"a","text":"func"},{"id":"b","text":"def"}],"correct":["a"],"multi":false}`)
	p2 := newProblem(owner, agent, problems.StatusLive, problems.TypeCloze, "A slice has a {{c1}} and a capacity.",
		`{"blanks":{"c1":["length","len"]},"note":  "ünïcode ✓"}`)
	p3 := newProblem(owner, human, problems.StatusLive, problems.TypeOpen, "Explain goroutines.",
		`{"z":null,"reference_answer":"lightweight threads","a":[1,2.5,{"y":true,"b":false}]}`)
	p4 := newProblem(owner, system, problems.StatusDraft, problems.TypeOpen, "Draft question?", `{"reference_answer":"x"}`)
	p5 := newProblem(owner, human, problems.StatusArchived, problems.TypeOpen, "Old question?", `{"reference_answer":"y"}`)
	p6 := newProblem(owner, human, problems.StatusLive, problems.TypeOpen, "Deleted question?", `{"reference_answer":"z"}`)
	must(st.Problems().SoftDelete(ctx, owner.ID, p6.ID, tick()))
	otherProblem := newProblem(other, domain.Actor{Kind: domain.ActorHuman, UserID: other.ID}, problems.StatusLive,
		problems.TypeOpen, "Not yours", `{"reference_answer":"secret"}`)
	for _, a := range []struct {
		p    problems.Problem
		kind problems.ChangeKind
		text string
	}{{p1, problems.ChangeMinor, "Which of these is a Go keyword?"}, {p1, problems.ChangeMajor, "Pick the Go keyword."}, {p3, problems.ChangeMinor, "Explain goroutines briefly."}} {
		_, err := st.Problems().AppendVersion(ctx, owner.ID, a.p.ID, problems.Version{ProblemID: a.p.ID, Type: problems.TypeOpen,
			Prompt: a.text, Payload: json.RawMessage(`{"reference_answer":"edited","choices":[{"id":"a","text":"func"},{"id":"b","text":"def"}],"correct":["a"],"multi":false}`),
			ChangeKind: a.kind, CreatedBy: agent, CreatedAt: tick()})
		must(err)
	}

	// Tags, mappings, and every kind of tag operation, through the service.
	ts := tags.NewService(store.TagsTx(st), clock, ids)
	tag := func(slug string) tags.Tag {
		tg, err := ts.Create(ctx, owner.ID, tags.CreateInput{Slug: slug, Name: strings.ToUpper(slug), Description: "about " + slug})
		must(err)
		clock.Advance(time.Minute)
		return tg
	}
	golang, goLang, goTag, data, maths, spare := tag("golang"), tag("go-lang"), tag("go"), tag("data"), tag("math"), tag("spare")
	setTags := func(p problems.Problem, refs ...string) {
		_, err := ts.SetProblemTags(ctx, owner.ID, p.ID, refs)
		must(err)
	}
	setTags(p1, "golang", "go")
	setTags(p2, "go-lang", "math")
	setTags(p3, "golang", "data")
	setTags(p4, "data")
	setTags(p5, "math", "spare")
	_, _, _, _ = spare, maths, goLang, golang
	_ = data
	propose := func(kind tags.OpKind, payload any, apply bool) tags.Operation {
		raw, err := json.Marshal(payload)
		must(err)
		clock.Advance(time.Minute)
		d, err := ts.Propose(ctx, owner.ID, human, tags.ProposeInput{Kind: kind, Payload: raw, Apply: apply})
		must(err)
		return d.Operation
	}
	propose(tags.OpRename, tags.RenameInput{Tag: "math", Slug: str("maths"), Name: str("Maths")}, true)
	reverted := propose(tags.OpMerge, tags.MergeInput{Sources: []string{"golang"}, Target: "go"}, true)
	clock.Advance(time.Minute)
	_, err := ts.RevertOperation(ctx, owner.ID, agent, reverted.ID)
	must(err)
	propose(tags.OpMerge, tags.MergeInput{Sources: []string{"go-lang"}, Target: "go"}, true)
	propose(tags.OpRename, tags.RenameInput{Tag: "spare", Name: str("Spare tag")}, false)
	rejected := propose(tags.OpRename, tags.RenameInput{Tag: "spare", Slug: str("spare-parts")}, false)
	clock.Advance(time.Minute)
	_, err = ts.RejectOperation(ctx, owner.ID, human, rejected.ID)
	must(err)
	clock.Advance(time.Minute)
	_, err = ts.Split(ctx, owner.ID, human, tags.SplitInput{Source: "data",
		Targets:      []tags.SplitTarget{{New: &tags.CreateInput{Name: "Data A"}}, {New: &tags.CreateInput{Name: "Data B"}}},
		Assignments:  map[domain.ID][]string{p3.ID: {"data-a"}, p4.ID: {"data-a", "data-b"}},
		RetireSource: true})
	must(err)
	otherTag, err := tags.NewService(store.TagsTx(st), clock, ids).Create(ctx, other.ID, tags.CreateInput{Name: "Private"})
	must(err)
	_, err = tags.NewService(store.TagsTx(st), clock, ids).SetProblemTags(ctx, other.ID, otherProblem.ID, []string{otherTag.Slug})
	must(err)

	// Quizzes.
	newQuiz := func(u domain.ID, title string, status quizzes.Status, items ...domain.ID) quizzes.Quiz {
		q := quizzes.Quiz{ID: ids.New(), OwnerID: u, Title: title, Description: "d " + title, Status: status,
			CreatedBy: domain.Actor{Kind: domain.ActorHuman, UserID: u}, CreatedAt: tick()}
		q.UpdatedAt = q.CreatedAt
		must(st.Quizzes().Create(ctx, u, q))
		if len(items) > 0 {
			must(st.Quizzes().SetItems(ctx, u, q.ID, items, tick()))
		}
		return q
	}
	quiz := newQuiz(owner.ID, "Go basics", quizzes.StatusLive, p3.ID, p1.ID, p2.ID)
	gone := newQuiz(owner.ID, "Retired quiz", quizzes.StatusDraft, p1.ID)
	must(st.Quizzes().SoftDelete(ctx, owner.ID, gone.ID, tick()))
	newQuiz(other.ID, "Not yours", quizzes.StatusLive, otherProblem.ID)

	// Sessions: a review session that grew and completed, and a quiz session.
	rs := review.Session{ID: ids.New(), UserID: owner.ID, Kind: review.SessionReview, CreatedAt: tick(),
		Items: []review.Item{{ProblemID: p1.ID, Epoch: 1, Kind: review.ItemNew}, {ProblemID: p2.ID, Epoch: 1, Kind: review.ItemNew}}}
	must(st.Sessions().Create(ctx, owner.ID, rs))
	must(st.Sessions().AppendItems(ctx, owner.ID, rs.ID, 2, []review.Item{{ProblemID: p1.ID, Epoch: 1, Kind: review.ItemReview}}))
	must(st.Sessions().Complete(ctx, owner.ID, rs.ID, tick()))
	qs := review.Session{ID: ids.New(), UserID: owner.ID, Kind: review.SessionQuiz, QuizID: quiz.ID, CreatedAt: tick(),
		Items: []review.Item{{ProblemID: p3.ID, Epoch: 1, Kind: "quiz"}}}
	must(st.Sessions().Create(ctx, owner.ID, qs))

	// Seed keys.
	must(st.SeedKeys().Put(ctx, owner.ID, seed.KindProblem, "go/keyword", seed.Entry{EntityID: p1.ID, Digest: "d1"}, tick()))
	must(st.SeedKeys().Put(ctx, owner.ID, seed.KindTag, "go", seed.Entry{EntityID: goTag.ID, Digest: "d2"}, tick()))
	must(st.SeedKeys().Put(ctx, owner.ID, seed.KindQuiz, "basics", seed.Entry{EntityID: quiz.ID, Digest: "d3"}, tick()))

	// Attempts: all three client kinds, an unscored/unrated open attempt that
	// is rated afterwards, a session attempt, an older epoch.
	attempt := func(u domain.ID, p problems.Problem, version, epoch int, by domain.Actor, resp string, auto *bool, score *float64, rating *int, session domain.ID) attempts.Attempt {
		a := attempts.Attempt{ID: ids.New(), UserID: u, ProblemID: p.ID, ProblemVersion: version, CardEpoch: epoch,
			SessionID: session, Response: json.RawMessage(resp), AutoCorrect: auto, Score: score, Rating: rating,
			LatencyMS: 1200 + epoch, AnsweredAt: tick(), Client: by}
		must(st.Attempts().Create(ctx, u, a))
		return a
	}
	yes, no := true, false
	rate := func(n int) *int { return &n }
	attempt(owner.ID, p1, 1, 1, human, `{"choices":["a"]}`, &yes, f64(1), rate(3), domain.Nil)
	attempt(owner.ID, p1, 3, 2, human, `{"choices":["b"]}`, &no, f64(0), rate(1), rs.ID)
	attempt(owner.ID, p1, 3, 2, human, `{"choices":["a"]}`, &yes, f64(1), rate(4), rs.ID)
	attempt(owner.ID, p2, 1, 1, human, `{"blanks":{"c1":"len"}}`, &yes, f64(0.5), nil, domain.Nil)
	open := attempt(owner.ID, p3, 1, 1, human, `{"text":"threads <3"}`, nil, nil, nil, qs.ID)
	attempt(owner.ID, p3, 2, 1, system, `{"text":"again"}`, nil, f64(0.75), rate(3), domain.Nil)
	must(st.Attempts().SetRating(ctx, owner.ID, open.ID, 2, f64(0.5)))
	attempt(other.ID, otherProblem, 1, 1, domain.Actor{Kind: domain.ActorHuman, UserID: other.ID}, `{"text":"private"}`, nil, f64(1), rate(3), domain.Nil)

	// Settings: the user row already carries overrides; touch them through the repo too.
	must(st.Settings().Replace(ctx, owner.ID, settings.Overrides{DesiredRetention: f64(0.9), NewPerDay: i(9),
		MaxReviewsPerDay: i(120), NewInterval: i(3), WeakTagBoost: f64(2), RolloverHour: i(5), Timezone: str("Asia/Tokyo")}))
	return ExportFixture{Owner: owner.ID, Other: other.ID}
}

// ExportBytes exports one user's data through a fresh export service over s.
func ExportBytes(t testing.TB, s store.Store, owner domain.ID) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := exportService(s).Export(context.Background(), owner, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	return buf.Bytes()
}

// ImportBytes imports a document into s with the real projection rebuild.
func ImportBytes(t testing.TB, s store.Store, doc []byte) (export.Summary, error) {
	t.Helper()
	return exportService(s).Import(context.Background(), bytes.NewReader(doc))
}

func exportService(s store.Store) *export.Service {
	return store.NewMastery(store.DefaultCards(), domain.SystemClock{}).ExportService(s, nil)
}

func runExport(t *testing.T, newStore NewStore) {
	ctx := context.Background()

	t.Run("exports only the owner's rows, in a stable order", func(t *testing.T) {
		s, _ := newStore(t)
		fx := RichFixture(t, s)
		doc := ExportBytes(t, s, fx.Owner)
		for _, secret := range []string{fx.Other.String(), "other@example.com", "Not yours", "private", "Private"} {
			if bytes.Contains(doc, []byte(secret)) {
				t.Errorf("export contains another user's data (%q)", secret)
			}
		}
		var parsed struct {
			Format string                       `json:"format"`
			Tables map[string][]json.RawMessage `json:"tables"`
			Counts map[string]int               `json:"counts"`
		}
		if err := json.Unmarshal(doc, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Format != export.FormatName {
			t.Errorf("format = %q", parsed.Format)
		}
		for _, tb := range export.Tables {
			if len(parsed.Tables[tb.Name]) == 0 {
				t.Errorf("fixture left %s empty, so it is not covered", tb.Name)
			}
			if parsed.Counts[tb.Name] != len(parsed.Tables[tb.Name]) {
				t.Errorf("counts[%s] = %d, rows = %d", tb.Name, parsed.Counts[tb.Name], len(parsed.Tables[tb.Name]))
			}
		}
		if again := ExportBytes(t, s, fx.Owner); !bytes.Equal(doc, again) {
			t.Error("two exports of the same data differ")
		}
		if err := exportService(s).Export(ctx, domain.NewIDGenerator(domain.SystemClock{}).New(), &bytes.Buffer{}); err == nil {
			t.Error("exporting an unknown user succeeded")
		}
	})

	t.Run("read pages in key order", func(t *testing.T) {
		s, _ := newStore(t)
		fx := RichFixture(t, s)
		for _, tb := range export.Tables {
			all, err := s.Export().Read(ctx, fx.Owner, tb, nil, 1000)
			if err != nil {
				t.Fatalf("%s: %v", tb.Name, err)
			}
			var paged []export.Row
			var after export.Row
			for {
				page, err := s.Export().Read(ctx, fx.Owner, tb, after, 2)
				if err != nil {
					t.Fatalf("%s page: %v", tb.Name, err)
				}
				paged = append(paged, page...)
				if len(page) < 2 {
					break
				}
				after = page[len(page)-1]
			}
			if len(paged) != len(all) {
				t.Errorf("%s: paged %d rows, whole read %d", tb.Name, len(paged), len(all))
			}
		}
	})

	t.Run("import restores everything and rebuilds projections", func(t *testing.T) {
		src, _ := newStore(t)
		fx := RichFixture(t, src)
		doc := ExportBytes(t, src, fx.Owner)

		dst, _ := newStore(t)
		sum, err := ImportBytes(t, dst, doc)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if sum.UserID != fx.Owner || sum.Rows["attempts"] != 6 {
			t.Errorf("summary = %+v", sum)
		}
		if again := ExportBytes(t, dst, fx.Owner); !bytes.Equal(doc, again) {
			t.Errorf("re-export differs:\n%s\n----\n%s", doc, again)
		}
		cards, err := dst.Cards().List(ctx, fx.Owner)
		if err != nil || len(cards) == 0 {
			t.Errorf("cards after import = %d, %v (want a rebuilt projection)", len(cards), err)
		}
		// The append-only rule survives the import.
		if err := dst.WithTx(ctx, func(tx store.Store) error {
			return tx.Attempts().SetRating(ctx, fx.Owner, mustFirstAttempt(t, dst, fx.Owner), 1, nil)
		}); err == nil {
			t.Error("a rated attempt accepted a second rating after import")
		}
	})

	t.Run("import refuses a non-empty instance and leaves it untouched", func(t *testing.T) {
		src, _ := newStore(t)
		fx := RichFixture(t, src)
		doc := ExportBytes(t, src, fx.Owner)

		dst, _ := newStore(t)
		RichFixture(t, dst)
		before := ExportBytes(t, dst, mustSole(t, dst))
		if _, err := ImportBytes(t, dst, doc); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("import into a non-empty instance = %v", err)
		}
		if after := ExportBytes(t, dst, mustSole(t, dst)); !bytes.Equal(before, after) {
			t.Error("a refused import changed the instance")
		}
	})

	t.Run("a bad document imports nothing", func(t *testing.T) {
		src, _ := newStore(t)
		fx := RichFixture(t, src)
		doc := ExportBytes(t, src, fx.Owner)
		for name, bad := range map[string][]byte{
			"truncated":      doc[:len(doc)/2],
			"wrong version":  bytes.Replace(doc, []byte(`"version":1`), []byte(`"version":99`), 1),
			"wrong counts":   bytes.Replace(doc, []byte(`"counts":{"attempts":`), []byte(`"counts":{"attempts":9999,"x":`), 1),
			"foreign owner":  bytes.Replace(doc, []byte(`"owner_id":"`+fx.Owner.String()+`"`), []byte(`"owner_id":"`+fx.Other.String()+`"`), 1),
			"unknown column": bytes.Replace(doc, []byte(`"display_name"`), []byte(`"nickname"`), 1),
		} {
			dst, _ := newStore(t)
			if _, err := ImportBytes(t, dst, bad); err == nil {
				t.Errorf("%s: import succeeded", name)
			}
			if has, _ := dst.Export().HasUsers(ctx); has {
				t.Errorf("%s: a failed import left rows behind", name)
			}
		}
	})
}

func mustSole(t testing.TB, s store.Store) domain.ID {
	t.Helper()
	// The fixture has two users; the owner is the first created.
	ids, err := s.Users().ListIDs(context.Background(), domain.ID{}, 10)
	if err != nil || len(ids) == 0 {
		t.Fatalf("users = %v, %v", ids, err)
	}
	return ids[0]
}

func mustFirstAttempt(t testing.TB, s store.Store, owner domain.ID) domain.ID {
	t.Helper()
	as, err := s.Attempts().List(context.Background(), owner, attempts.ListFilter{Limit: 1})
	if err != nil || len(as) == 0 {
		t.Fatalf("attempts = %v, %v", as, err)
	}
	return as[0].ID
}
