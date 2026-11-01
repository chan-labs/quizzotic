// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"net/http"
	"strings"
)

// SetETag sets the ETag response header. etag must be a quoted strong
// validator such as `"3-1700000000000"`.
func SetETag(w http.ResponseWriter, etag string) {
	w.Header().Set("ETag", etag)
}

// RequireIfMatch returns the request's If-Match header value, or a 428
// problem when it is absent or blank. Call it before reading the body so a
// client that forgot the header learns immediately.
func RequireIfMatch(r *http.Request) (string, error) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return "", PreconditionRequired("If-Match header is required; send the ETag from the last read")
	}
	return v, nil
}

// IfMatchSatisfied reports whether the If-Match value ifMatch allows a write
// to a resource whose current ETag is current. Per RFC 9110 it uses strong
// comparison: "*" matches any existing resource, a comma-separated list
// matches when any member equals current, and weak validators (W/"...")
// never match.
func IfMatchSatisfied(ifMatch, current string) bool {
	ifMatch = strings.TrimSpace(ifMatch)
	if ifMatch == "*" {
		return true
	}
	if current == "" || strings.HasPrefix(current, "W/") {
		return false
	}
	for _, c := range strings.Split(ifMatch, ",") {
		if strings.TrimSpace(c) == current {
			return true
		}
	}
	return false
}

// CheckIfMatch returns a 412 problem unless ifMatch satisfies current. Run it
// inside the same transaction (or under the same lock) as the write so the
// check and the update are atomic.
func CheckIfMatch(ifMatch, current string) error {
	if !IfMatchSatisfied(ifMatch, current) {
		return PreconditionFailed("If-Match does not match the resource's current ETag; re-read and retry")
	}
	return nil
}
