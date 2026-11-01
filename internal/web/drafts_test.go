// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/web"
)

type draftsEnv struct {
	mux   *http.ServeMux
	svc   *problems.Service
	owner domain.ID
	other domain.ID
}

func newDraftsEnv(t *testing.T) draftsEnv {
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
	var users [2]domain.ID
	for i := range users {
		u := auth.User{ID: ids.New(), DisplayName: "U", Email: "u" + string(rune('a'+i)) + "@example.com", CreatedAt: clock.Now()}
		if err := st.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		users[i] = u.ID
	}
	svc := problems.NewService(st.Problems(), store.ProblemsTx(st), clock, ids)

	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := r.Cookie("ok"); err != nil {
				web.RedirectToLogin(w, r)
				return
			}
			ws := auth.WebSession{UserID: users[0], CSRFToken: testCSRF}
			next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), ws)))
		})
	}
	site, err := web.New(web.Config{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	site.UseDrafts(svc)
	mux := http.NewServeMux()
	site.Register(mux)
	return draftsEnv{mux: mux, svc: svc, owner: users[0], other: users[1]}
}

func (e draftsEnv) create(t *testing.T, owner domain.ID, kind domain.ActorKind, prompt string) problems.View {
	t.Helper()
	v, err := e.svc.Create(context.Background(), domain.Actor{Kind: kind, UserID: owner, ClientID: "c"}, problems.CreateInput{
		Type: problems.TypeOpen, Prompt: prompt, Payload: []byte(`{"reference_answer":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (e draftsEnv) post(path string, form url.Values, token string) *httptest.ResponseRecorder {
	if token != "" {
		form.Set("csrf_token", token)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ok", Value: "1"})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e draftsEnv) count(t *testing.T, st problems.Status) int {
	t.Helper()
	res, err := e.svc.List(context.Background(), e.owner, problems.ListQuery{Status: string(st)})
	if err != nil {
		t.Fatal(err)
	}
	return len(res.Items)
}

func TestDraftsListShowsAuthorKind(t *testing.T) {
	e := newDraftsEnv(t)
	e.create(t, e.owner, domain.ActorAgent, "agent made")
	e.create(t, e.owner, domain.ActorHuman, "human made")
	e.create(t, e.other, domain.ActorAgent, "someone elses")

	body := get(e.mux, "/drafts", true).Body.String()
	for _, want := range []string{"agent made", "human made", `author-agent`, `author-human`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "someone elses") {
		t.Error("another owner's draft is listed")
	}
	forms, tokens := strings.Count(body, "<form"), strings.Count(body, `name="csrf_token" value="`+testCSRF+`"`)
	if forms-1 != tokens { // the filter form is a GET and carries no token
		t.Errorf("forms = %d, csrf fields = %d", forms, tokens)
	}
	agentOnly := get(e.mux, "/drafts?kind=agent", true).Body.String()
	if strings.Contains(agentOnly, "human made") || !strings.Contains(agentOnly, "agent made") {
		t.Error("author filter did not narrow the list")
	}
}

// TestBulkPublishMakesTenAgentDraftsEligible is the task's Done-when: only live
// problems get cards, so 10 published agent drafts become eligible for review.
func TestBulkPublishMakesTenAgentDraftsEligible(t *testing.T) {
	e := newDraftsEnv(t)
	form := url.Values{"action": {"publish"}}
	for i := 0; i < 10; i++ {
		v := e.create(t, e.owner, domain.ActorAgent, "agent draft "+string(rune('A'+i)))
		form.Add("id", v.ID.String())
	}
	untouched := e.create(t, e.owner, domain.ActorHuman, "stays a draft")
	if got := e.count(t, problems.StatusDraft); got != 11 {
		t.Fatalf("drafts before = %d", got)
	}

	rec := e.post("/drafts/bulk", form, testCSRF)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "done=10") {
		t.Fatalf("status = %d, Location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := e.count(t, problems.StatusLive); got != 10 {
		t.Fatalf("live after publish = %d, want 10", got)
	}
	v, err := e.svc.Get(context.Background(), e.owner, untouched.ID)
	if err != nil || v.Status != problems.StatusDraft {
		t.Fatalf("unselected draft = %v, %v", v.Status, err)
	}
}

func TestBulkDiscardDeletesDraftsOnly(t *testing.T) {
	e := newDraftsEnv(t)
	d := e.create(t, e.owner, domain.ActorAgent, "discard me")
	live := e.create(t, e.owner, domain.ActorAgent, "already live")
	if _, err := e.svc.Publish(context.Background(), e.owner, live.ID, "*"); err != nil {
		t.Fatal(err)
	}
	theirs := e.create(t, e.other, domain.ActorAgent, "not mine")

	rec := e.post("/drafts/bulk", url.Values{"action": {"discard"}, "id": {d.ID.String(), live.ID.String(), theirs.ID.String()}}, testCSRF)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "done=1") || !strings.Contains(loc, "failed=2") {
		t.Fatalf("Location = %q", loc)
	}
	if _, err := e.svc.Get(context.Background(), e.owner, d.ID); err == nil {
		t.Error("draft not discarded")
	}
	if v, err := e.svc.Get(context.Background(), e.owner, live.ID); err != nil || v.Status != problems.StatusLive {
		t.Errorf("live problem was touched: %v %v", v.Status, err)
	}
	if _, err := e.svc.Get(context.Background(), e.other, theirs.ID); err != nil {
		t.Errorf("other owner's draft was touched: %v", err)
	}
}

func TestDraftPostsRequireCSRF(t *testing.T) {
	e := newDraftsEnv(t)
	v := e.create(t, e.owner, domain.ActorAgent, "keep")
	for _, path := range []string{"/drafts/bulk", "/drafts/" + v.ID.String() + "/edit"} {
		for _, token := range []string{"", "wrong"} {
			rec := e.post(path, url.Values{"action": {"discard"}, "id": {v.ID.String()}, "prompt": {"x"}}, token)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s token %q: status = %d", path, token, rec.Code)
			}
		}
	}
	if e.count(t, problems.StatusDraft) != 1 {
		t.Error("draft changed without a CSRF token")
	}
}

func TestInlineEditSavesNewVersion(t *testing.T) {
	e := newDraftsEnv(t)
	v := e.create(t, e.owner, domain.ActorAgent, "old prompt")
	rec := e.post("/drafts/"+v.ID.String()+"/edit", url.Values{"prompt": {"new prompt"}, "explanation": {"why"}, "source": {""}, "payload": {""}}, testCSRF)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got, _ := e.svc.Get(context.Background(), e.owner, v.ID)
	if got.Prompt != "new prompt" || got.Explanation != "why" || got.Version != 2 || got.CreatedBy.Kind != domain.ActorAgent {
		t.Fatalf("after edit: %+v", got)
	}

	bad := e.post("/drafts/"+v.ID.String()+"/edit", url.Values{"prompt": {" "}}, testCSRF)
	if bad.Code != http.StatusUnprocessableEntity || !strings.Contains(bad.Body.String(), "required") {
		t.Fatalf("invalid edit: status = %d", bad.Code)
	}
}
