// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// ProblemService is what the Problems pages need from the problems feature.
// *problems.Service implements it.
type ProblemService interface {
	List(ctx context.Context, ownerID domain.ID, q problems.ListQuery) (platform.PageResult[problems.View], error)
	Get(ctx context.Context, ownerID, id domain.ID) (problems.View, error)
	Create(ctx context.Context, actor domain.Actor, in problems.CreateInput) (problems.View, error)
	Edit(ctx context.Context, actor domain.Actor, id domain.ID, ifMatch string, in problems.EditInput) (problems.View, error)
}

// TagService is what the tag picker needs from the tags feature.
// *tags.Service implements it.
type TagService interface {
	List(ctx context.Context, ownerID domain.ID, status tags.Status, p platform.Page) ([]tags.Tag, string, error)
	ProblemTags(ctx context.Context, ownerID, problemID domain.ID) ([]tags.Tag, error)
	SetProblemTags(ctx context.Context, ownerID, problemID domain.ID, refs []string) ([]tags.Tag, error)
}

type problemPages struct {
	problems ProblemService
	tags     TagService // nil: no tag picker
	preview  *template.Template
}

// UseProblems enables the Problems pages. Call it before Register. tagSvc may
// be nil, which hides the tag picker and filter.
func (w *Web) UseProblems(p ProblemService, tagSvc TagService) {
	w.prob.problems = p
	w.prob.tags = tagSvc
}

func (w *Web) parseProblemTemplates(layout *template.Template) error {
	for _, name := range []string{"problems", "problem_edit"} {
		t, err := layout.Clone()
		if err != nil {
			return err
		}
		if _, err := t.ParseFS(templateFS, "templates/"+name+".html", "templates/problem_preview.html"); err != nil {
			return err
		}
		w.tmpl[name] = t
	}
	p, err := template.ParseFS(templateFS, "templates/problem_preview.html")
	if err != nil {
		return err
	}
	w.prob.preview = p
	return nil
}

func (w *Web) registerProblems(mux *http.ServeMux) {
	if w.prob.problems == nil {
		return
	}
	mux.Handle("GET /problems", w.gate(http.HandlerFunc(w.problemList)))
	mux.Handle("GET /problems/new", w.gate(http.HandlerFunc(w.problemNew)))
	mux.Handle("POST /problems", w.gate(w.csrf(http.HandlerFunc(w.problemCreate))))
	mux.Handle("POST /problems/preview", w.gate(w.csrf(http.HandlerFunc(w.problemPreview))))
	mux.Handle("GET /problems/{id}", w.gate(http.HandlerFunc(w.problemShow)))
	mux.Handle("POST /problems/{id}", w.gate(w.csrf(http.HandlerFunc(w.problemEdit))))
}

