// SPDX-License-Identifier: Apache-2.0

package tags

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/problems"
)

// Handler serves the tag endpoints. HTTP concerns only; rules live in Service.
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

// Register mounts the tag routes on the API router.
func (h *Handler) Register(rt *platform.Router) {
	rt.HandleFunc("GET /tags", h.list)
	rt.HandleFunc("POST /tags", h.create)
	rt.HandleFunc("GET /tags/{tag}", h.get)
	rt.HandleFunc("PATCH /tags/{tag}", h.update)
	rt.HandleFunc("GET /tags/{tag}/problems", h.listProblems)
	rt.HandleFunc("PUT /problems/{id}/tags", h.setProblemTags)
	h.registerOps(rt)
}

// tagView is the JSON form of a Tag.
type tagView struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      Status    `json:"status"`
	MergedInto  *string   `json:"merged_into"`
	Aliases     []string  `json:"aliases"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func viewOf(t Tag) tagView {
	v := tagView{ID: t.ID.String(), Slug: t.Slug, Name: t.Name, Description: t.Description,
		Status: t.Status, Aliases: t.Aliases, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
	if v.Aliases == nil {
		v.Aliases = []string{}
	}
	if !t.MergedInto.IsNil() {
		s := t.MergedInto.String()
		v.MergedInto = &s
	}
	return v
}

type taggedProblemView struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	CurrentVersion int       `json:"current_version"`
	Type           string    `json:"type"`
	Prompt         string    `json:"prompt"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// owner returns the calling user, or writes the 404/401 and returns false.
func (h *Handler) owner(w http.ResponseWriter, r *http.Request) (domain.ID, bool) {
	if h.svc == nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return domain.Nil, false
	}
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		platform.WriteError(w, r, platform.Unauthorized("authentication required"))
		return domain.Nil, false
	}
	return p.UserID, true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, problems.ErrNotFound):
		err = platform.NotFound("no such resource")
	case errors.Is(err, ErrOpNotFound):
		err = platform.NotFound("no such resource")
	case errors.Is(err, ErrOpNotProposed):
		err = platform.Conflict("the operation is already decided")
	case errors.Is(err, ErrOpNotApplied):
		err = platform.Conflict("only an applied operation can be reverted")
	case errors.Is(err, ErrOpReverted):
		err = platform.Conflict("the operation is already reverted")
	case errors.Is(err, ErrRevertConflict):
		err = platform.Conflict("a later applied operation touched the same tags; revert that one first")
	case errors.Is(err, ErrSlugTaken):
		err = platform.Conflict("slug is already used by an active tag or an alias")
	case errors.Is(err, ErrPreconditionFailed):
		err = platform.PreconditionFailed("If-Match does not match the tag's current ETag")
	default:
		var p *platform.Problem
		if !errors.As(err, &p) {
			// The cause stays in the log; the client gets an opaque 500.
			h.logger.ErrorContext(r.Context(), "tags: request failed", slog.String("error", err.Error()))
		}
	}
	platform.WriteError(w, r, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeTag(w http.ResponseWriter, status int, t Tag) {
	platform.SetETag(w, ETag(t))
	writeJSON(w, status, viewOf(t))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	ts, next, err := h.svc.List(r.Context(), owner, Status(r.URL.Query().Get("status")), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]tagView, len(ts))
	for i, t := range ts {
		items[i] = viewOf(t)
	}
	writeJSON(w, http.StatusOK, platform.NewPageResult(items, next))
}

type createBody struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	var body createBody
	if err := platform.DecodeJSON(w, r, &body); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	t, err := h.svc.Create(r.Context(), owner, CreateInput(body))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", platform.APIPrefix+"/tags/"+t.ID.String())
	writeTag(w, http.StatusCreated, t)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	t, err := h.svc.Get(r.Context(), owner, r.PathValue("tag"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeTag(w, http.StatusOK, t)
}

type updateBody struct {
	Slug        *string `json:"slug"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
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
	if body.Slug == nil && body.Name == nil && body.Description == nil {
		platform.WriteError(w, r, platform.Validation("nothing to update"))
		return
	}
	t, err := h.svc.Update(r.Context(), owner, r.PathValue("tag"), ifMatch, Patch(body))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeTag(w, http.StatusOK, t)
}

func (h *Handler) listProblems(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	page, err := platform.ParsePage(r)
	if err != nil {
		platform.WriteError(w, r, err)
		return
	}
	ps, next, err := h.svc.ListProblems(r.Context(), owner, r.PathValue("tag"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]taggedProblemView, len(ps))
	for i, p := range ps {
		items[i] = taggedProblemView{ID: p.Problem.ID.String(), Status: string(p.Problem.Status),
			CurrentVersion: p.Problem.CurrentVersion, Type: p.Version.Type, Prompt: p.Version.Prompt,
			CreatedAt: p.Problem.CreatedAt, UpdatedAt: p.Problem.UpdatedAt}
	}
	writeJSON(w, http.StatusOK, platform.NewPageResult(items, next))
}

type setTagsBody struct {
	Tags []string `json:"tags"`
}

func (h *Handler) setProblemTags(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.owner(w, r)
	if !ok {
		return
	}
	pid, err := domain.ParseID(r.PathValue("id"))
	if err != nil {
		platform.WriteError(w, r, platform.NotFound("no such resource"))
		return
	}
	var body setTagsBody
	if err := platform.DecodeJSON(w, r, &body); err != nil {
		platform.WriteError(w, r, err)
		return
	}
	if body.Tags == nil {
		platform.WriteError(w, r, platform.Validation("invalid body", platform.FieldError{Field: "tags", Detail: "is required"}))
		return
	}
	ts, err := h.svc.SetProblemTags(r.Context(), owner, pid, body.Tags)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]tagView, len(ts))
	for i, t := range ts {
		items[i] = viewOf(t)
	}
	writeJSON(w, http.StatusOK, struct {
		Items []tagView `json:"items"`
	}{items})
}
