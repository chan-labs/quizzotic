// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/server"
)

// The background sweep that server.Run starts deletes expired web sessions
// and login states and keeps live sessions.
func TestRunSweepsExpiredSessionsAndLoginStates(t *testing.T) {
	e, _ := startRunEnvOpts(t, newFakeIDP(t), filepath.Join(t.TempDir(), "q.db"), nil, nil,
		server.Options{SessionSweepInterval: 50 * time.Millisecond})
	ctx := context.Background()

	// A live session from a real browser login.
	cookie := e.webLogin(t)
	sum := sha256.Sum256([]byte(cookie))
	liveHash := hex.EncodeToString(sum[:])
	live, err := e.st.WebSessions().GetByTokenHash(ctx, liveHash)
	if err != nil {
		t.Fatal(err)
	}

	// An expired session of the same user and an abandoned, expired login
	// state, written after the login so its own purge cannot remove them.
	past := time.Now().Add(-time.Hour)
	ids := domain.NewIDGenerator(domain.SystemClock{})
	if err := e.st.WebSessions().Create(ctx, auth.WebSession{
		ID: ids.New(), UserID: live.UserID, TokenHash: "expired-session", CSRFToken: "c",
		CreatedAt: past.Add(-time.Hour), LastSeenAt: past.Add(-time.Hour), ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.LoginStates().Create(ctx, auth.LoginState{
		StateHash: "expired-state", Nonce: "n", CodeVerifier: "v", ReturnTo: "/", ExpiresAt: past,
	}); err != nil {
		t.Fatal(err)
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.st.DB().QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		sess := count(`SELECT COUNT(*) FROM web_sessions WHERE token_hash = ?`, "expired-session")
		states := count(`SELECT COUNT(*) FROM login_states WHERE state_hash = ?`, "expired-state")
		if sess == 0 && states == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sweep did not run: expired sessions=%d login states=%d", sess, states)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, err := e.st.WebSessions().GetByTokenHash(ctx, liveHash); err != nil || got.ID != live.ID {
		t.Fatalf("live session removed: %+v err=%v", got, err)
	}
	// The live session's cookie still authenticates.
	if r := e.do(t, "GET", "/", nil, cookie); r.code != http.StatusOK {
		t.Fatalf("GET / with the live session = %d", r.code)
	}
}
