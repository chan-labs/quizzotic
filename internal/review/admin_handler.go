// SPDX-License-Identifier: Apache-2.0

package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// AdminHandler serves the admin rebuild endpoint. HTTP concerns only; the work is
// Service's.
type AdminHandler struct {
	svc    *Service
	logger *slog.Logger
}

// NewAdminHandler builds an AdminHandler. A nil logger means slog.Default(); a nil
// service answers every request 404.
func NewAdminHandler(svc *Service, logger *slog.Logger) *AdminHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminHandler{svc: svc, logger: logger}
}

// Register mounts the routes on the API router. The scope guard has already
// authenticated the request and checked the admin scope.
func (h *AdminHandler) Register(rt *platform.Router) {
	rt.HandleFunc("POST /admin/rebuild", h.Rebuild)
}

type rebuildRequest struct {
	UserID string `json:"user_id"`
}

// Rebuild answers POST /admin/rebuild. The body is optional.
func (h *AdminHandler) Rebuild(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		platform.WriteError(w, r, platform.Unauthorized("authentication required"))
		return
	}
	// The scope guard already refuses agents (they never hold admin); this
	// keeps the rule even if the route is ever mounted without the guard.
	if p.Kind != auth.KindHuman {
		platform.WriteError(w, r, platform.Forbidden("only a human can rebuild cards"))
		return
	}

	var req rebuildRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platform.MaxBodyBytes))
	if err != nil {
		platform.WriteError(w, r, platform.NewProblem(platform.TypeTooLarge, http.StatusRequestEntityTooLarge, "request body too large"))
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := platform.DecodeJSON(w, r, &req); err != nil {
			platform.WriteError(w, r, err)
			return
		}
	}

	var sum RebuildSummary
	if req.UserID == "" {
		h.logger.Info("admin rebuild requested", slog.String("by", p.UserID.String()), slog.String("scope", "all"))
		sum, err = h.svc.RebuildAll(r.Context())
	} else {
		id, perr := domain.ParseID(req.UserID)
		if perr != nil {
			platform.WriteError(w, r, platform.Validation("the request is invalid",
				platform.FieldError{Field: "user_id", Detail: "not a valid ID"}))
			return
		}
		h.logger.Info("admin rebuild requested", slog.String("by", p.UserID.String()), slog.String("scope", id.String()))
		sum, err = h.svc.RebuildOne(r.Context(), id)
	}
	switch {
	case errors.Is(err, ErrNotFound):
		platform.WriteError(w, r, platform.NotFound("no such user"))
		return
	case err != nil:
		h.logger.Error("admin rebuild failed", slog.String("error", err.Error()))
		platform.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(sum)
}
