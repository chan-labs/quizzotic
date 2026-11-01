// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
)

func init() {
	// The batch creates new rows for the caller and takes no ID. Tag
	// references in it are resolved for the caller only, which
	// TestBatchCreateProblems proves with another user's tag.
	ownerScope.Exempt("POST /problems/batch",
		"creates new rows owned by the caller; tag refs resolve only among the caller's tags (tested)")
}

func batchOf(n int, tags ...string) map[string]any {
	items := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		it := openBody(fmt.Sprintf("Batch problem %d", i))
		if len(tags) > 0 {
			it["tags"] = tags
		}
		items = append(items, it)
	}
	return map[string]any{"items": items}
}

type batchReply struct {
	Items []problems.View `json:"items"`
}

func TestBatchCreateProblems(t *testing.T) {
	e := newProbEnv(t)
	mkTag := func(tok, slug string) {
		t.Helper()
		if r := e.req(t, "POST", "/tags", tok, map[string]string{"slug": slug, "name": slug}, nil); r.code != http.StatusCreated {
			t.Fatalf("create tag = %d (%s)", r.code, r.body)
		}
	}
	mkTag(e.human, "biology")
	mkTag(e.human, "chemistry")
	mkTag(e.other, "physics")
	count := func() int {
		uid, _ := e.userFor(t, "author-sub")
		owner, _ := domain.ParseID(uid)
		rows, err := e.st.Problems().List(context.Background(), owner, problems.ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}

	t.Run("all valid items are created with their tags, in order", func(t *testing.T) {
		body := batchOf(3, "biology")
		body["items"].([]map[string]any)[1]["tags"] = []string{"chemistry", "biology", "biology"}
		body["items"].([]map[string]any)[2]["status"] = "live"
		r := e.req(t, "POST", "/problems/batch", e.agent, body, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		got := decodeReply[batchReply](t, r).Items
		if len(got) != 3 {
			t.Fatalf("items = %d", len(got))
		}
		for i, v := range got {
			if want := fmt.Sprintf("Batch problem %d", i); v.Prompt != want {
				t.Errorf("item %d prompt = %q, want %q", i, v.Prompt, want)
			}
		}
		if got[0].Status != problems.StatusDraft || got[2].Status != problems.StatusLive {
			t.Errorf("statuses = %s, %s", got[0].Status, got[2].Status)
		}
		if got[0].CreatedBy.Kind != "agent" {
			t.Errorf("created_by = %+v", got[0].CreatedBy)
		}
		if n := count(); n != 3 {
			t.Errorf("stored = %d, want 3", n)
		}
		for tag, want := range map[string]int{"biology": 3, "chemistry": 1} {
			if page := e.list(t, e.human, url.Values{"tag": {tag}}); len(page.Items) != want {
				t.Errorf("tag %s: %d problems, want %d", tag, len(page.Items), want)
			}
		}
	})

	t.Run("one invalid item writes nothing and names its index", func(t *testing.T) {
		before := count()
		body := batchOf(4, "biology")
		items := body["items"].([]map[string]any)
		items[1]["payload"] = map[string]any{"choices": []string{}}
		items[1]["type"] = "mcq"
		items[3]["tags"] = []string{"biology", "no-such-tag"}
		r := e.req(t, "POST", "/problems/batch", e.human, body, nil)
		pe := r.wantProblem(t, 422, "validation")
		var onItem1, onItem3, other int
		for _, fe := range pe.Errors {
			switch {
			case strings.HasPrefix(fe.Field, "items[1]."):
				onItem1++
			case fe.Field == "items[3].tags[1]":
				onItem3++
			default:
				other++
				t.Errorf("unexpected error %+v", fe)
			}
		}
		if onItem1 == 0 || onItem3 != 1 || other != 0 {
			t.Errorf("errors = %+v", pe.Errors)
		}
		if n := count(); n != before {
			t.Errorf("stored = %d, want %d (nothing written)", n, before)
		}
		if page := e.list(t, e.human, url.Values{"tag": {"biology"}}); len(page.Items) != 3 {
			t.Errorf("biology problems = %d, want the 3 from before", len(page.Items))
		}
	})

	t.Run("another user's tag is unknown", func(t *testing.T) {
		before := count()
		r := e.req(t, "POST", "/problems/batch", e.human, batchOf(1, "physics"), nil)
		if pe := r.wantProblem(t, 422, "validation"); len(pe.Errors) != 1 || pe.Errors[0].Field != "items[0].tags[0]" {
			t.Errorf("errors = %+v", pe.Errors)
		}
		if n := count(); n != before {
			t.Errorf("stored = %d, want %d", n, before)
		}
	})

	t.Run("100 items are accepted", func(t *testing.T) {
		before := count()
		r := e.req(t, "POST", "/problems/batch", e.human, batchOf(problems.MaxBatch, "biology"), nil)
		if r.code != http.StatusCreated {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		if n := count(); n != before+problems.MaxBatch {
			t.Errorf("stored = %d, want %d", n, before+problems.MaxBatch)
		}
	})

	t.Run("101 items are rejected and nothing is written", func(t *testing.T) {
		before := count()
		r := e.req(t, "POST", "/problems/batch", e.human, batchOf(problems.MaxBatch+1, "biology"), nil)
		if pe := r.wantProblem(t, 422, "validation"); len(pe.Errors) != 1 || pe.Errors[0].Field != "items" {
			t.Errorf("errors = %+v", pe.Errors)
		}
		if n := count(); n != before {
			t.Errorf("stored = %d, want %d", n, before)
		}
	})

	t.Run("an empty batch is rejected", func(t *testing.T) {
		e.req(t, "POST", "/problems/batch", e.human, map[string]any{"items": []any{}}, nil).wantProblem(t, 422, "validation")
	})

	t.Run("unknown field is a 400", func(t *testing.T) {
		e.req(t, "POST", "/problems/batch", e.human, map[string]any{"items": []any{}, "x": 1}, nil).wantProblem(t, 400, "bad-request")
	})

	t.Run("Idempotency-Key replays the original batch", func(t *testing.T) {
		before := count()
		key := map[string]string{"Idempotency-Key": "batch-key-1"}
		body := batchOf(2, "chemistry")
		first := e.req(t, "POST", "/problems/batch", e.agent, body, key)
		if first.code != http.StatusCreated {
			t.Fatalf("first = %d (%s)", first.code, first.body)
		}
		replay := e.req(t, "POST", "/problems/batch", e.agent, body, key)
		if replay.code != http.StatusCreated || string(replay.body) != string(first.body) ||
			replay.header.Get("Idempotency-Replayed") != "true" {
			t.Errorf("replay = %d %s (replayed=%q)", replay.code, replay.body, replay.header.Get("Idempotency-Replayed"))
		}
		if n := count(); n != before+2 {
			t.Errorf("stored = %d, want %d", n, before+2)
		}
	})

	t.Run("an unauthenticated request is refused", func(t *testing.T) {
		// Unauthenticated requests never reach the handler.
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/problems/batch", strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})
}
