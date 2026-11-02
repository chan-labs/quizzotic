// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"slices"
	"testing"
)

// Done when: after A is merged into B and B into C, lookups by A reach C, and
// reverting the second merge restores the first.
func TestChainedMergesResolveToFinalTargetOverHTTP(t *testing.T) {
	oe := newOpsEnv(t)
	a := oe.create(t, oe.human, "chain-a", "Chain A")
	b := oe.create(t, oe.human, "chain-b", "Chain B")
	c := oe.create(t, oe.human, "chain-c", "Chain C")
	pa := oe.tag(t, "on a", "chain-a")
	oe.applyOp(t, "merge", map[string]any{"sources": []string{"chain-a"}, "target": "chain-b"})
	second := oe.applyOp(t, "merge", map[string]any{"sources": []string{"chain-b"}, "target": "chain-c"})

	// Tagging by A's old slug, A's ID and B's slug all land on C.
	for _, ref := range []string{"chain-a", a.ID, "chain-b"} {
		p := oe.tag(t, "via "+ref, ref)
		if got := oe.tagProblems(t, c.ID); !slices.Contains(got, p) {
			t.Fatalf("tagging via %s: C has %v, want %s", ref, got, p)
		}
	}
	if got := oe.tagProblems(t, c.ID); !slices.Contains(got, pa) {
		t.Fatalf("C lost A's problem: %v", got)
	}
	// Filtering the problem list by A's slug shows C's problems.
	list := decode[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, oe.human.Do("GET", "/problems?tag=chain-a&limit=100", nil), http.StatusOK)
	if want := oe.tagProblems(t, c.ID); len(list.Items) != len(want) {
		t.Fatalf("filter by A returned %d problems, C has %d", len(list.Items), len(want))
	}

	// Revert the second merge: B is active again and A still stands for B.
	decode[opJSON](t, oe.human.Do("POST", "/tag-operations/"+second.ID+"/revert", nil), http.StatusOK)
	if tg, _ := oe.get(t, oe.human, b.ID); tg.Status != "active" {
		t.Fatalf("B after revert = %+v", tg)
	}
	p := oe.tag(t, "after revert", "chain-a")
	if got := oe.tagProblems(t, b.ID); !slices.Contains(got, p) {
		t.Fatalf("after revert B has %v, want %s", got, p)
	}
}
