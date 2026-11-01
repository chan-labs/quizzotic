// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// tagState renders every tag (all statuses, with aliases) and the problems
// each carries, as the API reports them.
func (oe opsEnv) tagState(t *testing.T) string {
	t.Helper()
	var lines []string
	for _, st := range []string{"active", "merged", "retired"} {
		page := decode[tagPageJSON](t, oe.human.Do("GET", "/tags?status="+st+"&limit=200", nil), http.StatusOK)
		for _, tg := range page.Items {
			mi := ""
			if tg.MergedInto != nil {
				mi = *tg.MergedInto
			}
			lines = append(lines, fmt.Sprintf("tag %s %s %q %s into=%s aliases=%v problems=%v",
				tg.ID, tg.Slug, tg.Name, tg.Status, mi, tg.Aliases, oe.tagProblems(t, tg.ID)))
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func (oe opsEnv) applyOp(t *testing.T, kind string, payload any) opJSON {
	t.Helper()
	return decode[opJSON](t, oe.human.Do("POST", "/tag-operations",
		map[string]any{"kind": kind, "payload": payload, "apply": true}), http.StatusCreated)
}

// Done when: merge -> revert round-trips to identical mappings and tag rows.
func TestRevertMergeRoundTripsOverHTTP(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "golang", "Golang")
	oe.create(t, oe.human, "go-lang", "Go Lang")
	oe.create(t, oe.human, "go", "Go")
	oe.tag(t, "one", "golang")
	oe.tag(t, "two", "golang", "go")
	oe.tag(t, "three", "go-lang", "go")
	oe.tag(t, "four", "go")
	before := oe.tagState(t)

	op := oe.applyOp(t, "merge", map[string]any{"sources": []string{"golang", "go-lang"}, "target": "go"})
	if oe.tagState(t) == before {
		t.Fatal("merge changed nothing")
	}
	if op.RevertedAt != nil || op.RevertedBy != nil {
		t.Fatalf("fresh op = %+v", op)
	}

	rev := decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/revert", nil), http.StatusOK)
	if rev.Status != "applied" || rev.RevertedAt == nil || rev.RevertedBy == nil || rev.RevertedBy.Kind != "human" {
		t.Fatalf("reverted = %+v", rev)
	}
	if after := oe.tagState(t); after != before {
		t.Fatalf("state after revert differs:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if tg, _ := oe.get(t, oe.human, "golang"); tg.Status != "active" || tg.Slug != "golang" {
		t.Fatalf("golang = %+v", tg)
	}

	// GET shows the marker; a second revert is a 409.
	got := decode[opJSON](t, oe.human.Do("GET", "/tag-operations/"+op.ID, nil), http.StatusOK)
	if got.RevertedAt == nil || got.Status != "applied" {
		t.Fatalf("get = %+v", got)
	}
	wantProblem(t, oe.human.Do("POST", "/tag-operations/"+op.ID+"/revert", nil), http.StatusConflict, "conflict")
}

func TestRevertRenameAndSplitOverHTTP(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "data", "Data")
	p1, p2 := oe.tag(t, "first", "data"), oe.tag(t, "second", "data")
	before := oe.tagState(t)

	ren := oe.applyOp(t, "rename", map[string]any{"tag": "data", "slug": "datasets", "name": "Datasets"})
	decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+ren.ID+"/revert", nil), http.StatusOK)
	if after := oe.tagState(t); after != before {
		t.Fatalf("rename revert differs:\n--- before\n%s\n--- after\n%s", before, after)
	}

	split := oe.applyOp(t, "split", map[string]any{
		"source":        "data",
		"targets":       []map[string]any{{"new": map[string]string{"name": "Trees"}}, {"new": map[string]string{"name": "Graphs"}}},
		"assignments":   map[string][]string{p1: {"trees"}, p2: {"graphs"}},
		"retire_source": true,
	})
	if oe.get(t, oe.human, "trees"); oe.tagState(t) == before {
		t.Fatal("split changed nothing")
	}
	decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+split.ID+"/revert", nil), http.StatusOK)
	if after := oe.tagState(t); after != before {
		t.Fatalf("split revert differs:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if r := oe.human.Do("GET", "/tags/trees", nil); r.Code != http.StatusNotFound {
		t.Fatalf("created tag survived revert: %d", r.Code)
	}
}

func TestRevertConflictAndPermissions(t *testing.T) {
	oe := newOpsEnv(t)
	oe.create(t, oe.human, "alpha", "Alpha")
	oe.create(t, oe.human, "beta", "Beta")
	oe.tag(t, "first", "alpha")

	merge := oe.applyOp(t, "merge", map[string]any{"sources": []string{"alpha"}, "target": "beta"})
	rename := oe.applyOp(t, "rename", map[string]any{"tag": "beta", "name": "Beta 2"})
	merged := oe.tagState(t)

	// An agent client without tags:apply cannot revert.
	wantProblem(t, oe.agent.Do("POST", "/tag-operations/"+merge.ID+"/revert", nil), http.StatusForbidden, "forbidden")
	// Another user sees nothing and changes nothing.
	wantProblem(t, oe.b.Do("POST", "/tag-operations/"+rename.ID+"/revert", nil), http.StatusNotFound, "not-found")

	// A later applied operation touched beta, so the merge cannot be reverted.
	wantProblem(t, oe.human.Do("POST", "/tag-operations/"+merge.ID+"/revert", nil), http.StatusConflict, "conflict")
	if oe.tagState(t) != merged {
		t.Fatal("refused revert changed state")
	}

	// Reverting the later one first unblocks it; an agent holding tags:apply may do it.
	decode[opJSON](t, oe.applier.Do("POST", "/tag-operations/"+rename.ID+"/revert", nil), http.StatusOK)
	decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+merge.ID+"/revert", nil), http.StatusOK)
	if a, _ := oe.get(t, oe.human, "alpha"); a.Status != "active" {
		t.Fatalf("alpha = %+v", a)
	}

	// A proposed operation cannot be reverted.
	prop := decode[opJSON](t, oe.agent.Do("POST", "/tag-operations",
		map[string]any{"kind": "rename", "payload": map[string]string{"tag": "alpha", "slug": "omega"}}), http.StatusCreated)
	wantProblem(t, oe.human.Do("POST", "/tag-operations/"+prop.ID+"/revert", nil), http.StatusConflict, "conflict")
}
