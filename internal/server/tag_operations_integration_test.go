// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

// Agent clients declared in newITEnv: itTagAgent may propose but not apply;
// itTagApplier is also allowed tags:apply in its config.
const (
	itTagAgent   = "quizzotic-tag-agent"
	itTagApplier = "quizzotic-tag-applier"
)

type effectJSON struct {
	AffectedProblemIDs []string `json:"affected_problem_ids"`
	Tags               []struct {
		ID             string `json:"id"`
		Slug           string `json:"slug"`
		Status         string `json:"status"`
		ProblemsBefore int    `json:"problems_before"`
		ProblemsAfter  int    `json:"problems_after"`
		Created        bool   `json:"created"`
	} `json:"tags"`
}

type actorJSON struct {
	Kind     string `json:"kind"`
	ClientID string `json:"client_id"`
}

type opJSON struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Payload      json.RawMessage `json:"payload"`
	ProposedBy   actorJSON       `json:"proposed_by"`
	DecidedBy    *actorJSON      `json:"decided_by"`
	DecidedAt    *string         `json:"decided_at"`
	AppliedAt    *string         `json:"applied_at"`
	RevertedAt   *string         `json:"reverted_at"`
	RevertedBy   *actorJSON      `json:"reverted_by"`
	Preview      *effectJSON     `json:"preview"`
	PreviewError *string         `json:"preview_error"`
	Result       *effectJSON     `json:"result"`
}

func (e effectJSON) counts() map[string][2]int {
	m := map[string][2]int{}
	for _, t := range e.Tags {
		m[t.Slug] = [2]int{t.ProblemsBefore, t.ProblemsAfter}
	}
	return m
}

func init() {
	// Every call creates its own tag and a proposal to rename it, so each op
	// runs against a fresh proposed operation owned by user A.
	proposeRename := func(t testing.TB, c *ownerscope.Client) ownerscope.Created {
		slug, next := uniqueSlug("secret-op-tag"), uniqueSlug("secret-op-new")
		if r := c.Do("POST", "/tags", map[string]string{"slug": slug, "name": "Secret Op Tag"}); r.Code != http.StatusCreated {
			t.Fatalf("create tag = %d (%s)", r.Code, r.Body)
		}
		r := c.Do("POST", "/tag-operations", map[string]any{"kind": "rename", "payload": map[string]string{"tag": slug, "slug": next}})
		if r.Code != http.StatusCreated {
			t.Fatalf("propose = %d (%s)", r.Code, r.Body)
		}
		var op opJSON
		if err := json.Unmarshal(r.Body, &op); err != nil {
			t.Fatal(err)
		}
		return ownerscope.Created{
			Params:  ownerscope.Params{"id": op.ID},
			Secrets: []string{slug, next, "Secret Op Tag"},
		}
	}
	ownerScope.Resource(ownerscope.Resource{
		Name:   "tag operations",
		Create: proposeRename,
		Ops: []ownerscope.Op{
			{Pattern: "GET /tag-operations/{id}"},
			{Pattern: "POST /tag-operations/{id}/apply"},
			{Pattern: "POST /tag-operations/{id}/reject"},
			// The fixture operation is only proposed, so the owner's own revert is a 409.
			{Pattern: "POST /tag-operations/{id}/revert", WantOwner: http.StatusConflict},
			{Pattern: "GET /tag-operations", List: true},
			// Another user's tag in the payload is a clean validation error that
			// does not echo it.
			{Pattern: "POST /tag-operations", List: true, WantOwner: http.StatusCreated, WantOther: http.StatusUnprocessableEntity,
				Body: func(c ownerscope.Created) any {
					return map[string]any{"kind": "rename", "payload": map[string]string{"tag": c.Secrets[0], "slug": uniqueSlug("posted-op")}}
				}},
		},
		Intact: func(t testing.TB, c *ownerscope.Client, created ownerscope.Created) {
			r := c.Do("GET", "/tag-operations/"+created.Params["id"], nil)
			var op opJSON
			if err := json.Unmarshal(r.Body, &op); err != nil || op.Status != "proposed" {
				t.Errorf("operation decided by another user: %d %s", r.Code, r.Body)
			}
		},
	})
}

