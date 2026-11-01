// SPDX-License-Identifier: Apache-2.0

package attempts

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/grading"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Handler serves the attempts endpoints. HTTP concerns only; rules live in
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

// Register mounts the attempts routes on the API router. The scope guard has
// already authenticated and authorized the request.
func (h *Handler) Register(rt *platform.Router) {
	rt.HandleFunc("POST /attempts", h.Create)
}

type createRequest struct {
	ProblemID      string          `json:"problem_id"`
	ProblemVersion int             `json:"problem_version"`
	Response       json.RawMessage `json:"response"`
	LatencyMS      int             `json:"latency_ms"`
	SessionID      string          `json:"session_id"`
	Rating         *int            `json:"rating"`
}

// Create answers POST /attempts.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		platform.WriteError(w, r, platform.Unauthorized("authentication required"))
		return
	}
	// The scope guard already refuses agents (they never hold attempts:write);
	// this keeps the rule even if a route is ever mounted without the guard.
	if p.Kind != auth.KindHuman {
		platform.WriteError(w, r, platform.Forbidden("only a human can answer problems"))
		return
	}
	var req createRequest
	if err := platform.DecodeJSON(w, r, &req); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	// A malformed problem_id is the client's validation error, not a missing
	// resource; an empty one is caught by the service.
	var pid domain.ID
	if req.ProblemID != "" {
		id, err := domain.ParseID(req.ProblemID)
		if err != nil {
			platform.WriteError(w, r, platform.Validation("the attempt is invalid",
				platform.FieldError{Field: "problem_id", Detail: "not a valid ID"}))
			return
		}
		pid = id
	}
	actor := domain.Actor{Kind: domain.ActorKind(p.Kind), UserID: p.UserID, ClientID: p.ClientID}
	res, err := h.svc.Answer(r.Context(), actor, AnswerInput{
		ProblemID: pid, ProblemVersion: req.ProblemVersion, Response: req.Response,
		LatencyMS: req.LatencyMS, SessionID: req.SessionID, Rating: req.Rating,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// fail renders err. Grading errors become client-facing problems; anything
// else that is not already a problem is logged and becomes an opaque 500.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	err = mapGradingError(err)
	var pb *platform.Problem
	if !errors.As(err, &pb) {
		h.logger.ErrorContext(r.Context(), "attempts: create failed", slog.String("error", err.Error()))
	}
	platform.WriteError(w, r, err)
}

// mapGradingError turns the grading package's sentinel errors into problems.
//
//   - ErrInvalidRating: 422. The request was well formed but the rating does
//     not apply to this outcome (rating 1, Again, on a right answer).
//   - ErrRatingRequired: 422 on the rating field.
//   - ErrUnknownProblemType: left as an internal error. The type comes from a
//     stored problem, so reaching it means corrupt data, not a bad request;
//     the caller logs it and answers an opaque 500.
func mapGradingError(err error) error {
	switch {
	case errors.Is(err, grading.ErrInvalidRating):
		return platform.Validation("the rating is not valid for this answer",
			platform.FieldError{Field: "rating", Detail: invalidRatingDetail(err)})
	case errors.Is(err, grading.ErrRatingRequired):
		return platform.Validation("a rating is required",
			platform.FieldError{Field: "rating", Detail: "required for open problems"})
	}
	return err
}

// invalidRatingDetail is the client-facing reason; it never echoes internals.
func invalidRatingDetail(error) string {
	return "rating 1 (Again) is invalid when the answer is right; use 2 (Hard), 3 (Good) or 4 (Easy), or omit it"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
