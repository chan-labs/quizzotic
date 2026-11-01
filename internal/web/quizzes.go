// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/quizzes"
	"github.com/chan-labs/quizzotic/internal/review"
)

// QuizService is what the Quizzes pages need from the quizzes feature.
// *quizzes.Service implements it.
type QuizService interface {
	Create(ctx context.Context, actor domain.Actor, in quizzes.CreateInput) (quizzes.Quiz, error)
	Get(ctx context.Context, ownerID, id domain.ID) (quizzes.Quiz, error)
	List(ctx context.Context, ownerID domain.ID, status quizzes.Status, p platform.Page) ([]quizzes.Quiz, string, error)
	Update(ctx context.Context, ownerID, id domain.ID, ifMatch string, patch quizzes.Patch) (quizzes.Quiz, error)
	SetItems(ctx context.Context, ownerID, id domain.ID, ifMatch string, refs []string) (quizzes.Quiz, error)
}

// QuizProblems is what the quiz editor needs from the problems feature: to
// name a quiz's items and to offer problems to add.
type QuizProblems interface {
	List(ctx context.Context, ownerID domain.ID, q problems.ListQuery) (platform.PageResult[problems.View], error)
	Get(ctx context.Context, ownerID, id domain.ID) (problems.View, error)
}

// QuizSessions starts quiz sessions. *review.SessionService implements it.
type QuizSessions interface {
	Create(ctx context.Context, userID domain.ID, in review.CreateSessionInput) (review.SessionView, error)
}

type quizPages struct {
	quizzes  QuizService
	problems QuizProblems
	sessions QuizSessions
}

// UseQuizzes enables the Quizzes pages. Call it before Register.
func (w *Web) UseQuizzes(q QuizService, p QuizProblems, s QuizSessions) {
	w.quiz = quizPages{quizzes: q, problems: p, sessions: s}
}

func (w *Web) parseQuizTemplates(layout *template.Template) error {
	for _, name := range []string{"quizzes", "quiz_edit"} {
		t, err := layout.Clone()
		if err != nil {
			return err
		}
		if _, err := t.ParseFS(templateFS, "templates/"+name+".html"); err != nil {
			return err
		}
		w.tmpl[name] = t
	}
	return nil
}

func (w *Web) registerQuizzes(mux *http.ServeMux) {
	if w.quiz.quizzes == nil {
		return
	}
	mux.Handle("GET /quizzes", w.gate(http.HandlerFunc(w.quizList)))
	mux.Handle("GET /quizzes/new", w.gate(http.HandlerFunc(w.quizNew)))
	mux.Handle("POST /quizzes", w.gate(w.csrf(http.HandlerFunc(w.quizCreate))))
	mux.Handle("GET /quizzes/{id}", w.gate(http.HandlerFunc(w.quizShow)))
	mux.Handle("POST /quizzes/{id}", w.gate(w.csrf(http.HandlerFunc(w.quizSave))))
	mux.Handle("POST /quizzes/{id}/start", w.gate(w.csrf(http.HandlerFunc(w.quizStart))))
}

// ---- list ----

type quizRow struct {
	ID, Title, Description, Status string
	Items                          int
	Live                           bool
}

type quizListData struct {
	Rows    []quizRow
	NextURL string
	Notice  string
}

var quizNotices = map[string]string{
	"created": "Quiz created.",
	"saved":   "Quiz saved.",
}

func (w *Web) quizList(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	qs, next, err := w.quiz.quizzes.List(r.Context(), actor.UserID, "", platform.Page{Cursor: r.URL.Query().Get("cursor"), Limit: platform.DefaultLimit})
	if err != nil {
		w.failQuiz(rw, r, "list quizzes", err)
		return
	}
	d := quizListData{Notice: quizNotices[r.URL.Query().Get("notice")]}
	for _, q := range qs {
		d.Rows = append(d.Rows, quizRow{ID: q.ID.String(), Title: q.Title, Description: q.Description,
			Status: string(q.Status), Items: len(q.Items), Live: q.Status == quizzes.StatusLive})
	}
	if next != "" {
		d.NextURL = "/quizzes?" + url.Values{"cursor": {next}}.Encode()
	}
	w.render(rw, r, "quizzes", page{Title: "Quizzes", Data: d})
}

// ---- editor ----

type quizItemRow struct {
	ID, Snippet string
	Pos         int
	Remove      bool
}

type quizCandidate struct{ ID, Snippet string }

