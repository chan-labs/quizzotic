// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/store"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/tags"
	"github.com/chan-labs/quizzotic/internal/web"
)

type probEnv struct {
	t     *testing.T
	mux   *http.ServeMux
	svc   *problems.Service
	tags  *tags.Service
	owner domain.ID
}

func newProbEnv(t *testing.T) *probEnv {
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
	tagSvc := tags.NewService(store.TagsTx(st), clock, ids)
	svc := problems.NewService(st.Problems(), store.ProblemsTx(st), clock, ids, problems.WithTagResolver(tagSvc))

	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := r.Cookie("ok"); err != nil {
				web.RedirectToLogin(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), auth.WebSession{UserID: u.ID, CSRFToken: testCSRF})))
		})
	}
	site, err := web.New(web.Config{Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	site.UseProblems(svc, tagSvc)
	site.UseTagOps(tagSvc)
	mux := http.NewServeMux()
	site.Register(mux)
	return &probEnv{t: t, mux: mux, svc: svc, tags: tagSvc, owner: u.ID}
}

func (e *probEnv) do(method, path string, form url.Values, csrf bool) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if method == "POST" {
		if csrf {
			form.Set("csrf_token", testCSRF)
		}
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(&http.Cookie{Name: "ok", Value: "1"})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

var (
	etagRe = regexp.MustCompile(`name="etag" value="([^"]*)"`)
	idRe   = regexp.MustCompile(`^/problems/([0-9a-z-]+)\?saved=`)
)

func etagOf(t *testing.T, body string) string {
	t.Helper()
	m := etagRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no etag field in page:\n%s", body)
	}
	return html.UnescapeString(m[1])
}

// create posts form to /problems, expects the redirect, and returns the ID.
func (e *probEnv) create(form url.Values) domain.ID {
	e.t.Helper()
	rec := e.do("POST", "/problems", form, true)
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("create = %d\n%s", rec.Code, rec.Body)
	}
	m := idRe.FindStringSubmatch(rec.Header().Get("Location"))
	if m == nil {
		e.t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	id, err := domain.ParseID(m[1])
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *probEnv) view(id domain.ID) problems.View {
	e.t.Helper()
	v, err := e.svc.Get(context.Background(), e.owner, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func mcqForm() url.Values {
	return url.Values{"type": {"mcq"}, "prompt": {"Capital of France?"}, "status": {"live"},
		"choice_id": {"", "", "", ""}, "choice_text": {"Paris", "Rome", "", "Oslo"}, "correct": {"0"}}
}

func clozeForm() url.Values {
	return url.Values{"type": {"cloze"}, "prompt": {"The sky is {{c1}}."},
		"blank_id": {"c1", ""}, "blank_accepted": {"blue\nazure", ""}, "blank_edits": {"1", "0"}}
}

func openForm() url.Values {
	return url.Values{"type": {"open"}, "prompt": {"Explain **TCP**."}, "reference_answer": {"A reliable transport."},
		"rubric": {"mentions reliability\nmentions ordering"}}
}

func TestCreateEachTypeThroughUI(t *testing.T) {
	e := newProbEnv(t)
	for _, tc := range []struct {
		name string
		form url.Values
		typ  string
		want string // substring of the stored payload
	}{
		{"mcq", mcqForm(), problems.TypeMCQ, `"correct":["c1"]`},
		{"cloze", clozeForm(), problems.TypeCloze, `"accepted":["blue","azure"]`},
		{"open", openForm(), problems.TypeOpen, `"rubric":["mentions reliability","mentions ordering"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := e.do("GET", "/problems/new?type="+tc.typ, nil, false)
			if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="csrf_token" value="`+testCSRF+`"`) {
				t.Fatalf("new page = %d", page.Code)
			}
			id := e.create(tc.form)
			v := e.view(id)
			if v.Type != tc.typ || v.Version != 1 || !strings.Contains(string(v.Payload), tc.want) {
				t.Fatalf("stored %+v payload %s, want %s", v, v.Payload, tc.want)
			}
			edit := e.do("GET", "/problems/"+id.String(), nil, false)
			if edit.Code != http.StatusOK || !strings.Contains(edit.Body.String(), "Edit problem") {
				t.Fatalf("edit page = %d", edit.Code)
			}
		})
	}
}

func TestClozeMatchModeThroughUI(t *testing.T) {
	e := newProbEnv(t)

	// No blank_match (an old form): relaxed, and the legacy checkbox is ignored.
	legacy := clozeForm()
	legacy["blank_case"] = []string{"0"}
	v := e.view(e.create(legacy))
	if !strings.Contains(string(v.Payload), `"match":"relaxed"`) || strings.Contains(string(v.Payload), "case_sensitive") {
		t.Fatalf("legacy form payload %s", v.Payload)
	}

	f := clozeForm()
	f["blank_match"] = []string{"case-insensitive", "relaxed"}
	id := e.create(f)
	v = e.view(id)
	if !strings.Contains(string(v.Payload), `"match":"case-insensitive"`) {
		t.Fatalf("payload %s", v.Payload)
	}
	page := e.do("GET", "/problems/"+id.String(), nil, false).Body.String()
	if !strings.Contains(page, `<option value="case-insensitive" selected>`) {
		t.Errorf("edit page does not preselect case-insensitive:\n%s", page)
	}
	if strings.Contains(page, `name="blank_case"`) {
		t.Error("the case-sensitive checkbox is still rendered")
	}

	// The default for a fresh row is relaxed.
	fresh := e.do("GET", "/problems/new?type=cloze", nil, false).Body.String()
	if !strings.Contains(fresh, `<option value="relaxed" selected>`) || strings.Contains(fresh, `<option value="case-insensitive" selected>`) {
		t.Errorf("new cloze form should default to relaxed:\n%s", fresh)
	}
}

func TestMCQCorrectChoiceFollowsRowIndex(t *testing.T) {
	e := newProbEnv(t)
	id := e.create(mcqForm())
	v := e.view(id)
	// Row 2 was empty and dropped; the generated ids must not collide.
	if !strings.Contains(string(v.Payload), `"choices":[{"id":"c1","text":"Paris"},{"id":"c2","text":"Rome"},{"id":"c3","text":"Oslo"}]`) {
		t.Fatalf("payload %s", v.Payload)
	}
}

func TestEditEachTypeKeepsOrChangesCardEpoch(t *testing.T) {
	e := newProbEnv(t)
	for _, tc := range []struct {
		name string
		form url.Values
		edit func(url.Values)
		kind string
		want string
	}{
		{"mcq minor", mcqForm(), func(f url.Values) { f.Set("prompt", "Capital city of France?") }, "minor", "Capital city of France?"},
		{"cloze major", clozeForm(), func(f url.Values) { f.Set("blank_accepted", "cyan\n") }, "major", `"accepted":["cyan"]`},
		{"open minor", openForm(), func(f url.Values) { f.Set("reference_answer", "Reliable, ordered delivery.") }, "minor", "Reliable, ordered delivery."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := e.create(tc.form)
			page := e.do("GET", "/problems/"+id.String(), nil, false).Body.String()
			form := url.Values{}
			for k, v := range tc.form {
				form[k] = v
			}
			// Re-submit the stored ids so edits keep stable choice ids.
			if tc.form.Get("type") == "mcq" {
				form["choice_id"] = []string{"c1", "c2", "", "c3"}
			}
			form.Set("etag", etagOf(t, page))
			form.Set("change_kind", tc.kind)
			tc.edit(form)
			rec := e.do("POST", "/problems/"+id.String(), form, true)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("edit = %d\n%s", rec.Code, rec.Body)
			}
			v := e.view(id)
			if v.Version != 2 || !strings.Contains(v.Prompt+string(v.Payload), tc.want) {
				t.Fatalf("after edit %+v payload %s", v, v.Payload)
			}
			vs, err := e.svc.Versions(context.Background(), e.owner, id)
			if err != nil || len(vs) != 2 || string(vs[1].ChangeKind) != tc.kind {
				t.Fatalf("versions %+v err %v, want second change_kind %s", vs, err, tc.kind)
			}
		})
	}
}

