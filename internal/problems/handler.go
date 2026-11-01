// SPDX-License-Identifier: Apache-2.0

package problems

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Handler serves the problems endpoints. HTTP concerns only; rules live in
// Service.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

// NewHandler builds a Handler. A nil logger means slog.Default(); a nil
// service answers every request 404.
func NewHandler(svc *Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger}
}

// Register mounts the problems routes on the API router. The scope guard has
// already authenticated and authorized the request.
func (h *Handler) Register(rt *platform.Router) {
	rt.HandleFunc("POST /problems", h.Create)
	rt.HandleFunc("GET /problems", h.List)
	rt.HandleFunc("GET /problems/{id}", h.Get)
	rt.HandleFunc("POST /problems/{id}/publish", h.Publish)
	rt.HandleFunc("POST /problems/{id}/archive", h.Archive)
	rt.HandleFunc("DELETE /problems/{id}", h.Delete)
}

type createRequest struct {
	Type        string          `json:"type"`
	Prompt      string          `json:"prompt"`
	Payload     json.RawMessage `json:"payload"`
	Explanation string          `json:"explanation"`
	Source      string          `json:"source"`
	Status      string          `json:"status"`
}

// Create answers POST /problems.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := platform.DecodeJSON(w, r, &req); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	v, err := h.svc.Create(r.Context(), actorOf(p), CreateInput{
		Type: req.Type, Prompt: req.Prompt, Payload: req.Payload,
		Explanation: req.Explanation, Source: req.Source, Status: Status(req.Status),
	})
	if err != nil {
		h.fail(w, r, "create", err)
		return
	}
	w.Header().Set("Location", platform.APIPrefix+"/problems/"+v.ID.String())
	platform.SetETag(w, v.ETag())
	writeJSON(w, http.StatusCreated, v)
}

// Get answers GET /problems/{id}[?view=prompt].
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	view := r.URL.Query().Get("view")
	if view != "" && view != ViewFull && view != ViewPrompt {
		platform.WriteError(w, r, platform.Validation("invalid view",
			platform.FieldError{Field: "view", Detail: "must be full or prompt"}))
		return
	}
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		platform.WriteError(w, r, platform.NotFound("no such problem"))
		return
	}
	if view == ViewPrompt {
		pv, err := h.svc.GetPrompt(r.Context(), p.UserID, id)
		if err != nil {
			h.fail(w, r, "get", err)
			return
		}
		// A shuffled view differs on every request, so it must not be cached.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, pv)
		return
	}
	v, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		h.fail(w, r, "get", err)
		return
	}
	platform.SetETag(w, v.ETag())
	writeJSON(w, http.StatusOK, v)
}

// List answers GET /problems.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	q := r.URL.Query()
	res, err := h.svc.List(r.Context(), p.UserID, ListQuery{
		Status: q.Get("status"), Type: q.Get("type"), CreatedByKind: q.Get("created_by_kind"),
		Q: q.Get("q"), Tag: q.Get("tag"), Cursor: page.Cursor, Limit: page.Limit,
	})
	if err != nil {
		h.fail(w, r, "list", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// Publish answers POST /problems/{id}/publish.
func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "publish", (*Service).Publish)
}

// Archive answers POST /problems/{id}/archive.
func (h *Handler) Archive(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, "archive", (*Service).Archive)
}

func (h *Handler) lifecycle(w http.ResponseWriter, r *http.Request, op string,
	do func(*Service, context.Context, domain.ID, domain.ID, string) (View, error)) {
	p, id, ifMatch, ok := h.mutation(w, r)
	if !ok {
		return
	}
	v, err := do(h.svc, r.Context(), p.UserID, id, ifMatch)
	if err != nil {
		h.fail(w, r, op, err)
		return
	}
	platform.SetETag(w, v.ETag())
	writeJSON(w, http.StatusOK, v)
}

// Delete answers DELETE /problems/{id}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, id, ifMatch, ok := h.mutation(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), p.UserID, id, ifMatch); err != nil {
		h.fail(w, r, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mutation authenticates, demands If-Match (428 before anything is looked up)
// and parses the path ID.
func (h *Handler) mutation(w http.ResponseWriter, r *http.Request) (auth.Principal, domain.ID, string, bool) {
	p, ok := h.principal(w, r)
	if !ok {
		return p, domain.Nil, "", false
	}
	ifMatch, err := platform.RequireIfMatch(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return p, domain.Nil, "", false
	}
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		platform.WriteError(w, r, platform.NotFound("no such problem"))
		return p, domain.Nil, "", false
	}
	return p, id, ifMatch, true
}

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

func actorOf(p auth.Principal) domain.Actor {
	return domain.Actor{Kind: domain.ActorKind(p.Kind), UserID: p.UserID, ClientID: p.ClientID}
}

// fail renders err. Anything that is not a client-facing problem is logged and
// becomes an opaque 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var pb *platform.Problem
	if !errors.As(err, &pb) {
		h.logger.ErrorContext(r.Context(), "problems: "+op+" failed", slog.String("error", err.Error()))
	}
	platform.WriteError(w, r, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
