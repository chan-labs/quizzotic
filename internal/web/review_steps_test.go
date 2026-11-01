// SPDX-License-Identifier: Apache-2.0

package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/problems"
)

// A learning-step card comes back in the same session: the session then holds
// the problem twice. Both presentations are recorded; replays are not.
func TestReviewSecondPresentationOfSameProblemIsRecorded(t *testing.T) {
	e := newTodayEnv(t)
	p := e.create(t, problems.TypeMCQ, "Which?", mcqPayload)
	path := sessionPath(t, e)
	wrong := url.Values{"csrf_token": {testCSRF}, "problem_id": {p.ID.String()}, "problem_version": {"1"}, "selected": {"a"}}

	first := e.do("POST", path+"/answer", wrong)
	if first.Code != http.StatusSeeOther || !strings.Contains(first.Header().Get("Location"), "/result/") {
		t.Fatalf("first answer = %d %q", first.Code, first.Header().Get("Location"))
	}
	if n := e.attemptCount(t); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
	// Replaying the first post before the card is back writes nothing.
	if rec := e.do("POST", path+"/answer", wrong); rec.Header().Get("Location") != path || e.attemptCount(t) != 1 {
		t.Fatalf("replay: %d %q, attempts %d", rec.Code, rec.Header().Get("Location"), e.attemptCount(t))
	}

	// Again puts the card on a learning step; once it has elapsed the same
	// problem is served again in this session.
	e.clock.Advance(2 * time.Minute)
	body := e.do("GET", path, nil).Body.String()
	if !strings.Contains(body, `data-review-phase="prompt"`) || !strings.Contains(body, p.ID.String()) {
		t.Fatalf("problem not served again:\n%s", body)
	}
	right := url.Values{"csrf_token": {testCSRF}, "problem_id": {p.ID.String()}, "problem_version": {"1"}, "selected": {"b"}}
	second := e.do("POST", path+"/answer", right)
	loc := second.Header().Get("Location")
	if second.Code != http.StatusSeeOther || !strings.Contains(loc, "/result/") {
		t.Fatalf("second answer = %d %q", second.Code, loc)
	}
	if n := e.attemptCount(t); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
	if body := e.do("GET", loc, nil).Body.String(); !strings.Contains(body, `class="verdict right"`) {
		t.Errorf("second result page:\n%s", body)
	}
	// Every presentation is answered now: replays of either write nothing.
	for _, f := range []url.Values{wrong, right} {
		if rec := e.do("POST", path+"/answer", f); rec.Header().Get("Location") != path {
			t.Errorf("replay = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	}
	if n := e.attemptCount(t); n != 2 {
		t.Errorf("attempts after replays = %d, want 2", n)
	}
}