func TestStaleEditShowsConflict(t *testing.T) {
	e := newProbEnv(t)
	id := e.create(openForm())
	path := "/problems/" + id.String()
	openA := etagOf(t, e.do("GET", path, nil, false).Body.String())
	openB := etagOf(t, e.do("GET", path, nil, false).Body.String())

	a := openForm()
	a.Set("etag", openA)
	a.Set("change_kind", "minor")
	a.Set("prompt", "First tab's wording.")
	if rec := e.do("POST", path, a, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("first edit = %d", rec.Code)
	}

	b := openForm()
	b.Set("etag", openB)
	b.Set("change_kind", "minor")
	b.Set("prompt", "Second tab's wording.")
	rec := e.do("POST", path, b, true)
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, "Edit conflict") {
		t.Fatalf("stale edit = %d, want 409 with conflict message\n%s", rec.Code, body)
	}
	if !strings.Contains(body, "Second tab&#39;s wording.") {
		t.Errorf("conflict page lost the user's input")
	}
	if v := e.view(id); v.Prompt != "First tab's wording." || v.Version != 2 {
		t.Errorf("stale edit changed the problem: %+v", v)
	}

	// A form without an etag must not bypass the check.
	b.Del("etag")
	if rec := e.do("POST", path, b, true); rec.Code != http.StatusConflict {
		t.Errorf("edit without etag = %d, want 409", rec.Code)
	}
}

func TestValidationErrorsKeepInput(t *testing.T) {
	e := newProbEnv(t)
	f := mcqForm()
	f.Set("correct", "") // nothing ticked
	f.Del("correct")
	rec := e.do("POST", "/problems", f, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "payload.correct") ||
		!strings.Contains(rec.Body.String(), "Capital of France?") {
		t.Fatalf("create invalid = %d\n%s", rec.Code, rec.Body)
	}
}

