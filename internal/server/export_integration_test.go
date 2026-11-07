// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/export"
	"github.com/chan-labs/quizzotic/internal/server"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

func init() {
	// The export describes the caller and has no ID in its path, so it
	// registers as Self: each user gets their own user_id, and the helper
	// checks neither document mentions the other user.
	ownerScope.Self(ownerscope.Self{
		Pattern: "GET /export",
		UserID: func(body []byte) string {
			var d struct {
				UserID string `json:"user_id"`
			}
			_ = json.Unmarshal(body, &d)
			return d.UserID
		},
	})
}

// TestExportThroughTheRealServer starts the server the way `quizzotic serve`
// does (server.Run, a real config.Config, a real SQLite file) and exports over
// HTTP: the route, the admin scope and the owner scoping are all installed by
// the production wiring.
func TestExportThroughTheRealServer(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := config.Default()
	cfg.Server.PublicURL = "https://quizzotic.example.com"
	cfg.Storage.URL = "sqlite://" + filepath.Join(t.TempDir(), "q.db")
	cfg.Auth.Issuers = []config.Issuer{{
		Name: "test", Issuer: idp.srv.URL, Audiences: []string{itAudience},
		Scopes: []string{"openid", "profile", "email"},
		Clients: []config.Client{
			{ID: "quizzotic-cli", Kind: "human"},
			{ID: "quizzotic-agent", Kind: "agent", AllowedScopes: []string{"problems:read", "problems:write"}},
		},
	}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, &cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), server.Options{Listener: ln})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			t.Logf("server stopped: %v", err)
		case <-time.After(10 * time.Second):
			t.Error("server did not shut down")
		}
	})
	base := "http://" + ln.Addr().String() + "/api/v1"

	do := func(method, path, tok string, body any) (int, http.Header, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		var resp *http.Response
		for i := 0; i < 100; i++ { // the server is starting
			req, _ := http.NewRequest(method, base+path, rd)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			if resp, err = http.DefaultClient.Do(req); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, b
	}

	alice := idp.access("quizzotic-cli", "alice-sub", with("name", "Alice Export"), with("email", "alice@example.com"))
	bob := idp.access("quizzotic-cli", "bob-sub", with("name", "Bob Export"), with("email", "bob@example.com"))
	agent := idp.access("quizzotic-agent", "alice-sub")

	for who, tok := range map[string]string{"alice": alice, "bob": bob} {
		code, _, b := do("POST", "/problems", tok, map[string]any{
			"type": "open", "status": "live", "prompt": "Question from " + who + "?",
			"payload": map[string]any{"reference_answer": "answer of " + who}})
		if code != http.StatusCreated {
			t.Fatalf("create as %s = %d (%s)", who, code, b)
		}
	}

	if code, _, _ := do("GET", "/export", "", nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous export = %d, want 401", code)
	}
	if code, _, _ := do("GET", "/export", agent, nil); code != http.StatusForbidden {
		t.Errorf("agent export = %d, want 403 (agents never hold admin)", code)
	}

	code, hdr, body := do("GET", "/export", alice, nil)
	if code != http.StatusOK {
		t.Fatalf("export = %d (%s)", code, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, "quizzotic-export.json") {
		t.Errorf("content disposition = %q", cd)
	}
	var doc struct {
		Format string                       `json:"format"`
		Tables map[string][]json.RawMessage `json:"tables"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("export is not JSON: %v\n%s", err, body)
	}
	if doc.Format != export.FormatName || len(doc.Tables["users"]) != 1 || len(doc.Tables["problems"]) != 1 ||
		len(doc.Tables["problem_versions"]) != 1 || len(doc.Tables["identities"]) != 1 {
		t.Errorf("document = %s", body)
	}
	for _, leaked := range []string{"Bob Export", "bob@example.com", "bob-sub", "Question from bob", "answer of bob"} {
		if strings.Contains(string(body), leaked) {
			t.Errorf("alice's export contains %q", leaked)
		}
	}
	if !strings.Contains(string(body), "Question from alice") {
		t.Error("alice's export lacks her problem")
	}
}
