// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"database/sql"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/store/sqlite"
	"github.com/chan-labs/quizzotic/internal/testsupport/serverrun"
)

// Through server.Run: while another connection holds SQLite's write lock,
// read endpoints still answer at once. The bearer lookup, the metrics overview
// and the tag list all use read-only transactions, so none waits for the lock
// (the 5 s busy_timeout) or fails with SQLITE_BUSY.
func TestRunReadsDoNotWaitForTheWriteLock(t *testing.T) {
	idp := newFakeIDP(t)
	cfg := config.Default()
	cfg.Server.PublicURL = "https://quizzotic.example.com"
	dbPath := filepath.Join(t.TempDir(), "q.db")
	cfg.Storage.URL = "sqlite://" + dbPath
	cfg.Auth.Issuers = []config.Issuer{{
		Name: "test", Issuer: idp.srv.URL, Audiences: []string{itAudience},
		Scopes:  []string{"openid", "profile", "email"},
		Clients: []config.Client{{ID: "quizzotic-cli", Kind: "human"}},
	}}
	srv := serverrun.Start(t, &cfg)
	tok := idp.access("quizzotic-cli", "reader-sub", with("name", "Reader"), with("email", "reader@example.com"))
	get := func(path string) (int, time.Duration) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		start := time.Now()
		resp, err := srv.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, time.Since(start)
	}
	// The first request provisions the user (a write); later ones find the
	// identity fresh and write nothing.
	if code, _ := get("/metrics/overview"); code != http.StatusOK {
		t.Fatalf("warm-up overview = %d", code)
	}

	// Hold the write lock from outside the server until the test ends.
	db, err := sql.Open("sqlite", sqlite.DSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tx, err := db.BeginTx(t.Context(), nil) // _txlock=immediate: takes the lock now
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })

	for _, path := range []string{"/metrics/overview", "/tags"} {
		code, took := get(path)
		if code != http.StatusOK {
			t.Errorf("GET %s with the write lock held = %d, want 200", path, code)
		}
		if took > 100*time.Millisecond {
			t.Errorf("GET %s took %v with the write lock held, want < 100ms", path, took)
		}
	}
}
