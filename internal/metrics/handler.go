// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Handler serves the metrics endpoints. HTTP concerns only; the figures come
// from QueryService.
type Handler struct {
	svc    *QueryService
	logger *slog.Logger
}

// NewHandler builds a Handler. A nil logger means slog.Default(); a nil
// service answers every request 404.
func NewHandler(svc *QueryService, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger}
}

// Register mounts the metrics routes on the API router. The scope guard has
// already authenticated and authorized the request.
func (h *Handler) Register(rt *platform.Router) {
	rt.HandleFunc("GET /metrics/overview", h.Overview)
	rt.HandleFunc("GET /metrics/tags", h.Tags)
	rt.HandleFunc("GET /metrics/tags/{tag}/history", h.TagHistory)
	rt.HandleFunc("GET /metrics/problems", h.Problems)
}

// Overview answers GET /metrics/overview.
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Overview(r.Context(), p.UserID)
	h.write(w, r, v, err)
}

// Tags answers GET /metrics/tags.
func (h *Handler) Tags(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	v, err := h.svc.Tags(r.Context(), p.UserID, page.Cursor, page.Limit)
	h.write(w, r, v, err)
}

// TagHistory answers GET /metrics/tags/{tag}/history.
func (h *Handler) TagHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v, err := h.svc.TagHistory(r.Context(), p.UserID, r.PathValue("tag"), q.Get("from"), q.Get("to"))
	h.write(w, r, v, err)
}

// Problems answers GET /metrics/problems.
func (h *Handler) Problems(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var fields []platform.FieldError
	if s := q.Get("sort"); s != "" && s != "recall" {
		fields = append(fields, platform.FieldError{Field: "sort", Detail: "must be recall"})
	}
	limit := DefaultWeakProblems
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > platform.MaxLimit {
			fields = append(fields, platform.FieldError{Field: "limit",
				Detail: "must be an integer between 1 and " + strconv.Itoa(platform.MaxLimit)})
		}
		limit = n
	}
	if len(fields) > 0 {
		platform.WriteError(w, r, platform.Validation("invalid parameters", fields...))
		return
	}
	items, err := h.svc.WeakProblems(r.Context(), p.UserID, limit)
	h.write(w, r, struct {
		Items []WeakProblemView `json:"items"`
	}{items}, err)
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

func (h *Handler) write(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		var pb *platform.Problem
		if !errors.As(err, &pb) {
			h.logger.ErrorContext(r.Context(), "metrics: request failed", slog.String("error", err.Error()))
		}
		platform.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
