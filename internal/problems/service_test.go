// SPDX-License-Identifier: Apache-2.0

package problems_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
)

type fixture struct {
	svc   *problems.Service
	st    *sqlite.Store
	actor domain.Actor
	clock *domain.FakeClock
}

func newFixture(t *testing.T, opts ...problems.Option) fixture {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := domain.NewFakeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	ids := domain.NewIDGenerator(clock)
	u := auth.User{ID: ids.New(), DisplayName: "A", Email: "a@example.com", CreatedAt: clock.Now()}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	return fixture{
		svc:   problems.NewService(st.Problems(), store.ProblemsTx(st), clock, ids, opts...),
		st:    st,
		actor: domain.Actor{Kind: domain.ActorAgent, UserID: u.ID, ClientID: "claude"},
		clock: clock,
	}
}

func mcqInput(shuffle bool) problems.CreateInput {
	return problems.CreateInput{
		Type: problems.TypeMCQ, Prompt: "Pick",
		Payload: json.RawMessage(`{
			"choices": [{"id":"a","text":"A"},{"id":"b","text":"B"},{"id":"c","text":"C"}],
			"correct": ["a"], "shuffle": ` + map[bool]string{true: "true", false: "false"}[shuffle] + `}`),
	}
}

func TestCreateStoresFirstMajorVersionWithCompactPayload(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.Create(context.Background(), f.actor, mcqInput(false))
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != problems.StatusDraft || v.Version != 1 || !v.CreatedAt.Equal(f.clock.Now()) {
		t.Errorf("view = %+v", v)
	}
	sv, err := f.st.Problems().GetVersion(context.Background(), f.actor.UserID, v.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sv.ChangeKind != problems.ChangeMajor || sv.CreatedBy != f.actor {
		t.Errorf("stored version = %+v", sv)
	}
	if want := `{"choices":[{"id":"a","text":"A"},{"id":"b","text":"B"},{"id":"c","text":"C"}],"correct":["a"],"shuffle":false}`; string(sv.Payload) != want {
		t.Errorf("payload = %s", sv.Payload)
	}
}

func TestCreateRejectsInvalidAndSystemActors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Create(ctx, domain.Actor{Kind: domain.ActorSystem}, mcqInput(false)); err == nil {
		t.Error("system actor accepted")
	}
	if _, err := f.svc.Create(ctx, domain.Actor{Kind: domain.ActorAgent}, mcqInput(false)); err == nil {
		t.Error("agent without user accepted")
	}
	in := mcqInput(false)
	in.Status = problems.StatusArchived
	_, err := f.svc.Create(ctx, f.actor, in)
	var pb *platform.Problem
	if !errors.As(err, &pb) || pb.Status != 422 || len(pb.Errors) != 1 || pb.Errors[0].Field != "status" {
		t.Errorf("err = %v", err)
	}
}

func TestPromptViewShufflesWithInjectedShuffle(t *testing.T) {
	// Reverse, so the test is deterministic.
	reverse := func(n int, swap func(i, j int)) {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			swap(i, j)
		}
	}
	f := newFixture(t, problems.WithShuffle(reverse))
	ctx := context.Background()

	shuffled, err := f.svc.Create(ctx, f.actor, mcqInput(true))
	if err != nil {
		t.Fatal(err)
	}
	pv, err := f.svc.GetPrompt(ctx, f.actor.UserID, shuffled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"choices":[{"id":"c","text":"C"},{"id":"b","text":"B"},{"id":"a","text":"A"}],"multiple":false,"shuffle":true}`; string(pv.Payload) != want {
		t.Errorf("payload = %s", pv.Payload)
	}

	plain, _ := f.svc.Create(ctx, f.actor, mcqInput(false))
	pv, _ = f.svc.GetPrompt(ctx, f.actor.UserID, plain.ID)
	if want := `{"choices":[{"id":"a","text":"A"},{"id":"b","text":"B"},{"id":"c","text":"C"}],"multiple":false,"shuffle":false}`; string(pv.Payload) != want {
		t.Errorf("payload = %s", pv.Payload)
	}
}

func TestListCursor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for range 3 {
		if _, err := f.svc.Create(ctx, f.actor, mcqInput(false)); err != nil {
			t.Fatal(err)
		}
	}
	p1, err := f.svc.List(ctx, f.actor.UserID, problems.ListQuery{Limit: 2})
	if err != nil || len(p1.Items) != 2 || p1.NextCursor == nil {
		t.Fatalf("page 1 = %+v err %v", p1, err)
	}
	p2, err := f.svc.List(ctx, f.actor.UserID, problems.ListQuery{Limit: 2, Cursor: *p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != nil {
		t.Fatalf("page 2 = %+v err %v", p2, err)
	}
	var pb *platform.Problem
	_, err = f.svc.List(ctx, f.actor.UserID, problems.ListQuery{Cursor: "garbage!"})
	if !errors.As(err, &pb) || pb.Status != 422 || pb.Errors[0].Field != "cursor" {
		t.Errorf("bad cursor err = %v", err)
	}
}
