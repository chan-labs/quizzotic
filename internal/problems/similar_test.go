// SPDX-License-Identifier: Apache-2.0

package problems_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

func openIn(prompt, ref string) problems.CreateInput {
	b, _ := json.Marshal(map[string]string{"reference_answer": ref})
	return problems.CreateInput{Type: problems.TypeOpen, Prompt: prompt, Payload: b}
}

func (f fixture) mustCreate(t *testing.T, in problems.CreateInput) problems.View {
	t.Helper()
	v, err := f.svc.Create(context.Background(), f.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func similarIDs(r problems.SimilarResult) []domain.ID {
	var ids []domain.ID
	for _, it := range r.Items {
		ids = append(ids, it.Problem.ID)
	}
	return ids
}

// A fixture with near-duplicates and unrelated problems: the near-duplicates
// must rank first, closest first.
func TestSimilarRanksNearDuplicatesFirst(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	unrelated1 := f.mustCreate(t, openIn("Explain how the mitochondria produce ATP", "Oxidative phosphorylation"))
	exact := f.mustCreate(t, openIn("What is the capital of France?", "Paris"))
	unrelated2 := f.mustCreate(t, mcqInput(false))
	near := f.mustCreate(t, openIn("Which city is the capital of France", "The city of Paris"))
	loose := f.mustCreate(t, openIn("Name the capital of Germany", "Berlin"))

	res, err := f.svc.Similar(ctx, f.actor.UserID, problems.SimilarInput{
		Prompt: "What is the capital of France?", Answers: []string{"Paris"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The unrelated ATP problem shares only "the", so it may trail the list but
	// never outranks a duplicate; the MCQ shares nothing and is absent.
	want := []domain.ID{exact.ID, near.ID, loose.ID, unrelated1.ID}
	if got := similarIDs(res); !slices.Equal(got, want) {
		t.Fatalf("ranking = %v, want %v (mcq: %v)", got, want, unrelated2.ID)
	}
	if res.Items[0].Score != 1 {
		t.Errorf("exact duplicate score = %v, want 1", res.Items[0].Score)
	}
	for i := 1; i < len(res.Items); i++ {
		if res.Items[i].Score > res.Items[i-1].Score || res.Items[i].Score <= 0 {
			t.Errorf("scores not descending and positive: %+v", res.Items)
		}
	}
}

func TestSimilarLimitMinScoreAndDefaults(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, p := range []string{"alpha beta gamma", "alpha beta delta", "alpha epsilon zeta", "alpha eta theta iota"} {
		f.mustCreate(t, openIn(p, "x"))
	}
	in := problems.SimilarInput{Prompt: "alpha beta gamma", Answers: []string{"x"}}

	res, err := f.svc.Similar(ctx, f.actor.UserID, in)
	if err != nil || len(res.Items) != 4 {
		t.Fatalf("default = %d items, err %v", len(res.Items), err)
	}
	in.Limit = 2
	if res, _ = f.svc.Similar(ctx, f.actor.UserID, in); len(res.Items) != 2 {
		t.Errorf("limit 2 = %d items", len(res.Items))
	}
	in.Limit, in.MinScore = 0, 0.5
	res, _ = f.svc.Similar(ctx, f.actor.UserID, in)
	if len(res.Items) != 2 {
		t.Errorf("min_score 0.5 = %d items, want 2", len(res.Items))
	}
	// No overlap: an empty list, not null.
	res, err = f.svc.Similar(ctx, f.actor.UserID, problems.SimilarInput{Prompt: "zzz"})
	if err != nil || res.Items == nil || len(res.Items) != 0 {
		t.Errorf("no match = %+v err %v", res, err)
	}
}

func TestSimilarSkipsDeletedAndUsesCurrentVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	gone := f.mustCreate(t, openIn("quantum entanglement basics", "spooky"))
	edited := f.mustCreate(t, openIn("quantum entanglement basics", "spooky"))
	if err := f.st.Problems().SoftDelete(ctx, f.actor.UserID, gone.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	nv := problems.Version{ProblemID: edited.ID, Type: problems.TypeOpen, Prompt: "photosynthesis in plants",
		Payload: json.RawMessage(`{"reference_answer":"light"}`), ChangeKind: problems.ChangeMajor,
		CreatedBy: f.actor, CreatedAt: f.clock.Now()}
	if _, err := f.st.Problems().AppendVersion(ctx, f.actor.UserID, edited.ID, nv); err != nil {
		t.Fatal(err)
	}

	res, err := f.svc.Similar(ctx, f.actor.UserID, problems.SimilarInput{Prompt: "quantum entanglement basics"})
	if err != nil || len(res.Items) != 0 {
		t.Fatalf("old text matched: %+v err %v", res, err)
	}
	res, _ = f.svc.Similar(ctx, f.actor.UserID, problems.SimilarInput{Prompt: "photosynthesis in plants"})
	if got := similarIDs(res); !slices.Equal(got, []domain.ID{edited.ID}) || res.Items[0].Problem.Version != 2 {
		t.Errorf("current version not matched: %+v", res.Items)
	}
}

func TestSimilarValidation(t *testing.T) {
	f := newFixture(t)
	for name, in := range map[string]problems.SimilarInput{
		"blank prompt": {Prompt: "  "},
		"big limit":    {Prompt: "x", Limit: 51},
		"neg limit":    {Prompt: "x", Limit: -1},
		"big score":    {Prompt: "x", MinScore: 1.5},
		"neg score":    {Prompt: "x", MinScore: -0.1},
	} {
		_, err := f.svc.Similar(context.Background(), f.actor.UserID, in)
		var pb *platform.Problem
		if !errors.As(err, &pb) || pb.Status != 422 || len(pb.Errors) == 0 {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
