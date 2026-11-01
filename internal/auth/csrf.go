// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/subtle"
	"net/http"
)

// CSRF transport names. A form carries the token in the hidden field; script
// callers may use the header.
const (
	CSRFFormField = "csrf_token"
	CSRFHeader    = "X-CSRF-Token"
)

// Forms embed WebSession.CSRFToken in a hidden csrf_token field.

// CheckCSRF reports whether r carries the session's CSRF token, in the
// X-CSRF-Token header or the csrf_token form field, compared in constant time.
// Call it on every state-changing request authenticated by the session cookie.
func CheckCSRF(r *http.Request, ws WebSession) bool {
	if ws.CSRFToken == "" {
		return false
	}
	got := r.Header.Get(CSRFHeader)
	if got == "" {
		got = r.PostFormValue(CSRFFormField)
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(ws.CSRFToken)) == 1
}
