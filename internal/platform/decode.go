// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// MaxBodyBytes caps JSON request bodies.
const MaxBodyBytes = 1 << 20

// DecodeJSON strictly decodes the request body into dst: unknown fields,
// trailing data, and empty bodies are rejected with a 400 problem; oversize
// bodies get 413.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		var syn *json.SyntaxError
		var typ *json.UnmarshalTypeError
		switch {
		case errors.As(err, &mbe):
			return NewProblem(TypeTooLarge, http.StatusRequestEntityTooLarge, "request body too large")
		case errors.Is(err, io.EOF):
			return BadRequest("request body is empty")
		case errors.As(err, &syn), errors.Is(err, io.ErrUnexpectedEOF):
			return BadRequest("request body is not valid JSON")
		case errors.As(err, &typ):
			return BadRequest("field " + typ.Field + " has the wrong type")
		default:
			// DisallowUnknownFields errors are plain strings
			// ("json: unknown field \"x\""); they are safe to echo.
			return BadRequest(err.Error())
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return BadRequest("request body must contain a single JSON value")
	}
	return nil
}
