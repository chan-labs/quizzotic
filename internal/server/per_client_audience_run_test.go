// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
)

// Through server.Run, the path `quizzotic serve` takes: an issuer that lists
// one audience per OAuth client (an IdP that sets aud to the client ID)
// authenticates and classifies web, CLI, agent and headless tokens, and
// rejects an audience that is not listed.
func TestRunPerClientAudiences(t *testing.T) {
	idp := newFakeIdP(t)
	const (
		web, cli, agent, headless = "web-client-id", "cli-client-id", "agent-client-id", "headless-client-id"
	)
	cfg := config.Default()
	cfg.Server.PublicURL = "https://quizzotic.example.com"
	cfg.Storage.URL = "sqlite://" + filepath.Join(t.TempDir(), "q.db")
	cfg.Auth.Issuers = []config.Issuer{{
		Name: "test", Issuer: idp.srv.URL,
		Audiences: []string{web, cli, agent, headless},
		Scopes:    []string{"openid", "profile", "email"},
		Clients: []config.Client{
			{ID: web, Kind: "human", Secret: "web-secret"},
			{ID: cli, Kind: "human"},
			{ID: agent, Kind: "agent", AllowedScopes: []string{"problems:read"}},
			{ID: headless, Kind: "agent", ActsFor: "owner-sub", AllowedScopes: []string{"metrics:read"}},
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
	base := "http://" + ln.Addr().String()
	ready := false
	for i := 0; i < 100 && !ready; i++ {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			ready = true
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !ready {
		t.Fatal("server did not start")
	}

	me := func(tok string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Kind     string `json:"kind"`
			ClientID string `json:"client_id"`
		}
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out.Kind, out.ClientID
	}
	noClientClaim := func(c map[string]any) { delete(c, "client_id") }

	for _, tc := range []struct {
		name, client, kind string
		mutate             []func(map[string]any)
	}{
		{"web", web, "human", []func(map[string]any){with("aud", web)}},
		{"cli", cli, "human", []func(map[string]any){with("aud", cli)}},
		{"agent", agent, "agent", []func(map[string]any){with("aud", agent)}},
		{"headless", headless, "agent", []func(map[string]any){with("aud", headless)}},
		{"cli aud array", cli, "human", []func(map[string]any){with("aud", []string{"other", cli})}},
		{"cli azp only", cli, "human", []func(map[string]any){with("aud", cli), noClientClaim, with("azp", cli)}},
		{"agent aud only", agent, "agent", []func(map[string]any){with("aud", agent), noClientClaim}},
	} {
		code, kind, client := me(idp.access(tc.client, "sub-"+tc.name, tc.mutate...))
		if code != http.StatusOK || kind != tc.kind || client != tc.client {
			t.Errorf("%s: got %d kind=%q client=%q, want 200 kind=%q client=%q", tc.name, code, kind, client, tc.kind, tc.client)
		}
	}

	for name, mutate := range map[string]func(map[string]any){
		"unlisted aud":      with("aud", "unlisted-client-id"),
		"unlisted aud list": with("aud", []string{"x", "y"}),
		"old fixed aud":     with("aud", "quizzotic"),
	} {
		if code, _, _ := me(idp.access(cli, "sub-bad", mutate)); code != http.StatusUnauthorized {
			t.Errorf("%s: code = %d, want 401", name, code)
		}
	}
}