type quizForm struct {
	ID, ETag, Title, Description, Status string
	Items                                []quizItemRow
	Add                                  []string
}

func (f quizForm) Adding(id string) bool {
	for _, a := range f.Add {
		if a == id {
			return true
		}
	}
	return false
}

type quizEditData struct {
	F          quizForm
	New        bool
	Candidates []quizCandidate
	Q          string
	Errors     []platform.FieldError
	Conflict   bool
	Notice     string
}

func (w *Web) quizNew(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	w.renderQuizEditor(rw, r, http.StatusOK, actor.UserID, quizForm{Status: string(quizzes.StatusDraft)}, nil, false, "")
}

func (w *Web) quizShow(rw http.ResponseWriter, r *http.Request) {
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
	q, err := w.quiz.quizzes.Get(r.Context(), actor.UserID, id)
	if err != nil {
		w.failQuiz(rw, r, "get quiz", err)
		return
	}
	w.renderQuizEditor(rw, r, http.StatusOK, actor.UserID, quizFormFrom(q), nil, false, quizNotices[r.URL.Query().Get("notice")])
}

func quizFormFrom(q quizzes.Quiz) quizForm {
	f := quizForm{ID: q.ID.String(), ETag: quizzes.ETag(q), Title: q.Title, Description: q.Description, Status: string(q.Status)}
	for i, pid := range q.Items {
		f.Items = append(f.Items, quizItemRow{ID: pid.String(), Pos: i + 1})
	}
	return f
}

// nameItems fills in each row's prompt snippet.
func (w *Web) nameItems(ctx context.Context, owner domain.ID, f *quizForm) {
	for i := range f.Items {
		id, err := domain.ParseID(f.Items[i].ID)
		if err == nil {
			var v problems.View
			if v, err = w.quiz.problems.Get(ctx, owner, id); err == nil {
				f.Items[i].Snippet = snippet(v.Prompt, 100)
				continue
			}
		}
		f.Items[i].Snippet = "(problem no longer available)"
	}
}

func (w *Web) renderQuizEditor(rw http.ResponseWriter, r *http.Request, status int, owner domain.ID, f quizForm, errs []platform.FieldError, conflict bool, notice string) {
	w.nameItems(r.Context(), owner, &f)
	d := quizEditData{F: f, New: f.ID == "", Errors: errs, Conflict: conflict, Notice: notice, Q: r.URL.Query().Get("q")}
	have := map[string]bool{}
	for _, it := range f.Items {
		have[it.ID] = true
	}
	res, err := w.quiz.problems.List(r.Context(), owner, problems.ListQuery{Status: string(problems.StatusLive), Q: d.Q, Limit: platform.DefaultLimit})
	if err == nil {
		for _, v := range res.Items {
			if !have[v.ID.String()] {
				d.Candidates = append(d.Candidates, quizCandidate{ID: v.ID.String(), Snippet: snippet(v.Prompt, 100)})
			}
		}
	}
	title := "New quiz"
	if !d.New {
		title = "Edit quiz"
	}
	w.renderStatus(rw, r, status, "quiz_edit", page{Title: title, Data: d})
}

func parseQuizForm(r *http.Request) quizForm {
	_ = r.ParseForm()
	f := quizForm{ID: r.PathValue("id"), ETag: r.PostFormValue("etag"), Title: r.PostFormValue("title"),
		Description: r.PostFormValue("description"), Status: r.PostFormValue("status"), Add: r.PostForm["add"]}
	for i, id := range r.PostForm["item"] {
		row := quizItemRow{ID: id, Pos: i + 1, Remove: r.PostFormValue("remove_"+id) != ""}
		if n, err := strconv.Atoi(r.PostFormValue("pos_" + id)); err == nil {
			row.Pos = n
		}
		f.Items = append(f.Items, row)
	}
	return f
}

// itemRefs is the ordered item list the form asks for: kept items by their
// position number (ties keep page order), then newly added problems in the
// order they were listed.
func (f quizForm) itemRefs() []string {
	kept := make([]quizItemRow, 0, len(f.Items))
	for _, it := range f.Items {
		if !it.Remove {
			kept = append(kept, it)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Pos < kept[j].Pos })
	refs := make([]string, 0, len(kept)+len(f.Add))
	seen := map[string]bool{}
	for _, it := range kept {
		if !seen[it.ID] {
			seen[it.ID] = true
			refs = append(refs, it.ID)
		}
	}
	for _, id := range f.Add {
		if !seen[id] {
			seen[id] = true
			refs = append(refs, id)
		}
	}
	return refs
}