// csrf rejects a state-changing request that lacks the session's CSRF token.
// It runs inside the Gate, which has attached the session.
func (w *Web) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ws, ok := auth.SessionFrom(r.Context())
		if !ok || !auth.CheckCSRF(r, ws) {
			http.Error(rw, "invalid or missing CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func sessionActor(r *http.Request) (domain.Actor, bool) {
	ws, ok := auth.SessionFrom(r.Context())
	if !ok || ws.UserID.IsNil() {
		return domain.Actor{}, false
	}
	return domain.Actor{Kind: domain.ActorHuman, UserID: ws.UserID, ClientID: "web-session"}, true
}

// ---- list ----

type problemRow struct {
	ID        string
	Snippet   string
	Type      string
	Status    string
	Version   int
	CreatedBy string
	Updated   string
}

type listData struct {
	Status, Type, Tag, Q, Kind string
	Tags                       []tags.Tag
	HasTags                    bool
	Rows                       []problemRow
	NextURL                    string
	Errors                     []platform.FieldError
}

func (w *Web) problemList(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	qv := r.URL.Query()
	d := listData{Status: qv.Get("status"), Type: qv.Get("type"), Tag: qv.Get("tag"), Q: qv.Get("q"),
		Kind: qv.Get("created_by_kind"), HasTags: w.prob.tags != nil}
	d.Tags = w.activeTags(r.Context(), actor.UserID)

	q := problems.ListQuery{Status: d.Status, Type: d.Type, Tag: d.Tag, Q: d.Q, CreatedByKind: d.Kind, Cursor: qv.Get("cursor")}
	res, err := w.prob.problems.List(r.Context(), actor.UserID, q)
	status := http.StatusOK
	if err != nil {
		var pe *platform.Problem
		if !errors.As(err, &pe) || pe.Status >= 500 {
			w.failProblemPage(rw, "list problems", err)
			return
		}
		status = pe.Status
		d.Errors = pe.Errors
		if len(d.Errors) == 0 {
			d.Errors = []platform.FieldError{{Field: "request", Detail: pe.Detail}}
		}
	}
	for _, v := range res.Items {
		d.Rows = append(d.Rows, problemRow{ID: v.ID.String(), Snippet: snippet(v.Prompt, 120), Type: v.Type,
			Status: string(v.Status), Version: v.Version, CreatedBy: string(v.CreatedBy.Kind),
			Updated: v.UpdatedAt.UTC().Format("2006-01-02 15:04")})
	}
	if res.NextCursor != nil {
		next := url.Values{}
		for _, k := range []string{"status", "type", "tag", "q", "created_by_kind"} {
			if v := qv.Get(k); v != "" {
				next.Set(k, v)
			}
		}
		next.Set("cursor", *res.NextCursor)
		d.NextURL = "/problems?" + next.Encode()
	}
	w.renderStatus(rw, r, status, "problems", page{Title: "Problems", Data: d})
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// activeTags returns all of the owner's active tags (empty without a tag
// service).
func (w *Web) activeTags(ctx context.Context, owner domain.ID) []tags.Tag {
	if w.prob.tags == nil {
		return nil
	}
	var out []tags.Tag
	cursor := ""
	for {
		page, next, err := w.prob.tags.List(ctx, owner, tags.StatusActive, platform.Page{Cursor: cursor, Limit: platform.MaxLimit})
		if err != nil {
			w.cfg.Logger.Error("list tags", slog.String("error", err.Error()))
			return out
		}
		out = append(out, page...)
		if next == "" {
			return out
		}
		cursor = next
	}
}

// ---- editor ----

type editData struct {
	F        problemForm
	Tags     []tags.Tag
	Errors   []platform.FieldError
	Conflict bool
	Notice   string
	Preview  previewData
}

var notices = map[string]string{
	"created": "Problem created.",
	"edited":  "Saved as a new version.",
}

func validType(t string) bool {
	return t == problems.TypeMCQ || t == problems.TypeCloze || t == problems.TypeOpen
}

func (w *Web) problemNew(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	typ := r.URL.Query().Get("type")
	if !validType(typ) {
		typ = problems.TypeMCQ
	}
	f := problemForm{Type: typ, Status: string(problems.StatusDraft), Tags: map[string]bool{}}
	w.renderEditor(rw, r, http.StatusOK, actor.UserID, f, nil, false, "")
}

func (w *Web) problemShow(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	v, err := w.prob.problems.Get(r.Context(), actor.UserID, id)
	if err != nil {
		w.failProblem(rw, r, "get problem", err)
		return
	}
	f, err := formFromView(v)
	if err != nil {
		w.failProblemPage(rw, "decode problem", err)
		return
	}
	if w.prob.tags != nil {
		cur, err := w.prob.tags.ProblemTags(r.Context(), actor.UserID, id)
		if err != nil {
			w.failProblemPage(rw, "problem tags", err)
			return
		}
		for _, t := range cur {
			f.Tags[t.Slug] = true
		}
	}
	w.renderEditor(rw, r, http.StatusOK, actor.UserID, f, nil, false, notices[r.URL.Query().Get("saved")])
}

func (w *Web) problemCreate(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	f := parseProblemForm(r)
	if !validType(f.Type) {
		http.Error(rw, "unknown problem type", http.StatusBadRequest)
		return
	}
	payload, err := f.payload()
	if err != nil {
		w.failProblemPage(rw, "build payload", err)
		return
	}
	status := problems.StatusDraft
	if f.Status == string(problems.StatusLive) {
		status = problems.StatusLive
	}
	f.Status = string(status)
	v, err := w.prob.problems.Create(r.Context(), actor, problems.CreateInput{Type: f.Type, Prompt: f.Prompt,
		Payload: payload, Explanation: f.Explanation, Source: f.Source, Status: status})
	if err != nil {
		w.editorError(rw, r, actor.UserID, f, err, "create problem")
		return
	}
	if err := w.saveTags(r.Context(), actor.UserID, v.ID, f); err != nil {
		w.editorError(rw, r, actor.UserID, w.reopen(v, f), err, "tag new problem")
		return
	}
	http.Redirect(rw, r, "/problems/"+v.ID.String()+"?saved=created", http.StatusSeeOther)
}

func (w *Web) problemEdit(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	f := parseProblemForm(r)
	f.ID = id.String()
	// The type of a stored problem is fixed in the editor.
	cur, err := w.prob.problems.Get(r.Context(), actor.UserID, id)
	if err != nil {
		w.failProblem(rw, r, "get problem", err)
		return
	}
	f.Type, f.Status, f.Version = cur.Type, string(cur.Status), cur.Version
	payload, err := f.payload()
	if err != nil {
		w.failProblemPage(rw, "build payload", err)
		return
	}
	prompt, expl, src := f.Prompt, f.Explanation, f.Source
	ifMatch := f.ETag
	if ifMatch == "" {
		// An empty If-Match must not mean "anything goes".
		ifMatch = `"missing"`
	}
	v, err := w.prob.problems.Edit(r.Context(), actor, id, ifMatch, problems.EditInput{
		ChangeKind: problems.ChangeKind(f.ChangeKind), Prompt: &prompt, Payload: payload,
		Explanation: &expl, Source: &src})
	if err != nil {
		w.editorError(rw, r, actor.UserID, f, err, "edit problem")
		return
	}
	if err := w.saveTags(r.Context(), actor.UserID, id, f); err != nil {
		w.editorError(rw, r, actor.UserID, w.reopen(v, f), err, "tag problem")
		return
	}
	http.Redirect(rw, r, "/problems/"+id.String()+"?saved=edited", http.StatusSeeOther)
}

// reopen turns a saved problem plus the submitted form into editor state for
// the saved problem, so a failure after the save (tags) leaves the user
// editing it rather than creating a duplicate.
func (w *Web) reopen(v problems.View, f problemForm) problemForm {
	f.ID, f.ETag, f.Version, f.Status = v.ID.String(), v.ETag(), v.Version, string(v.Status)
	return f
}

func (w *Web) saveTags(ctx context.Context, owner, id domain.ID, f problemForm) error {
	if w.prob.tags == nil {
		return nil
	}
	refs := make([]string, 0, len(f.Tags))
	for slug := range f.Tags {
		refs = append(refs, slug)
	}
	_, err := w.prob.tags.SetProblemTags(ctx, owner, id, refs)
	return err
}

// editorError re-renders the editor with the user's input kept. A stale
// If-Match (412) becomes the conflict message; validation errors list their
// fields.
func (w *Web) editorError(rw http.ResponseWriter, r *http.Request, owner domain.ID, f problemForm, err error, what string) {
	var pe *platform.Problem
	if !errors.As(err, &pe) {
		w.failProblemPage(rw, what, err)
		return
	}
	switch {
	case pe.Status == http.StatusPreconditionFailed:
		w.renderEditor(rw, r, http.StatusConflict, owner, f, nil, true, "")
	case pe.Status == http.StatusNotFound:
		http.NotFound(rw, r)
	case pe.Status >= 500:
		w.failProblemPage(rw, what, err)
	default:
		errs := pe.Errors
		if len(errs) == 0 {
			errs = []platform.FieldError{{Field: "request", Detail: pe.Detail}}
		}
		w.renderEditor(rw, r, http.StatusUnprocessableEntity, owner, f, errs, false, "")
	}
}

func (w *Web) renderEditor(rw http.ResponseWriter, r *http.Request, status int, owner domain.ID, f problemForm, errs []platform.FieldError, conflict bool, notice string) {
	f.pad()
	d := editData{F: f, Tags: w.activeTags(r.Context(), owner), Errors: errs, Conflict: conflict, Notice: notice,
		Preview: buildPreview(f)}
	title := "New problem"
	if !f.IsNew() {
		title = "Edit problem"
	}
	w.renderStatus(rw, r, status, "problem_edit", page{Title: title, Data: d})
}

func (w *Web) failProblem(rw http.ResponseWriter, r *http.Request, what string, err error) {
	var pe *platform.Problem
	if errors.As(err, &pe) && pe.Status == http.StatusNotFound {
		http.NotFound(rw, r)
		return
	}
	w.failProblemPage(rw, what, err)
}

func (w *Web) failProblemPage(rw http.ResponseWriter, what string, err error) {
	w.cfg.Logger.Error(what, slog.String("error", err.Error()))
	http.Error(rw, "internal error", http.StatusInternalServerError)
}

// ---- live preview ----

type previewData struct {
	Type      string
	Prompt    template.HTML
	Choices   []choiceRow
	Multiple  bool
	Reference template.HTML
	Rubric    []string
	Blanks    []blankRow
	Expl      template.HTML
}

var blankMarker = regexp.MustCompile(`\{\{[^{}<>]*\}\}`)

func buildPreview(f problemForm) previewData {
	d := previewData{Type: f.Type, Choices: f.Choices, Multiple: f.Multiple, Blanks: f.Blanks}
	html := string(RenderMarkdown(f.Prompt))
	if f.Type == problems.TypeCloze {
		// The rendered Markdown is already inert; the replacement is a fixed tag.
		html = blankMarker.ReplaceAllString(html, `<span class="blank">&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;</span>`)
	}
	d.Prompt = template.HTML(html)
	if f.Type == problems.TypeOpen {
		d.Reference = RenderMarkdown(f.Reference)
		d.Rubric = lines(f.Rubric)
	}
	d.Expl = RenderMarkdown(f.Explanation)
	return d
}

// problemPreview renders the editor's current fields as a learner would see
// them. It is a fragment for the small script in /static/problem-editor.js.
func (w *Web) problemPreview(rw http.ResponseWriter, r *http.Request) {
	f := parseProblemForm(r)
	var buf bytes.Buffer
	if err := w.prob.preview.ExecuteTemplate(&buf, "preview", buildPreview(f)); err != nil {
		w.failProblemPage(rw, "render preview", err)
		return
	}
	h := rw.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = rw.Write(buf.Bytes())
}
