// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Two simultaneous identical answers to one session item record one attempt:
// the loser gets a 409 and nothing is stored for it.
func TestConcurrentDuplicateAnswersRecordOneAttempt(t *testing.T) {
	e := newProbEnv(t)
	b := mcqBody("Race me", false)
	b["status"] = "live"
	p := e.create(t, e.human, b)
	sid := e.newSession(t, e.human)

	const n = 6
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := e.answer(t, e.human, attemptBody(p, map[string]any{"selected": []string{"c"}}, map[string]any{"session_id": sid}))
			codes[i] = r.code
		}()
	}
	close(start)
	wg.Wait()

	created, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected status %d in %v", c, codes)
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("statuses = %v, want one 201 and %d 409", codes, n-1)
	}
	got, _ := e.listAttempts(t, e.human, nil, http.StatusOK)
	if len(got) != 1 {
		t.Errorf("stored %d attempts, want 1", len(got))
	}
	if !strings.Contains(e.metricsText(t), "quizzotic_attempts_recorded_total 1\n") {
		t.Errorf("want 1 attempt recorded:\n%s", e.metricsText(t))
	}
}