// opsEnv has user A as a human plus A's two agent clients, and user B.
type opsEnv struct {
	tagsEnv
	human, agent, applier *ownerscope.Client
}

func newOpsEnv(t *testing.T) opsEnv {
	t.Helper()
	te := newTagsEnv(t)
	as := func(clientID string) *ownerscope.Client {
		env := *te.a.Env
		env.Credentials = func(_ testing.TB, _ ownerscope.User) func(*http.Request) {
			tok := te.e.idp.access(clientID, "owner-a-sub")
			return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
		}
		return &ownerscope.Client{T: t, Env: &env, User: ownerscope.UserA}
	}
	return opsEnv{tagsEnv: te, human: te.a, agent: as(itTagAgent), applier: as(itTagApplier)}
}

func (oe opsEnv) tagProblems(t *testing.T, ref string) []string {
	t.Helper()
	page := decode[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, oe.human.Do("GET", "/tags/"+ref+"/problems", nil), http.StatusOK)
	var ids []string
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	slices.Sort(ids)
	return ids
}

func (oe opsEnv) tag(t *testing.T, p string, tags ...string) string {
	t.Helper()
	id := seedProblem(t, oe.e, "owner-a-sub", p).String()
	decode[struct{}](t, oe.human.Do("PUT", "/problems/"+id+"/tags", map[string]any{"tags": tags}), http.StatusOK)
	return id
}

// Done when: an agent without tags:apply can propose but not apply; a human
// applies it.
func TestAgentProposesHumanApplies(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "alpha", "Alpha")
	oe.create(t, oe.human, "beta", "Beta")
	p1 := oe.tag(t, "first", "alpha")
	p2 := oe.tag(t, "second", "alpha", "beta")
	want := []string{p1, p2}
	slices.Sort(want)

	// The agent proposes a merge by slug.
	body := map[string]any{"kind": "merge", "payload": map[string]any{"sources": []string{"alpha"}, "target": "beta"}}
	r := oe.agent.Do("POST", "/tag-operations", body)
	op := decode[opJSON](t, r, http.StatusCreated)
	if op.Status != "proposed" || op.Kind != "merge" || op.ProposedBy.Kind != "agent" || op.ProposedBy.ClientID != itTagAgent ||
		op.DecidedBy != nil || op.DecidedAt != nil || op.AppliedAt != nil || op.Result != nil {
		t.Fatalf("proposed = %+v", op)
	}
	if r.Header.Get("Location") == "" {
		t.Error("no Location header")
	}
	if op.Preview == nil || !slices.Equal(op.Preview.AffectedProblemIDs, want) {
		t.Fatalf("preview = %+v", op.Preview)
	}
	if c := op.Preview.counts(); c["alpha"] != [2]int{2, 0} || c["beta"] != [2]int{1, 2} {
		t.Fatalf("preview counts = %v", c)
	}
	if got := oe.tagProblems(t, "alpha"); !slices.Equal(got, want) {
		t.Fatalf("proposal changed alpha's problems: %v", got)
	}

	// The agent cannot apply, either at once or later, nor reject.
	wantProblem(t, oe.agent.Do("POST", "/tag-operations", map[string]any{"kind": body["kind"], "payload": body["payload"], "apply": true}),
		http.StatusForbidden, "forbidden")
	wantProblem(t, oe.agent.Do("POST", "/tag-operations/"+op.ID+"/apply", nil), http.StatusForbidden, "forbidden")
	wantProblem(t, oe.agent.Do("POST", "/tag-operations/"+op.ID+"/reject", nil), http.StatusForbidden, "forbidden")
	list := decode[struct {
		Items []opJSON `json:"items"`
	}](t, oe.agent.Do("GET", "/tag-operations", nil), http.StatusOK)
	if len(list.Items) != 1 || list.Items[0].Status != "proposed" {
		t.Fatalf("denied calls left operations %+v", list.Items)
	}

	// The agent can read it back with a live preview.
	got := decode[opJSON](t, oe.agent.Do("GET", "/tag-operations/"+op.ID, nil), http.StatusOK)
	if got.Status != "proposed" || got.Preview == nil || len(got.Preview.AffectedProblemIDs) != 2 {
		t.Fatalf("get = %+v", got)
	}

	// The human applies it.
	applied := decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/apply", nil), http.StatusOK)
	if applied.Status != "applied" || applied.DecidedBy == nil || applied.DecidedBy.Kind != "human" ||
		applied.ProposedBy.ClientID != itTagAgent || applied.AppliedAt == nil || applied.DecidedAt == nil {
		t.Fatalf("applied = %+v", applied)
	}
	if applied.Result == nil || !slices.Equal(applied.Result.AffectedProblemIDs, want) {
		t.Fatalf("result = %+v", applied.Result)
	}
	if got := oe.tagProblems(t, "beta"); !slices.Equal(got, want) {
		t.Fatalf("beta problems = %v, want %v", got, want)
	}
	if a, _ := oe.get(t, oe.human, "alpha"); a.Slug != "beta" || a.Status != "active" {
		t.Fatalf("alpha slug does not resolve to beta: %+v", a)
	}

	// Decided once.
	wantProblem(t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/apply", nil), http.StatusConflict, "conflict")
	wantProblem(t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/reject", nil), http.StatusConflict, "conflict")
	final := decode[opJSON](t, oe.human.Do("GET", "/tag-operations/"+op.ID, nil), http.StatusOK)
	if final.Status != "applied" || final.Preview != nil || final.Result != nil {
		t.Fatalf("final = %+v", final)
	}
}

func TestApplyScopeHeldByAgentClient(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "alpha", "Alpha")
	oe.tag(t, "first", "alpha")
	r := oe.applier.Do("POST", "/tag-operations", map[string]any{
		"kind": "rename", "payload": map[string]string{"tag": "alpha", "name": "Alpha Renamed"}, "apply": true})
	op := decode[opJSON](t, r, http.StatusCreated)
	if op.Status != "applied" || op.DecidedBy == nil || op.DecidedBy.ClientID != itTagApplier || op.Result == nil {
		t.Fatalf("op = %+v", op)
	}
	if tg, _ := oe.get(t, oe.human, "alpha"); tg.Slug != "alpha" {
		t.Fatalf("tag = %+v", tg)
	}
}

