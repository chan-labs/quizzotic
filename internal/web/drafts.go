// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// Drafts is what the drafts inbox needs from the problems service.
type Drafts interface {
	List(ctx context.Context, ownerID domain.ID, q problems.ListQuery) (platform.PageResult[problems.View], error)
	Get(ctx context.Context, ownerID, id domain.ID) (problems.View, error)
	Edit(ctx context.Context, actor domain.Actor, id domain.ID, ifMatch string, in problems.EditInput) (problems.View, error)
	Publish(ctx context.Context, ownerID, id domain.ID, ifMatch string) (problems.View, error)
	Delete(ctx context.Context, ownerID, id domain.ID, ifMatch string) error
}

// UseDrafts enables the drafts inbox. Call it before Register.
func (w *Web) UseDrafts(d Drafts) { w.drafts = d }

const (
	draftPageSize   = 50
	maxBulkSelected = 200
	webClientID     = "web-session"
)

type draftRow struct {
	ID          string
	Kind        domain.ActorKind
	Type        string
	Prompt      string
	Explanation string
	Source      string
	Payload     string
	Err         string // set on the row whose edit was refused
}

type draftsData struct {
	Rows    []draftRow
	Kind    string
	Next    string
	Notice  string
	Authors []string
}

func (w *Web) registerDrafts(mux *http.ServeMux) {
	if w.drafts == nil {
		return
	}
	mux.Handle("GET /drafts", w.gate(http.HandlerFunc(w.draftsList)))
	mux.Handle("POST /drafts/bulk", w.gate(http.HandlerFunc(w.draftsBulk)))
	mux.Handle("POST /drafts/{id}/edit", w.gate(http.HandlerFunc(w.draftsEdit)))
}

func (w *Web) draftsList(rw http.ResponseWriter, r *http.Request) {
	w.renderDrafts(rw, r, http.StatusOK, draftNotice(r.URL.Query()), nil)
}

// renderDrafts lists the owner's drafts. failed, when set, marks the row
// whose inline edit was refused and carries the reason.
func (w *Web) renderDrafts(rw http.ResponseWriter, r *http.Request, status int, notice string, failed *draftRow) {
	ws, _ := auth.SessionFrom(r.Context())
	q := r.URL.Query()
	kind := q.Get("kind")
	res, err := w.drafts.List(r.Context(), ws.UserID, problems.ListQuery{
		Status: string(problems.StatusDraft), CreatedByKind: kind, Cursor: q.Get("cursor"), Limit: draftPageSize})
	if err != nil {
		w.fail(rw, err)
		return
	}
	d := draftsData{Kind: kind, Notice: notice, Authors: []string{"human", "agent", "system"}}
	if res.NextCursor != nil {
		d.Next = *res.NextCursor
	}
	for _, v := range res.Items {
		row := toDraftRow(v)
		if failed != nil && failed.ID == row.ID {
			row.Prompt, row.Explanation, row.Source, row.Payload, row.Err = failed.Prompt, failed.Explanation, failed.Source, failed.Payload, failed.Err
		}
		d.Rows = append(d.Rows, row)
	}
	w.renderStatus(rw, r, status, "drafts", page{Title: "Drafts", Data: d})
}

func toDraftRow(v problems.View) draftRow {
	var pretty bytes.Buffer
	payload := string(v.Payload)
	if json.Indent(&pretty, v.Payload, "", "  ") == nil {
		payload = pretty.String()
	}
	return draftRow{ID: v.ID.String(), Kind: v.CreatedBy.Kind, Type: v.Type, Prompt: v.Prompt,
		Explanation: v.Explanation, Source: v.Source, Payload: payload}
}

