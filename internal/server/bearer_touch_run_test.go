// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"testing"
	"time"
)

// Bearer requests through server.Run advance the identity's last_seen_at at
// most once a minute, while profile claim changes still apply at once.
func TestRunBearerRequestsThrottleLastSeen(t *testing.T) {
	e := newRunEnv(t)
	ctx := t.Context()
	issuer := e.idp.srv.URL

	get := func(tok string) {
		t.Helper()
		if r := e.req(t, "GET", "/me", tok, nil, nil); r.code != http.StatusOK {
			t.Fatalf("GET /me = %d (%s)", r.code, r.body)
		}
	}
	seen := func() time.Time {
		t.Helper()
		id, err := e.st.Identities().GetByIssuerSubject(ctx, issuer, "author-sub")
		if err != nil {
			t.Fatal(err)
		}
		return id.LastSeenAt
	}

	get(e.human) // first request provisions the user
	first := seen()
	time.Sleep(10 * time.Millisecond) // a write now would store a later time
	get(e.human)
	if got := seen(); !got.Equal(first) {
		t.Fatalf("second bearer request within a minute wrote last_seen_at: %v -> %v", first, got)
	}

	// Once the stored value is older than the interval, the next request
	// advances it.
	id, err := e.st.Identities().GetByIssuerSubject(ctx, issuer, "author-sub")
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * time.Minute).Truncate(time.Millisecond)
	if err := e.st.Identities().TouchLastSeen(ctx, id.UserID, id.ID, stale); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	get(e.human)
	touched := seen()
	if !touched.After(before) {
		t.Fatalf("request after the interval did not advance last_seen_at: %v (stale %v)", touched, stale)
	}

	// A profile change inside the interval still applies immediately, and
	// last_seen_at stays put.
	get(e.idp.access("quizzotic-cli", "author-sub", with("name", "Renamed"), with("email", "renamed@example.com")))
	u, err := e.st.Users().Get(ctx, id.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if u.DisplayName != "Renamed" || u.Email != "renamed@example.com" {
		t.Fatalf("profile change not applied: %+v", u)
	}
	if got := seen(); !got.Equal(touched) {
		t.Fatalf("profile-only request moved last_seen_at: %v -> %v", touched, got)
	}
}