func TestHumanProposeAndApplyAtOnce(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "alpha", "Alpha")
	r := oe.human.Do("POST", "/tag-operations", map[string]any{
		"kind": "rename", "payload": map[string]string{"tag": "alpha", "slug": "omega"}, "apply": true})
	op := decode[opJSON](t, r, http.StatusCreated)
	if op.Status != "applied" || op.ProposedBy.Kind != "human" || op.DecidedBy == nil || op.Result == nil || op.Preview != nil {
		t.Fatalf("op = %+v", op)
	}
	tg, _ := oe.get(t, oe.human, "alpha")
	if tg.Slug != "omega" || !slices.Equal(tg.Aliases, []string{"alpha"}) {
		t.Fatalf("tag = %+v", tg)
	}
}

func TestRejectLeavesTagsAlone(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "alpha", "Alpha")
	op := decode[opJSON](t, oe.agent.Do("POST", "/tag-operations",
		map[string]any{"kind": "rename", "payload": map[string]string{"tag": "alpha", "slug": "omega"}}), http.StatusCreated)
	rej := decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/reject", nil), http.StatusOK)
	if rej.Status != "rejected" || rej.DecidedBy == nil || rej.DecidedBy.Kind != "human" || rej.AppliedAt != nil {
		t.Fatalf("rejected = %+v", rej)
	}
	if tg, _ := oe.get(t, oe.human, "alpha"); tg.Slug != "alpha" {
		t.Fatalf("tag renamed: %+v", tg)
	}
	wantProblem(t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/apply", nil), http.StatusConflict, "conflict")
	byStatus := decode[struct {
		Items []opJSON `json:"items"`
	}](t, oe.human.Do("GET", "/tag-operations?status=rejected", nil), http.StatusOK)
	if len(byStatus.Items) != 1 || byStatus.Items[0].ID != op.ID {
		t.Fatalf("rejected list = %+v", byStatus.Items)
	}
	wantProblem(t, oe.human.Do("GET", "/tag-operations?status=bogus", nil), http.StatusUnprocessableEntity, "validation")
}