func (w *Web) quizCreate(rw http.ResponseWriter, r *http.Request) {
	actor, ok := sessionActor(r)
	if !ok {
		http.Error(rw, "no session", http.StatusUnauthorized)
		return
	}
	f := parseQuizForm(r)
	f.ID = ""
	q, err := w.quiz.quizzes.Create(r.Context(), actor, quizzes.CreateInput{Title: f.Title, Description: f.Description,
		Status: quizzes.Status(f.Status), ProblemIDs: f.itemRefs()})
	if err != nil {
		w.quizError(rw, r, actor.UserID, f, err, "create quiz")
		return
	}
	http.Redirect(rw, r, "/quizzes/"+q.ID.String()+"?notice=created", http.StatusSeeOther)
}

func (w *Web) quizSave(rw http.ResponseWriter, r *http.Request) {
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
	f := parseQuizForm(r)
	ifMatch := f.ETag
	if ifMatch == "" {
		ifMatch = `"missing"` // an empty If-Match must not mean "anything goes"
	}
	title, desc, status := f.Title, f.Description, f.Status
	cur, err := w.quiz.quizzes.Update(r.Context(), actor.UserID, id, ifMatch, quizzes.Patch{Title: &title, Description: &desc, Status: &status})
	if err != nil {
		w.quizError(rw, r, actor.UserID, f, err, "update quiz")
		return
	}
	f.ETag = quizzes.ETag(cur) // the details are saved; later retries use the new token
	if _, err := w.quiz.quizzes.SetItems(r.Context(), actor.UserID, id, f.ETag, f.itemRefs()); err != nil {
		w.quizError(rw, r, actor.UserID, f, err, "set quiz items")
		return
	}
	http.Redirect(rw, r, "/quizzes/"+id.String()+"?notice=saved", http.StatusSeeOther)
}

func (w *Web) quizError(rw http.ResponseWriter, r *http.Request, owner domain.ID, f quizForm, err error, what string) {
	var pe *platform.Problem
	if !errors.As(err, &pe) || pe.Status >= 500 || errors.Is(err, quizzes.ErrNotFound) {
		w.failQuiz(rw, r, what, err)
		return
	}
	switch pe.Status {
	case http.StatusNotFound:
		http.NotFound(rw, r)
	case http.StatusPreconditionFailed:
		w.renderQuizEditor(rw, r, http.StatusConflict, owner, f, nil, true, "")
	default:
		w.renderQuizEditor(rw, r, http.StatusUnprocessableEntity, owner, f, problemErrors(pe), false, "")
	}
}

func problemErrors(pe *platform.Problem) []platform.FieldError {
	if len(pe.Errors) > 0 {
		return pe.Errors
	}
	return []platform.FieldError{{Field: "request", Detail: pe.Detail}}
}

// ---- start ----

// quizStart freezes a quiz session through the session service and sends the
// browser to the Review page for it.
func (w *Web) quizStart(rw http.ResponseWriter, r *http.Request) {
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
	order := r.PostFormValue("order")
	if order != "ordered" && order != "shuffled" {
		http.Error(rw, "order must be ordered or shuffled", http.StatusBadRequest)
		return
	}
	sess, err := w.quiz.sessions.Create(r.Context(), actor.UserID, review.CreateSessionInput{
		Kind: review.SessionQuiz, QuizID: id.String(), Shuffle: order == "shuffled"})
	if err != nil {
		var pe *platform.Problem
		if errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity {
			q, gerr := w.quiz.quizzes.Get(r.Context(), actor.UserID, id)
			if gerr != nil {
				w.failQuiz(rw, r, "get quiz", gerr)
				return
			}
			w.renderQuizEditor(rw, r, http.StatusUnprocessableEntity, actor.UserID, quizFormFrom(q), problemErrors(pe), false, "")
			return
		}
		w.failQuiz(rw, r, "start quiz", err)
		return
	}
	http.Redirect(rw, r, "/review/"+sess.ID.String(), http.StatusSeeOther)
}

func (w *Web) failQuiz(rw http.ResponseWriter, r *http.Request, what string, err error) {
	var pe *platform.Problem
	if errors.Is(err, quizzes.ErrNotFound) || (errors.As(err, &pe) && pe.Status == http.StatusNotFound) {
		http.NotFound(rw, r)
		return
	}
	w.failProblemPage(rw, what, err)
}
