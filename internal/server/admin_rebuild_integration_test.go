// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/chan-labs/quizzotic/internal/domain"
)

func init() {
	ownerScope.Exempt("POST /admin/rebuild",
		"admin only: instance-wide by design (see spec, Auth scopes); it takes no data from a user and returns only counts")
}

type rebuildBody struct {
	Users int `json:"users"`
	Cards int `json:"cards"`
}

// answeredCard has the human answer a live open problem, so one card exists.
func (e *probEnv) answeredCard(t *testing.T, tok string) domain.ID {
	t.Helper()
	live := openBody("Rebuild me " + domain.NewIDGenerator(domain.SystemClock{}).New().String())
	live["status"] = "live"
	p := e.create(t, tok, live)
	if r := e.answer(t, tok, attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})); r.code != http.StatusCreated {
		t.Fatalf("answer = %d (%s)", r.code, r.body)
	}
	return p.ID
}

func TestAdminRebuild(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()
	a := e.answeredCard(t, e.human)
	b := e.answeredCard(t, e.other)
	_, au := e.userFor(t, "author-sub")
	_, bu := e.userFor(t, "other-sub")
	cards := e.st.Cards()

	before, err := cards.List(ctx, au.ID)
	if err != nil || len(before) != 1 {
		t.Fatalf("cards before = %d, %v", len(before), err)
	}
	// Corrupt both users' projections; the rebuild must restore them.
	for _, u := range []domain.ID{au.ID, bu.ID} {
		if err := cards.DeleteAll(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("one user", func(t *testing.T) {
		r := e.req(t, "POST", "/admin/rebuild", e.human, map[string]any{"user_id": au.ID.String()}, nil)
		if r.code != http.StatusOK {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		if got := decodeReply[rebuildBody](t, r); got != (rebuildBody{Users: 1, Cards: 1}) {
			t.Errorf("result = %+v", got)
		}
		if _, err := cards.Get(ctx, au.ID, a); err != nil {
			t.Errorf("user A's card not rebuilt: %v", err)
		}
		if l, _ := cards.List(ctx, bu.ID); len(l) != 0 {
			t.Errorf("user B was rebuilt by a request for A: %d cards", len(l))
		}
	})

	t.Run("everyone, no body", func(t *testing.T) {
		r := e.req(t, "POST", "/admin/rebuild", e.human, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
		if got := decodeReply[rebuildBody](t, r); got != (rebuildBody{Users: 2, Cards: 2}) {
			t.Errorf("result = %+v", got)
		}
		after, _ := cards.List(ctx, au.ID)
		if !reflect.DeepEqual(after, before) {
			t.Errorf("rebuilt card differs from the original\n before %+v\n after  %+v", before, after)
		}
		if _, err := cards.Get(ctx, bu.ID, b); err != nil {
			t.Errorf("user B's card not rebuilt: %v", err)
		}
	})

	t.Run("empty object", func(t *testing.T) {
		if r := e.req(t, "POST", "/admin/rebuild", e.human, map[string]any{}, nil); r.code != http.StatusOK {
			t.Fatalf("status = %d (%s)", r.code, r.body)
		}
	})

	t.Run("unknown and malformed user", func(t *testing.T) {
		unknown := domain.NewIDGenerator(domain.SystemClock{}).New().String()
		if r := e.req(t, "POST", "/admin/rebuild", e.human, map[string]any{"user_id": unknown}, nil); r.code != http.StatusNotFound {
			t.Errorf("unknown user = %d (%s)", r.code, r.body)
		}
		if r := e.req(t, "POST", "/admin/rebuild", e.human, map[string]any{"user_id": "nope"}, nil); r.code != http.StatusUnprocessableEntity {
			t.Errorf("malformed user = %d (%s)", r.code, r.body)
		}
		if r := e.req(t, "POST", "/admin/rebuild", e.human, "{bad", nil); r.code != http.StatusBadRequest {
			t.Errorf("malformed body = %d (%s)", r.code, r.body)
		}
	})

	t.Run("agents and anonymous callers are refused", func(t *testing.T) {
		claimed := e.idp.access("quizzotic-agent", "author-sub", with("scope", "problems:read admin"))
		for name, tok := range map[string]string{"agent": e.agent, "agent claiming admin": claimed} {
			r := e.req(t, "POST", "/admin/rebuild", tok, nil, nil)
			if r.code != http.StatusForbidden || r.header.Get("Content-Type") != "application/problem+json" {
				t.Errorf("%s: status = %d, content-type = %q (%s)", name, r.code, r.header.Get("Content-Type"), r.body)
			}
		}
		if r := e.req(t, "POST", "/admin/rebuild", "", nil, nil); r.code != http.StatusUnauthorized {
			t.Errorf("anonymous = %d", r.code)
		}
	})
}
