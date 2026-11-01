// SPDX-License-Identifier: Apache-2.0

package quizzes

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Handler serves the quiz endpoints. HTTP concerns only; rules live in Service.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

// NewHandler builds a Handler. A nil service answers 404 on every route (the
// feature is not wired); a nil logger means slog.Default().
func NewHandler(svc *Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger}
}

// Register mounts the quiz routes on the API router.
func (h *Handler) Register(rt *platform.Router) {
	rt.HandleFunc("GET /quizzes", h.list)
	rt.HandleFunc("POST /quizzes", h.create)
	rt.HandleFunc("GET /quizzes/{id}", h.get)
	rt.HandleFunc("PATCH /quizzes/{id}", h.update)
	rt.HandleFunc("DELETE /quizzes/{id}", h.delete)
	rt.HandleFunc("PUT /quizzes/{id}/items", h.setItems)
}

type itemView struct {
	ProblemID string `json:"problem_id"`
	Position  int    `json:"position"`
}

type actorView struct {
	Kind     domain.ActorKind `json:"kind"`
	UserID   string           `json:"user_id"`
	ClientID string           `json:"client_id"`
}

// quizView is the JSON form of a Quiz.
type quizView struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      Status     `json:"status"`
	CreatedBy   actorView  `json:"created_by"`
	Items       []itemView `json:"items"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func viewOf(q Quiz) quizView {
	v := quizView{ID: q.ID.String(), Title: q.Title, Description: q.Description, Status: q.Status,
		CreatedBy: actorView{Kind: q.CreatedBy.Kind, ClientID: q.CreatedBy.ClientID},
		Items:     make([]itemView, len(q.Items)), CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
	if !q.CreatedBy.UserID.IsNil() {
		v.CreatedBy.UserID = q.CreatedBy.UserID.String()
	}
	for i, id := range q.Items {
		v.Items[i] = itemView{ProblemID: id.String(), Position: i}
	}
	return v
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

// target returns the caller and the quiz ID in the path. A malformed ID is a
// 404, like an unknown one.
func (h *Handler) target(w http.ResponseWriter, r *http.Request) (domain.ID, domain.ID, bool) {
	p, ok := h.principal(w, r)
	if !ok {
		return domain.Nil, domain.Nil, false
	}
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return domain.Nil, domain.Nil, false
	}
	return p.UserID, id, true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *platform.Problem
	switch {
	case errors.Is(err, ErrNotFound):
		err = platform.NotFound("no such resource")
	case !errors.As(err, &p):
		// The cause stays in the log; the client gets an opaque 500.
		h.logger.ErrorContext(r.Context(), "quizzes: request failed", slog.String("error", err.Error()))
	}
	platform.WriteError(w, r, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeQuiz(w http.ResponseWriter, status int, q Quiz) {
	platform.SetETag(w, ETag(q))
	writeJSON(w, status, viewOf(q))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	qs, next, err := h.svc.List(r.Context(), p.UserID, Status(r.URL.Query().Get("status")), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]quizView, len(qs))
	for i, q := range qs {
		items[i] = viewOf(q)
	}
	writeJSON(w, http.StatusOK, platform.NewPageResult(items, next))
}

type createBody struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      Status   `json:"status"`
	ProblemIDs  []string `json:"problem_ids"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	var body createBody
	if err := platform.DecodeJSON(w, r, &body); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	actor := domain.Actor{Kind: domain.ActorKind(p.Kind), UserID: p.UserID, ClientID: p.ClientID}
	q, err := h.svc.Create(r.Context(), actor, CreateInput(body))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", platform.APIPrefix+"/quizzes/"+q.ID.String())
	writeQuiz(w, http.StatusCreated, q)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := h.target(w, r)
	if !ok {
		return
	}
	q, err := h.svc.Get(r.Context(), owner, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeQuiz(w, http.StatusOK, q)
}

type updateBody struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := h.target(w, r)
	if !ok {
		return
	}
	ifMatch, err := platform.RequireIfMatch(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	var body updateBody
	if err := platform.DecodeJSON(w, r, &body); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	if body.Title == nil && body.Description == nil && body.Status == nil {
		platform.WriteError(w, r, platform.Validation("nothing to update"))
		return
	}
	q, err := h.svc.Update(r.Context(), owner, id, ifMatch, Patch(body))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeQuiz(w, http.StatusOK, q)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := h.target(w, r)
	if !ok {
		return
	}
	ifMatch, err := platform.RequireIfMatch(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), owner, id, ifMatch); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setItemsBody struct {
	ProblemIDs []string `json:"problem_ids"`
}

func (h *Handler) setItems(w http.ResponseWriter, r *http.Request) {
	owner, id, ok := h.target(w, r)
	if !ok {
		return
	}
	ifMatch, err := platform.RequireIfMatch(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	var body setItemsBody
	if err := platform.DecodeJSON(w, r, &body); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	if body.ProblemIDs == nil {
		platform.WriteError(w, r, platform.Validation("invalid body", platform.FieldError{Field: "problem_ids", Detail: "is required"}))
		return
	}
	q, err := h.svc.SetItems(r.Context(), owner, id, ifMatch, body.ProblemIDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeQuiz(w, http.StatusOK, q)
}
