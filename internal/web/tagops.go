// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
	"github.com/chan-labs/quizzotic/internal/tags"
)

// TagOpsService is what the tag evolution pages need from the tags feature.
// *tags.Service implements it. The pages act as the signed-in human, who may
// apply, reject and revert any operation of their own, agent-proposed or not.
type TagOpsService interface {
	List(ctx context.Context, ownerID domain.ID, status tags.Status, p platform.Page) ([]tags.Tag, string, error)
	ListProblems(ctx context.Context, ownerID domain.ID, ref string, p platform.Page) ([]problems.View, string, error)
	Propose(ctx context.Context, ownerID domain.ID, actor domain.Actor, in tags.ProposeInput) (tags.OpDetail, error)
	ListOperationDetails(ctx context.Context, ownerID domain.ID, status tags.OpStatus, p platform.Page) ([]tags.OpDetail, string, error)
	ApplyOperation(ctx context.Context, ownerID domain.ID, actor domain.Actor, id domain.ID) (tags.OpDetail, error)
	RejectOperation(ctx context.Context, ownerID domain.ID, actor domain.Actor, id domain.ID) (tags.OpDetail, error)
	RevertOperation(ctx context.Context, ownerID domain.ID, actor domain.Actor, id domain.ID) (tags.OpDetail, error)
}

// UseTagOps enables the tag rename, merge and split pages and the approvals
// queue. Call it before Register.
func (w *Web) UseTagOps(s TagOpsService) { w.tagops = s }