func TestSplitProposalPreviewThenApply(t *testing.T) {
	oe := newOpsEnv(t)
	src := oe.create(t, oe.human, "data", "Data")
	p1 := oe.tag(t, "first", "data")
	p2 := oe.tag(t, "second", "data")
	r := oe.agent.Do("POST", "/tag-operations", map[string]any{"kind": "split", "payload": map[string]any{
		"source":        "data",
		"targets":       []map[string]any{{"new": map[string]string{"name": "Trees"}}, {"new": map[string]string{"name": "Graphs"}}},
		"assignments":   map[string][]string{p1: {"trees"}, p2: {"graphs"}},
		"retire_source": true,
	}})
	op := decode[opJSON](t, r, http.StatusCreated)
	created := map[string]string{}
	for _, tg := range op.Preview.Tags {
		if tg.Created {
			created[tg.Slug] = tg.ID
		}
	}
	if len(created) != 2 || op.Preview.counts()["data"] != [2]int{2, 0} || op.Preview.counts()["trees"] != [2]int{0, 1} {
		t.Fatalf("preview = %+v", op.Preview)
	}
	if r := oe.human.Do("GET", "/tags/trees", nil); r.Code != http.StatusNotFound {
		t.Fatalf("preview created a tag: %d", r.Code)
	}

	decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/apply", nil), http.StatusOK)
	// The tags exist with the IDs the preview showed.
	for slug, id := range created {
		if tg, _ := oe.get(t, oe.human, id); tg.Slug != slug || tg.Status != "active" {
			t.Errorf("created %s = %+v", slug, tg)
		}
	}
	if tg, _ := oe.get(t, oe.human, src.ID); tg.Status != "retired" {
		t.Fatalf("source = %+v", tg)
	}
	if got := oe.tagProblems(t, "trees"); !slices.Equal(got, []string{p1}) {
		t.Fatalf("trees problems = %v", got)
	}
}

func TestProposalValidationAndStaleApply(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "alpha", "Alpha")
	for name, body := range map[string]any{
		"unknown kind": map[string]any{"kind": "explode", "payload": map[string]string{}},
		"unknown tag":  map[string]any{"kind": "rename", "payload": map[string]string{"tag": "nope", "slug": "x"}},
		"no payload":   map[string]any{"kind": "rename"},
		"merge self":   map[string]any{"kind": "merge", "payload": map[string]any{"sources": []string{"alpha"}, "target": "alpha"}},
	} {
		if r := oe.agent.Do("POST", "/tag-operations", body); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422 (%s)", name, r.Code, r.Body)
		}
	}
	if r := oe.agent.Do("GET", "/tag-operations", nil); len(decode[struct {
		Items []opJSON `json:"items"`
	}](t, r, http.StatusOK).Items) != 0 {
		t.Error("invalid proposals were stored")
	}

	// A slug taken after the proposal makes the apply fail, atomically.
	op := decode[opJSON](t, oe.agent.Do("POST", "/tag-operations",
		map[string]any{"kind": "rename", "payload": map[string]string{"tag": "alpha", "slug": "omega"}}), http.StatusCreated)
	oe.create(t, oe.human, "omega", "Omega")
	wantProblem(t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/apply", nil), http.StatusConflict, "conflict")
	got := decode[opJSON](t, oe.human.Do("GET", "/tag-operations/"+op.ID, nil), http.StatusOK)
	if got.Status != "proposed" || got.Preview != nil || got.PreviewError == nil {
		t.Fatalf("after failed apply = %+v", got)
	}
	if tg, _ := oe.get(t, oe.human, "alpha"); tg.Slug != "alpha" || len(tg.Aliases) != 0 {
		t.Fatalf("failed apply left changes: %+v", tg)
	}
}
