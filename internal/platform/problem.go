// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ProblemContentType is the media type of RFC 9457 error bodies.
const ProblemContentType = "application/problem+json"

// Stable problem type URIs. These are identifiers, not fetchable URLs, and
// must never change once released: clients and agents key on them.
const (
	TypeBadRequest       = "urn:quizzotic:problem:bad-request"
	TypeValidation       = "urn:quizzotic:problem:validation"
	TypeUnauthorized     = "urn:quizzotic:problem:unauthorized"
	TypeForbidden        = "urn:quizzotic:problem:forbidden"
	TypeNotFound         = "urn:quizzotic:problem:not-found"
	TypeMethodNotAllowed = "urn:quizzotic:problem:method-not-allowed"
	TypeConflict         = "urn:quizzotic:problem:conflict"
	TypePreconditionFail = "urn:quizzotic:problem:precondition-failed"
	TypeTooLarge         = "urn:quizzotic:problem:payload-too-large"
	TypeInternal         = "urn:quizzotic:problem:internal"
)

// Problem is an RFC 9457 problem detail. It implements error so services can
// return it and handlers can render it with WriteError.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	// Errors carries per-field validation failures.
	Errors []FieldError `json:"errors,omitempty"`
}

// FieldError describes one invalid field.
type FieldError struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return p.Title + ": " + p.Detail
	}
	return p.Title
}

// NewProblem builds a Problem with the given type URI, status and detail.
// The title is the standard status text.
func NewProblem(typ string, status int, detail string) *Problem {
	return &Problem{Type: typ, Title: http.StatusText(status), Status: status, Detail: detail}
}

// BadRequest returns a 400 problem.
func BadRequest(detail string) *Problem {
	return NewProblem(TypeBadRequest, http.StatusBadRequest, detail)
}

// Unauthorized returns a 401 problem.
func Unauthorized(detail string) *Problem {
	return NewProblem(TypeUnauthorized, http.StatusUnauthorized, detail)
}

// Forbidden returns a 403 problem.
func Forbidden(detail string) *Problem {
	return NewProblem(TypeForbidden, http.StatusForbidden, detail)
}

// NotFound returns a 404 problem.
func NotFound(detail string) *Problem {
	return NewProblem(TypeNotFound, http.StatusNotFound, detail)
}

// Conflict returns a 409 problem.
func Conflict(detail string) *Problem {
	return NewProblem(TypeConflict, http.StatusConflict, detail)
}

// PreconditionFailed returns a 412 problem (stale ETag).
func PreconditionFailed(detail string) *Problem {
	return NewProblem(TypePreconditionFail, http.StatusPreconditionFailed, detail)
}

// Validation returns a 422 problem listing the invalid fields.
func Validation(detail string, fields ...FieldError) *Problem {
	p := NewProblem(TypeValidation, http.StatusUnprocessableEntity, detail)
	p.Errors = fields
	return p
}

// WriteError renders err as problem+json. A *Problem (possibly wrapped) is
// rendered as is; any other error becomes an opaque 500 so internals never
// leak. Instance and request ID are filled from the request.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var p *Problem
	if !errors.As(err, &p) {
		p = NewProblem(TypeInternal, http.StatusInternalServerError, "")
	}
	out := *p // do not mutate a shared Problem
	if out.Instance == "" {
		out.Instance = r.URL.Path
		if full, ok := r.Context().Value(fullPathKey{}).(string); ok {
			out.Instance = full
		}
	}
	if out.RequestID == "" {
		out.RequestID = RequestIDFrom(r.Context())
	}
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(out.Status)
	_ = json.NewEncoder(w).Encode(&out)
}
