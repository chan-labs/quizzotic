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
	"sync"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
)

// Through server.Run, the path `quizzotic serve` takes: simultaneous identical
// answers to one session item record one attempt; the rest are 409.
func TestRunConcurrentDuplicateAttemptsRecordOne(t *testing.T) {
	idp := newFakeIDP(t)
	cfg := config.Default()
	cfg.Server.PublicURL = "https://quizzotic.example.com"
	cfg.Storage.URL = "sqlite://" + filepath.Join(t.TempDir(), "q.db")
	cfg.Auth.Issuers = []config.Issuer{{
		Name: "test", Issuer: idp.srv.URL, Audiences: []string{itAudience},
		Scopes:  []string{"openid", "profile", "email"},
		Clients: []config.Client{{ID: "quizzotic-cli", Kind: "human"}},
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
	tok := idp.access("quizzotic-cli", "racer-sub", with("name", "Racer"), with("email", "racer@example.com"))
	call := func(method, path string, body any) (int, []byte) {
		var rd io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, base+path, rd)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	ready := false
	for i := 0; i < 100 && !ready; i++ {
		if resp, err := http.Get("http://" + ln.Addr().String() + "/healthz"); err == nil {
			resp.Body.Close()
			ready = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !ready {
		t.Fatal("server did not start")
	}

	b := mcqBody("Race through Run", false)
	b["status"] = "live"
	code, body := call("POST", "/problems", b)
	if code != http.StatusCreated {
		t.Fatalf("create problem = %d (%s)", code, body)
	}
	var prob struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(body, &prob); err != nil {
		t.Fatal(err)
	}
	code, body = call("POST", "/sessions", map[string]any{"kind": "review"})
	if code != http.StatusCreated {
		t.Fatalf("create session = %d (%s)", code, body)
	}
	var sess struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &sess); err != nil {
		t.Fatal(err)
	}

	const n = 6
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i], _ = call("POST", "/attempts", map[string]any{"problem_id": prob.ID, "problem_version": prob.Version,
				"latency_ms": 5, "session_id": sess.ID, "response": map[string]any{"selected": []string{"c"}}})
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
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("statuses = %v, want one 201 and %d 409", codes, n-1)
	}
	code, body = call("GET", "/attempts", nil)
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil || code != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("attempts = %d %s (%v), want exactly 1", code, body, err)
	}
}
