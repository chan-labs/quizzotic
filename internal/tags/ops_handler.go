// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// scopeApply authorizes deciding (and applying at once) a tag operation.
const scopeApply = "tags:apply"

// registerOps mounts the tag operation routes.
func (h *Handler) registerOps(rt *platform.Router) {
	rt.HandleFunc("GET /tag-operations", h.listOps)
	rt.HandleFunc("POST /tag-operations", h.proposeOp)
	rt.HandleFunc("GET /tag-operations/{id}", h.getOp)
	rt.HandleFunc("POST /tag-operations/{id}/apply", h.applyOp)
	rt.HandleFunc("POST /tag-operations/{id}/reject", h.rejectOp)
	rt.HandleFunc("POST /tag-operations/{id}/revert", h.revertOp)
}

type actorView struct {
	Kind     domain.ActorKind `json:"kind"`
	UserID   string           `json:"user_id"`
	ClientID string           `json:"client_id"`
}

func actorViewOf(a domain.Actor) *actorView {
	return &actorView{Kind: a.Kind, UserID: a.UserID.String(), ClientID: a.ClientID}
}

type tagEffectView struct {
	ID             string `json:"id"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Status         Status `json:"status"`
	ProblemsBefore int    `json:"problems_before"`
	ProblemsAfter  int    `json:"problems_after"`
	Created        bool   `json:"created"`
}

type effectView struct {
	AffectedProblemIDs []string        `json:"affected_problem_ids"`
	Tags               []tagEffectView `json:"tags"`
}

func effectViewOf(e *Effect) *effectView {
	if e == nil {
		return nil
	}
	v := &effectView{AffectedProblemIDs: make([]string, len(e.AffectedProblems)), Tags: make([]tagEffectView, len(e.Tags))}
	for i, id := range e.AffectedProblems {
		v.AffectedProblemIDs[i] = id.String()
	}
	for i, t := range e.Tags {
		v.Tags[i] = tagEffectView{ID: t.ID.String(), Slug: t.Slug, Name: t.Name, Status: t.Status,
			ProblemsBefore: t.ProblemsBefore, ProblemsAfter: t.ProblemsAfter, Created: t.Created}
	}
	return v
}

type operationView struct {
	ID           string          `json:"id"`
	Kind         OpKind          `json:"kind"`
	Status       OpStatus        `json:"status"`
	Payload      json.RawMessage `json:"payload"`
	ProposedBy   *actorView      `json:"proposed_by"`
	DecidedBy    *actorView      `json:"decided_by"`
	CreatedAt    time.Time       `json:"created_at"`
	DecidedAt    *time.Time      `json:"decided_at"`
	AppliedAt    *time.Time      `json:"applied_at"`
	RevertedBy   *actorView      `json:"reverted_by"`
	RevertedAt   *time.Time      `json:"reverted_at"`
	Preview     *effectView     `json:"preview"`
	PreviewError *string         `json:"preview_error"`
	Result       *effectView     `json:"result"`
}

func operationViewOf(d OpDetail) operationView {
	op := d.Operation
	v := operationView{ID: op.ID.String(), Kind: op.Kind, Status: op.Status, Payload: op.Payload,
		ProposedBy: actorViewOf(op.ProposedBy), CreatedAt: op.CreatedAt,
		Preview: effectViewOf(d.Preview), Result: effectViewOf(d.Result)}
	if len(v.Payload) == 0 {
		v.Payload = json.RawMessage("null")
	}
	if op.Status != OpProposed {
		v.DecidedBy = actorViewOf(op.DecidedBy)
		t := op.DecidedAt
		v.DecidedAt = &t
	}
	if !op.AppliedAt.IsZero() {
		t := op.AppliedAt
		v.AppliedAt = &t
	}
	if !op.RevertedAt.IsZero() {
		t := op.RevertedAt
		v.RevertedAt = &t
		v.RevertedBy = actorViewOf(op.RevertedBy)
	}
	if d.PreviewError != "" {
		v.PreviewError = &d.PreviewError
	}
	return v
}

func actorOf(p auth.Principal) domain.Actor {
	return domain.Actor{Kind: domain.ActorKind(p.Kind), UserID: p.UserID, ClientID: p.ClientID}
}

// principal returns the caller, or writes the 404/401 and returns false.
func (h *Handler) principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return auth.Principal{}, false
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		platform.WriteError(w, r, platform.Unauthorized("authentication required"))
		return auth.Principal{}, false
	}
	return p, true
}

func (h *Handler) opID(w http.ResponseWriter, r *http.Request) (domain.ID, bool) {
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return domain.Nil, false
	}
	return id, true
}

func (h *Handler) listOps(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	ops, next, err := h.svc.ListOperations(r.Context(), p.UserID, OpStatus(r.URL.Query().Get("status")), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]operationView, len(ops))
	for i, op := range ops {
		items[i] = operationViewOf(OpDetail{Operation: op})
	}
	writeJSON(w, http.StatusOK, platform.NewPageResult(items, next))
}

func (h *Handler) proposeOp(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body ProposeInput
	if err := platform.DecodeJSON(w, r, &body); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	if body.Apply && !p.Has(scopeApply) {
		platform.WriteError(w, r, platform.Forbidden("applying a tag operation needs the "+scopeApply+" scope; propose it without apply for a human to approve"))
		return
	}
	d, err := h.svc.Propose(r.Context(), p.UserID, actorOf(p), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", platform.APIPrefix+"/tag-operations/"+d.Operation.ID.String())
	writeJSON(w, http.StatusCreated, operationViewOf(d))
}

func (h *Handler) getOp(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := h.opID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.GetOperation(r.Context(), p.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operationViewOf(d))
}

func (h *Handler) applyOp(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := h.opID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.ApplyOperation(r.Context(), p.UserID, actorOf(p), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operationViewOf(d))
}

func (h *Handler) rejectOp(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := h.opID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.RejectOperation(r.Context(), p.UserID, actorOf(p), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operationViewOf(d))
}

func (h *Handler) revertOp(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, ok := h.opID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.RevertOperation(r.Context(), p.UserID, actorOf(p), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, operationViewOf(d))
}