func TestPostsNeedCSRFToken(t *testing.T) {
	e := newProbEnv(t)
	if rec := e.do("POST", "/problems", mcqForm(), false); rec.Code != http.StatusForbidden {
		t.Errorf("create without token = %d", rec.Code)
	}
	if rec := e.do("POST", "/problems/preview", mcqForm(), false); rec.Code != http.StatusForbidden {
		t.Errorf("preview without token = %d", rec.Code)
	}
	id := e.create(openForm())
	if rec := e.do("POST", "/problems/"+id.String(), openForm(), false); rec.Code != http.StatusForbidden {
		t.Errorf("edit without token = %d", rec.Code)
	}
	for _, p := range []string{"/problems/new?type=open", "/problems/" + id.String()} {
		body := e.do("GET", p, nil, false).Body.String()
		if strings.Count(body, "<form") != strings.Count(body, `name="csrf_token" value="`+testCSRF+`"`) {
			t.Errorf("%s: a form lacks the CSRF field", p)
		}
	}
}

func TestPagesRequireSession(t *testing.T) {
	e := newProbEnv(t)
	req := httptest.NewRequest("GET", "/problems", nil)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("anonymous list = %d", rec.Code)
	}
}

func TestTagPickerAndListFilters(t *testing.T) {
	e := newProbEnv(t)
	ctx := context.Background()
	if _, err := e.tags.Create(ctx, e.owner, tags.CreateInput{Name: "Geography"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tags.Create(ctx, e.owner, tags.CreateInput{Name: "Networking"}); err != nil {
		t.Fatal(err)
	}
	page := e.do("GET", "/problems/new?type=mcq", nil, false).Body.String()
	if !strings.Contains(page, `name="tags" value="geography"`) {
		t.Fatalf("tag picker missing:\n%s", page)
	}

	geo := mcqForm()
	geo["tags"] = []string{"geography"}
	gid := e.create(geo)
	net := openForm()
	net["tags"] = []string{"networking"}
	e.create(net)

	got, err := e.tags.ProblemTags(ctx, e.owner, gid)
	if err != nil || len(got) != 1 || got[0].Slug != "geography" {
		t.Fatalf("tags on problem = %+v, %v", got, err)
	}
	if !strings.Contains(e.do("GET", "/problems/"+gid.String(), nil, false).Body.String(), `value="geography" checked`) {
		t.Errorf("edit page does not tick the problem's tag")
	}

	list := func(q string) string {
		rec := e.do("GET", "/problems?"+q, nil, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s = %d\n%s", q, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if b := list("tag=geography"); !strings.Contains(b, "Capital of France?") || strings.Contains(b, "Explain") {
		t.Errorf("tag filter wrong:\n%s", b)
	}
	if b := list("type=open"); strings.Contains(b, "Capital of France?") || !strings.Contains(b, "Explain") {
		t.Errorf("type filter wrong")
	}
	if b := list("q=capital"); !strings.Contains(b, "Capital of France?") || strings.Contains(b, "Explain") {
		t.Errorf("q filter wrong")
	}
	if b := list("status=draft"); !strings.Contains(b, "Explain") || strings.Contains(b, "Capital of France?") {
		t.Errorf("status filter wrong")
	}
	if rec := e.do("GET", "/problems?tag=nope", nil, false); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no such tag") {
		t.Errorf("unknown tag = %d", rec.Code)
	}
}

func TestPreviewRendersMarkdownInert(t *testing.T) {
	e := newProbEnv(t)
	f := clozeForm()
	f.Set("prompt", "**bold** {{c1}} <script>alert(1)</script>")
	rec := e.do("POST", "/problems/preview", f, true)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "<strong>bold</strong>") ||
		!strings.Contains(body, `class="blank"`) || strings.Contains(body, "<script") || strings.Contains(body, "{{c1}}") {
		t.Fatalf("preview = %d\n%s", rec.Code, body)
	}
	mc := e.do("POST", "/problems/preview", mcqForm(), true).Body.String()
	if !strings.Contains(mc, "Paris") || !strings.Contains(mc, "Oslo") {
		t.Errorf("mcq preview lacks choices: %s", mc)
	}
}

func TestEditorScriptServedFromStatic(t *testing.T) {
	e := newProbEnv(t)
	page := e.do("GET", "/problems/new", nil, false)
	if !strings.Contains(page.Body.String(), `src="/static/problem-editor.js"`) || strings.Contains(page.Body.String(), "<script>") {
		t.Errorf("editor must load only the external script")
	}
	if rec := e.do("GET", "/static/problem-editor.js", nil, false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/problems/preview") {
		t.Errorf("script = %d", rec.Code)
	}
}

func TestOtherOwnersProblemIs404(t *testing.T) {
	e := newProbEnv(t)
	if rec := e.do("GET", "/problems/"+domain.NewIDGenerator(domain.SystemClock{}).New().String(), nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown problem = %d", rec.Code)
	}
}
