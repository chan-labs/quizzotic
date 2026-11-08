// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/fsrs"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/settings"
	"github.com/chan-labs/quizzotic/internal/store"
)

// tagged creates a live problem carrying one tag.
func (e *probEnv) tagged(t *testing.T, prompt, tagID string) problems.View {
	t.Helper()
	body := openBody(prompt)
	body["status"] = "live"
	p := e.create(t, e.human, body)
	if r := e.req(t, "PUT", "/problems/"+p.ID.String()+"/tags", e.human, map[string]any{"tags": []string{tagID}}, nil); r.code != http.StatusOK {
		t.Fatalf("tag problem = %d (%s)", r.code, r.body)
	}
	return p
}

func (e *probEnv) newTag(t *testing.T, name string) domain.ID {
	t.Helper()
	r := e.req(t, "POST", "/tags", e.human, map[string]any{"name": name}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create tag = %d (%s)", r.code, r.body)
	}
	id, err := domain.ParseID(decodeReply[struct {
		ID string `json:"id"`
	}](t, r).ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestMasteryWiredIntoServer proves the production server installs the mastery
// projection: answering a tagged problem writes tag_mastery in the attempt's
// transaction, and a weak tag then steers which new problems a session serves.
func TestMasteryWiredIntoServer(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()
	// Provision the user before touching the store.
	e.req(t, "GET", "/me", e.human, nil, nil)
	uid, _ := e.userFor(t, "author-sub")
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatal(err)
	}

	weak, strong := e.newTag(t, "weak-topic"), e.newTag(t, "strong-topic")
	pw := e.tagged(t, "Studied weak", weak.String())
	ps := e.tagged(t, "Studied strong", strong.String())

	// Tagging a live problem already counts its card, before any attempt.
	if m, err := e.st.Mastery().Get(ctx, owner, weak); err != nil || m.CardsTotal != 1 || m.CardsSeen != 0 {
		t.Fatalf("tag_mastery before any attempt = %+v (%v), want one unstudied card", m, err)
	}

	for _, p := range []problems.View{pw, ps} {
		if r := e.answer(t, e.human, attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})); r.code != http.StatusCreated {
			t.Fatalf("answer = %d (%s)", r.code, r.body)
		}
	}
	for _, tag := range []domain.ID{weak, strong} {
		m, err := e.st.Mastery().Get(ctx, owner, tag)
		if err != nil {
			t.Fatalf("answering did not write tag_mastery for %s: %v", tag, err)
		}
		if m.CardsSeen != 1 || m.CardsTotal != 1 || m.Reviews != 1 || m.Recall <= 0.9 {
			t.Errorf("mastery after one answer = %+v", m)
		}
	}

	// Let the weak tag's card decay (a year since review) and push both cards
	// out of the due list; the test cannot wait. Refresh through the rebuild.
	wc, err := e.st.Cards().Get(ctx, owner, pw.ID)
	if err != nil {
		t.Fatal(err)
	}
	wc.LastReview, wc.Stability = time.Now().Add(-365*24*time.Hour), 1
	wc.State, wc.Due = fsrs.Review, time.Now().Add(30*24*time.Hour)
	if err := e.st.Cards().Put(ctx, owner, wc); err != nil {
		t.Fatal(err)
	}
	sc, err := e.st.Cards().Get(ctx, owner, ps.ID)
	if err != nil {
		t.Fatal(err)
	}
	sc.Due = time.Now().Add(30 * 24 * time.Hour)
	if err := e.st.Cards().Put(ctx, owner, sc); err != nil {
		t.Fatal(err)
	}
	if err := store.NewMastery(store.DefaultCards(), domain.SystemClock{}).Service(e.st).RebuildUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	mw, _ := e.st.Mastery().Get(ctx, owner, weak)
	ms, _ := e.st.Mastery().Get(ctx, owner, strong)
	if !(mw.Recall < 0.5 && ms.Recall > 0.9) {
		t.Fatalf("weak recall %.2f, strong recall %.2f; want weak < 0.5 and strong > 0.9", mw.Recall, ms.Recall)
	}

	// One new problem per tag; a strong weak_tag_boost makes the weak one far
	// likelier to be drawn first. Without tag recall (the stub's 0.5 for all)
	// it would be a coin flip.
	nw := e.tagged(t, "New weak", weak.String())
	e.tagged(t, "New strong", strong.String())
	boost := 100.0
	if err := e.st.Settings().Replace(ctx, owner, settings.Overrides{WeakTagBoost: &boost}); err != nil {
		t.Fatal(err)
	}
	const n = 20
	weakFirst := 0
	for range n {
		r := e.req(t, "POST", "/sessions", e.human, map[string]any{"kind": "review"}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("POST /sessions = %d (%s)", r.code, r.body)
		}
		s := decodeReply[sessionJSON](t, r)
		if len(s.Items) != 2 {
			t.Fatalf("session items = %d, want the two new problems", len(s.Items))
		}
		if s.Items[0].ProblemID == nw.ID.String() {
			weakFirst++
		}
	}
	if weakFirst < 16 {
		t.Errorf("weak-tag problem drawn first in %d/%d sessions, want >= 16 (a coin flip would give ~10)", weakFirst, n)
	}
}

// Archiving a problem takes its card out of review and so out of the tag's
// mastery, in the same transaction.
func TestMasteryRefreshedOnArchive(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()
	e.req(t, "GET", "/me", e.human, nil, nil)
	uid, _ := e.userFor(t, "author-sub")
	owner, err := domain.ParseID(uid)
	if err != nil {
		t.Fatal(err)
	}
	tag := e.newTag(t, "archive-topic")
	p := e.tagged(t, "To be archived", tag.String())
	if r := e.answer(t, e.human, attemptBody(p, map[string]any{"text": "x"}, map[string]any{"rating": 3})); r.code != http.StatusCreated {
		t.Fatalf("answer = %d (%s)", r.code, r.body)
	}
	if m, err := e.st.Mastery().Get(ctx, owner, tag); err != nil || m.CardsSeen != 1 || m.CardsTotal != 1 {
		t.Fatalf("mastery before archive = %+v (%v)", m, err)
	}
	cur := e.req(t, "GET", "/problems/"+p.ID.String(), e.human, nil, nil)
	r := e.req(t, "POST", "/problems/"+p.ID.String()+"/archive", e.human, nil, map[string]string{"If-Match": cur.header.Get("ETag")})
	if r.code != http.StatusOK {
		t.Fatalf("archive = %d (%s)", r.code, r.body)
	}
	m, err := e.st.Mastery().Get(ctx, owner, tag)
	if err != nil || m.CardsSeen != 0 || m.CardsTotal != 0 {
		t.Errorf("mastery after archive = %+v (%v), want no studied or live cards", m, err)
	}
}