func (w *Web) parseTagOpsTemplates(layout *template.Template) error {
	for _, name := range []string{"tag_ops", "tag_rename", "tag_merge", "tag_split"} {
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

func (w *Web) registerTagOps(mux *http.ServeMux) {
	if w.tagops == nil {
		return
	}
	g := func(h http.HandlerFunc) http.Handler { return w.gate(h) }
	mux.Handle("GET /tag-operations", g(w.opsPage))
	mux.Handle("GET /tag-operations/rename", g(w.renameForm))
	mux.Handle("POST /tag-operations/rename", g(w.renameSubmit))
	mux.Handle("GET /tag-operations/merge", g(w.mergeForm))
	mux.Handle("POST /tag-operations/merge", g(w.mergeSubmit))
	mux.Handle("GET /tag-operations/split", g(w.splitForm))
	mux.Handle("POST /tag-operations/split", g(w.splitSubmit))
	mux.Handle("POST /tag-operations/{id}/apply", g(w.opDecide("apply")))
	mux.Handle("POST /tag-operations/{id}/reject", g(w.opDecide("reject")))
	mux.Handle("POST /tag-operations/{id}/revert", g(w.opDecide("revert")))
}

// ---- shared helpers ----

// tagOpsPost checks the CSRF token and returns the signed-in user's actor.
func (w *Web) tagOpsPost(rw http.ResponseWriter, r *http.Request) (domain.Actor, bool) {
	ws, _ := auth.SessionFrom(r.Context())
	if !auth.CheckCSRF(r, ws) {
		platform.WriteError(rw, r, platform.Forbidden("missing or invalid CSRF token"))
		return domain.Actor{}, false
	}
	return domain.Actor{Kind: domain.ActorHuman, UserID: ws.UserID, ClientID: "web-session"}, true
}

// tagOpError turns a service error into a status and a message for the user.
// ok is false for errors that are not the user's to fix.
func tagOpError(err error) (status int, msg string, ok bool) {
	var pb *platform.Problem
	switch {
	case errors.Is(err, tags.ErrNotFound), errors.Is(err, tags.ErrOpNotFound):
		return http.StatusNotFound, "That tag or operation no longer exists.", true
	case errors.Is(err, tags.ErrOpNotProposed):
		return http.StatusConflict, "That operation has already been decided.", true
	case errors.Is(err, tags.ErrOpNotApplied):
		return http.StatusConflict, "Only an applied operation can be reverted.", true
	case errors.Is(err, tags.ErrOpReverted):
		return http.StatusConflict, "That operation has already been reverted.", true
	case errors.Is(err, tags.ErrRevertConflict):
		return http.StatusConflict, "Cannot revert: a later applied operation touched the same tags. Revert that one first.", true
	case errors.Is(err, tags.ErrSlugTaken):
		return http.StatusConflict, "That slug is already used by an active tag or an alias.", true
	case errors.As(err, &pb) && pb.Status >= 400 && pb.Status < 500:
		return pb.Status, problemMessage(pb), true
	}
	return 0, "", false
}

// allTagsByStatus reads every tag with the status.
func (w *Web) allTagsByStatus(ctx context.Context, owner domain.ID, st tags.Status) ([]tags.Tag, error) {
	var out []tags.Tag
	cursor := ""
	for {
		ts, next, err := w.tagops.List(ctx, owner, st, platform.Page{Cursor: cursor, Limit: platform.MaxLimit})
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

type tagOpt struct {
	ID, Slug, Name string
	Checked        bool
}

func (w *Web) activeTagOpts(ctx context.Context, owner domain.ID) ([]tagOpt, error) {
	ts, err := w.allTagsByStatus(ctx, owner, tags.StatusActive)
	if err != nil {
		return nil, err
	}
	out := make([]tagOpt, len(ts))
	for i, t := range ts {
		out[i] = tagOpt{ID: t.ID.String(), Slug: t.Slug, Name: t.Name}
	}
	slices.SortFunc(out, func(a, b tagOpt) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, nil
}

// propose stores the operation as proposed and, on success, sends the browser
// to the queue where its preview waits for Apply or Reject (done is true). On
// a proposal the service rejects it returns the status and message for the
// caller to redisplay its form.
func (w *Web) propose(rw http.ResponseWriter, r *http.Request, actor domain.Actor, kind tags.OpKind, payload any) (status int, msg string, done bool) {
	raw, err := json.Marshal(payload)
	if err != nil {
		w.fail(rw, err)
		return 0, "", true
	}
	d, err := w.tagops.Propose(r.Context(), actor.UserID, actor, tags.ProposeInput{Kind: kind, Payload: raw})
	if err != nil {
		if st, m, ok := tagOpError(err); ok {
			return st, m, false
		}
		w.fail(rw, err)
		return 0, "", true
	}
	http.Redirect(rw, r, "/tag-operations?proposed="+url.QueryEscape(d.Operation.ID.String()), http.StatusSeeOther)
	return 0, "", true
}

// ---- rename ----

type renameData struct {
	Tags            []tagOpt
	Name, Slug, Tag string
	Error           string
}

func (w *Web) renameForm(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	opts, err := w.activeTagOpts(r.Context(), ws.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	w.render(rw, r, "tag_rename", page{Title: "Rename a tag", Data: renameData{Tags: opts, Tag: r.URL.Query().Get("tag")}})
}

func (w *Web) renameSubmit(rw http.ResponseWriter, r *http.Request) {
	actor, ok := w.tagOpsPost(rw, r)
	if !ok {
		return
	}
	d := renameData{Tag: r.PostFormValue("tag"), Name: strings.TrimSpace(r.PostFormValue("name")), Slug: strings.TrimSpace(r.PostFormValue("slug"))}
	in := tags.RenameInput{Tag: d.Tag}
	if d.Name != "" {
		in.Name = &d.Name
	}
	if d.Slug != "" {
		in.Slug = &d.Slug
	}
	status, msg, done := w.propose(rw, r, actor, tags.OpRename, in)
	if done {
		return
	}
	opts, err := w.activeTagOpts(r.Context(), actor.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	d.Tags, d.Error = opts, msg
	w.renderStatus(rw, r, status, "tag_rename", page{Title: "Rename a tag", Data: d})
}

// ---- merge ----

type mergeData struct {
	Tags   []tagOpt
	Target string
	Error  string
}

func (w *Web) mergeForm(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	opts, err := w.activeTagOpts(r.Context(), ws.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	w.render(rw, r, "tag_merge", page{Title: "Merge tags", Data: mergeData{Tags: opts}})
}

func (w *Web) mergeSubmit(rw http.ResponseWriter, r *http.Request) {
	actor, ok := w.tagOpsPost(rw, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	sources, target := r.PostForm["sources"], r.PostFormValue("target")
	status, msg, done := w.propose(rw, r, actor, tags.OpMerge, tags.MergeInput{Sources: sources, Target: target})
	if done {
		return
	}
	opts, err := w.activeTagOpts(r.Context(), actor.UserID)
	if err != nil {
		w.fail(rw, err)
		return
	}
	for i := range opts {
		opts[i].Checked = slices.Contains(sources, opts[i].ID)
	}
	w.renderStatus(rw, r, status, "tag_merge", page{Title: "Merge tags", Data: mergeData{Tags: opts, Target: target, Error: msg}})
}

// ---- split wizard ----
//
// Step 1 picks the source tag, step 2 the targets (existing tags and new
// ones), step 3 assigns each problem of the source to targets and submits.
// Steps 1 and 2 are GET forms so the wizard needs no script.

type newTargetRow struct{ Name, Slug string }

type splitTarget struct {
	Key, Label string // Key is what an assignment names: the tag ID or the new slug
	ID         string // set for an existing tag
	Name, Slug string // set for a new tag
}

type splitCell struct {
	Key     string
	Label   string
	Checked bool
}

type splitProblem struct {
	ID, Snippet string
	Cells       []splitCell
}

type splitData struct {
	Step     int
	Tags     []tagOpt // step 1: every active tag
	Source   tagOpt
	Others   []tagOpt // step 2: candidates for existing targets
	NewRows  []newTargetRow
	Targets  []splitTarget
	Problems []splitProblem
	Retire   bool
	Error    string
}

const splitNewRows = 3

func (w *Web) splitForm(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	q := r.URL.Query()
	d, err := w.splitState(r.Context(), ws.UserID, q, nil, q.Get("step") == "assign")
	if err != nil {
		w.splitFail(rw, r, d, err)
		return
	}
	w.render(rw, r, "tag_split", page{Title: "Split a tag", Data: d})
}

func (w *Web) splitFail(rw http.ResponseWriter, r *http.Request, d splitData, err error) {
	if st, m, ok := tagOpError(err); ok {
		d.Error = m
		w.renderStatus(rw, r, st, "tag_split", page{Title: "Split a tag", Data: d})
		return
	}
	w.fail(rw, err)
}

// splitState builds the wizard page for the values in v. checked holds the
// assignments already made ("problemID|key"); wantAssign asks for step 3.
func (w *Web) splitState(ctx context.Context, owner domain.ID, v url.Values, checked map[string]bool, wantAssign bool) (splitData, error) {
	all, err := w.activeTagOpts(ctx, owner)
	if err != nil {
		return splitData{}, err
	}
	d := splitData{Step: 1, Tags: all, Retire: v.Get("retire_source") != ""}
	src := v.Get("source")
	if src == "" {
		return d, nil
	}
	for _, t := range all {
		if t.ID == src || t.Slug == src {
			d.Source = t
		}
	}
	if d.Source.ID == "" {
		return d, tags.ErrNotFound
	}
	d.Step = 2
	existing := v["existing"]
	for _, t := range all {
		if t.ID != d.Source.ID {
			t.Checked = slices.Contains(existing, t.ID)
			d.Others = append(d.Others, t)
			if t.Checked {
				d.Targets = append(d.Targets, splitTarget{Key: t.ID, Label: t.Name, ID: t.ID})
			}
		}
	}
	names, slugs := v["new_name"], v["new_slug"]
	for i := 0; i < max(splitNewRows, len(names)); i++ {
		row := newTargetRow{}
		if i < len(names) {
			row.Name = strings.TrimSpace(names[i])
		}
		if i < len(slugs) {
			row.Slug = strings.TrimSpace(slugs[i])
		}
		d.NewRows = append(d.NewRows, row)
		if row.Name == "" {
			continue
		}
		if row.Slug == "" {
			row.Slug = tags.Slugify(row.Name)
		}
		d.Targets = append(d.Targets, splitTarget{Key: row.Slug, Label: row.Name + " (new)", Name: row.Name, Slug: row.Slug})
	}
	if !wantAssign {
		return d, nil
	}
	if len(d.Targets) == 0 {
		d.Error = "Choose at least one existing tag or name a new one."
		return d, nil
	}
	d.Step = 3
	cursor := ""
	for {
		vs, next, err := w.tagops.ListProblems(ctx, owner, d.Source.ID, platform.Page{Cursor: cursor, Limit: platform.MaxLimit})
		if err != nil {
			return d, err
		}
		for _, p := range vs {
			sp := splitProblem{ID: p.ID.String(), Snippet: snippet(p.Prompt, 120)}
			for _, tg := range d.Targets {
				sp.Cells = append(sp.Cells, splitCell{Key: tg.Key, Label: tg.Label, Checked: checked[sp.ID+"|"+tg.Key]})
			}
			d.Problems = append(d.Problems, sp)
		}
		if next == "" {
			return d, nil
		}
		cursor = next
	}
}

func (w *Web) splitSubmit(rw http.ResponseWriter, r *http.Request) {
	actor, ok := w.tagOpsPost(rw, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	checked := map[string]bool{}
	for _, a := range r.PostForm["assign"] {
		checked[a] = true
	}
	d, err := w.splitState(r.Context(), actor.UserID, r.PostForm, checked, true)
	if err != nil {
		w.splitFail(rw, r, d, err)
		return
	}
	if d.Step != 3 {
		w.renderStatus(rw, r, http.StatusUnprocessableEntity, "tag_split", page{Title: "Split a tag", Data: d})
		return
	}
	in := tags.SplitInput{Source: d.Source.ID, RetireSource: d.Retire, Assignments: map[domain.ID][]string{}}
	for _, tg := range d.Targets {
		if tg.ID != "" {
			in.Targets = append(in.Targets, tags.SplitTarget{Ref: tg.ID})
		} else {
			in.Targets = append(in.Targets, tags.SplitTarget{New: &tags.CreateInput{Slug: tg.Slug, Name: tg.Name}})
		}
	}
	for _, p := range d.Problems {
		pid, err := domain.ParseID(p.ID)
		if err != nil {
			continue
		}
		for _, c := range p.Cells {
			if c.Checked {
				in.Assignments[pid] = append(in.Assignments[pid], c.Key)
			}
		}
	}
	status, msg, done := w.propose(rw, r, actor, tags.OpSplit, in)
	if done {
		return
	}
	d.Error = msg
	w.renderStatus(rw, r, status, "tag_split", page{Title: "Split a tag", Data: d})
}