// draftsBulk publishes or discards the selected drafts. Each id is re-read and
// must still be a draft, so a stale page cannot discard a published problem.
func (w *Web) draftsBulk(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	if !auth.CheckCSRF(r, ws) {
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	action := r.PostFormValue("action")
	if action != "publish" && action != "discard" {
		http.Error(rw, "unknown action", http.StatusBadRequest)
		return
	}
	ids := r.PostForm["id"]
	if len(ids) > maxBulkSelected {
		http.Error(rw, "too many drafts selected", http.StatusBadRequest)
		return
	}
	done, failed := 0, 0
	for _, raw := range ids {
		if w.applyDraft(r.Context(), ws.UserID, raw, action) {
			done++
		} else {
			failed++
		}
	}
	v := url.Values{"done": {strconv.Itoa(done)}, "failed": {strconv.Itoa(failed)}, "action": {action}}
	http.Redirect(rw, r, "/drafts?"+v.Encode(), http.StatusSeeOther)
}

func (w *Web) applyDraft(ctx context.Context, owner domain.ID, raw, action string) bool {
	id, err := domain.ParseID(raw)
	if err != nil {
		return false
	}
	cur, err := w.drafts.Get(ctx, owner, id)
	if err != nil || cur.Status != problems.StatusDraft {
		return false
	}
	if action == "publish" {
		_, err = w.drafts.Publish(ctx, owner, id, cur.ETag())
	} else {
		err = w.drafts.Delete(ctx, owner, id, cur.ETag())
	}
	if err != nil {
		w.cfg.Logger.Warn("bulk draft action", "action", action, "error", err.Error())
		return false
	}
	return true
}

// draftsEdit saves an inline edit as a minor version (a draft has no card).
func (w *Web) draftsEdit(rw http.ResponseWriter, r *http.Request) {
	ws, _ := auth.SessionFrom(r.Context())
	if !auth.CheckCSRF(r, ws) {
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	row := draftRow{ID: id.String(), Prompt: r.PostFormValue("prompt"), Explanation: r.PostFormValue("explanation"),
		Source: r.PostFormValue("source"), Payload: r.PostFormValue("payload")}
	in := problems.EditInput{ChangeKind: problems.ChangeMinor, Prompt: &row.Prompt, Explanation: &row.Explanation, Source: &row.Source}
	if strings.TrimSpace(row.Payload) != "" {
		in.Payload = json.RawMessage(row.Payload)
	}
	cur, err := w.drafts.Get(r.Context(), ws.UserID, id)
	if err == nil && cur.Status != problems.StatusDraft {
		err = platform.Conflict("only drafts can be edited here")
	}
	if err == nil {
		actor := domain.Actor{Kind: domain.ActorHuman, UserID: ws.UserID, ClientID: webClientID}
		_, err = w.drafts.Edit(r.Context(), actor, id, cur.ETag(), in)
	}
	if err == nil {
		http.Redirect(rw, r, "/drafts", http.StatusSeeOther)
		return
	}
	var p *platform.Problem
	if !errors.As(err, &p) || p.Status == http.StatusNotFound {
		w.fail(rw, err)
		return
	}
	row.Err = p.Error()
	for _, fe := range p.Errors {
		row.Err += "; " + fe.Field + ": " + fe.Detail
	}
	w.renderDrafts(rw, r, p.Status, "", &row)
}

func (w *Web) fail(rw http.ResponseWriter, err error) {
	var p *platform.Problem
	if errors.As(err, &p) && p.Status == http.StatusNotFound {
		http.Error(rw, "not found", http.StatusNotFound)
		return
	}
	w.cfg.Logger.Error("web", "error", err.Error())
	http.Error(rw, "internal error", http.StatusInternalServerError)
}

// draftNotice words the result banner from the redirect query.
func draftNotice(q url.Values) string {
	if q.Get("done") == "" {
		return ""
	}
	verb := map[string]string{"publish": "Published", "discard": "Discarded"}[q.Get("action")]
	if verb == "" {
		return ""
	}
	s := verb + " " + q.Get("done")
	if f := q.Get("failed"); f != "" && f != "0" {
		s += "; " + f + " could not be changed"
	}
	return s
}
