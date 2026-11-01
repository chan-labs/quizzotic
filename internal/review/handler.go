// SPDX-License-Identifier: Apache-2.0

package review

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// SessionHandler serves the sessions endpoints. HTTP concerns only; rules live
// in SessionService.
type SessionHandler struct {
	svc    *SessionService
	logger *slog.Logger
}

// NewSessionHandler builds a SessionHandler. A nil logger means
// slog.Default(); a nil service answers every request 404.
func NewSessionHandler(svc *SessionService, logger *slog.Logger) *SessionHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionHandler{svc: svc, logger: logger}
}

// Register mounts the sessions routes on the API router. The scope guard has
// already authenticated and authorized the request.
func (h *SessionHandler) Register(rt *platform.Router) {
	rt.HandleFunc("POST /sessions", h.Create)
	rt.HandleFunc("GET /sessions/{id}", h.Get)
	rt.HandleFunc("GET /sessions/{id}/next", h.Next)
	rt.HandleFunc("POST /sessions/{id}/complete", h.Complete)
}

type createSessionRequest struct {
	Kind    string `json:"kind"`
	QuizID  string `json:"quiz_id"`
	Shuffle bool   `json:"shuffle"`
}

// Create answers POST /sessions.
func (h *SessionHandler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.human(w, r)
	if !ok {
		return
	}
	var req createSessionRequest
	if err := platform.DecodeJSON(w, r, &req); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	v, err := h.svc.Create(r.Context(), p.UserID, CreateSessionInput{Kind: SessionKind(req.Kind), QuizID: req.QuizID, Shuffle: req.Shuffle})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", platform.APIPrefix+"/sessions/"+v.ID.String())
	writeJSON(w, http.StatusCreated, v)
}

// Get answers GET /sessions/{id}.
func (h *SessionHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.humanWithID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// Next answers GET /sessions/{id}/next.
func (h *SessionHandler) Next(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.humanWithID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Next(r.Context(), p.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// Complete answers POST /sessions/{id}/complete.
func (h *SessionHandler) Complete(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.humanWithID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Complete(r.Context(), p.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// human returns the calling principal when it is a human. The scope guard
// already refuses agents (they never hold sessions:write); this keeps the rule
// even if a route is ever mounted without the guard.
func (h *SessionHandler) human(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return auth.Principal{}, false
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		platform.WriteError(w, r, platform.Unauthorized("authentication required"))
		return auth.Principal{}, false
	}
	if p.Kind != auth.KindHuman {
		platform.WriteError(w, r, platform.Forbidden("only a human can run sessions"))
		return auth.Principal{}, false
	}
	return p, true
}

func (h *SessionHandler) humanWithID(w http.ResponseWriter, r *http.Request) (auth.Principal, domain.ID, bool) {
	p, ok := h.human(w, r)
	if !ok {
		return auth.Principal{}, domain.Nil, false
	}
	id, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		platform.WriteError(w, r, platform.NotFound("no such session"))
		return auth.Principal{}, domain.Nil, false
	}
	return p, id, true
}

func (h *SessionHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	logUnexpected(h.logger, r, "sessions", err)
	platform.WriteError(w, r, err)
}

// CardHandler serves the cards endpoints. HTTP concerns only.
type CardHandler struct {
	svc    *CardService
	logger *slog.Logger
}

// NewCardHandler builds a CardHandler. A nil logger means slog.Default(); a nil
// service answers every request 404.
func NewCardHandler(svc *CardService, logger *slog.Logger) *CardHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &CardHandler{svc: svc, logger: logger}
}

// Register mounts the cards routes on the API router.
func (h *CardHandler) Register(rt *platform.Router) {
	rt.HandleFunc("GET /cards", h.List)
	rt.HandleFunc("GET /cards/{problem_id}", h.Get)
}

// List answers GET /cards.
func (h *CardHandler) List(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.svc.List(r.Context(), p.UserID, CardQuery{
		DueBefore: q.Get("due_before"), Tag: q.Get("tag"), Cursor: page.Cursor, Limit: page.Limit,
	})
	if err != nil {
		logUnexpected(h.logger, r, "cards", err)
		platform.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// Get answers GET /cards/{problem_id}.
func (h *CardHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	id, err := domain.ParseID(r.PathValue("problem_id"))
	if err != nil {
		platform.WriteError(w, r, platform.NotFound("no card for this problem"))
		return
	}
	v, err := h.svc.Get(r.Context(), p.UserID, id)
	if err != nil {
		logUnexpected(h.logger, r, "cards", err)
		platform.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *CardHandler) principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
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

// logUnexpected logs an error that is not already a client-facing problem;
// WriteError renders it as an opaque 500.
func logUnexpected(l *slog.Logger, r *http.Request, what string, err error) {
	var pb *platform.Problem
	if !errors.As(err, &pb) {
		l.ErrorContext(r.Context(), what+": request failed", slog.String("error", err.Error()))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
