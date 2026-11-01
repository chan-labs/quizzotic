// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"testing"

	"github.com/chan-labs/quizzotic/internal/problems"
)

func TestSimilarProblemsEndpoint(t *testing.T) {
	e := newProbEnv(t)
	near := e.create(t, e.human, openBody("What is the capital of France?"))
	e.create(t, e.human, openBody("Explain mitochondrial respiration"))
	e.create(t, e.other, openBody("What is the capital of France?"))

	body := map[string]any{"prompt": "what is the capital of france", "answers": []string{"secret reference answer"}, "limit": 5, "min_score": 0.5}
	r := e.req(t, "POST", "/problems/similar", e.agent, body, nil)
	if r.code != http.StatusOK {
		t.Fatalf("similar = %d (%s)", r.code, r.body)
	}
	res := decodeReply[problems.SimilarResult](t, r)
	if len(res.Items) != 1 || res.Items[0].Problem.ID != near.ID || res.Items[0].Score != 1 {
		t.Fatalf("items = %+v, want only the caller's duplicate with score 1", res.Items)
	}

	for name, bad := range map[string]any{
		"missing prompt": map[string]any{"answers": []string{"x"}},
		"limit too big":  map[string]any{"prompt": "x", "limit": 51},
	} {
		if r := e.req(t, "POST", "/problems/similar", e.human, bad, nil); r.code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d (%s), want 422", name, r.code, r.body)
		}
	}
	if r := e.req(t, "POST", "/problems/similar", e.human, `{"prompt":"x","bogus":1}`, nil); r.code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", r.code)
	}
}
