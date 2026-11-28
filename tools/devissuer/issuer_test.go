// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
)

// devStack runs the dev issuer and server.Run with the committed dev config
// (dev/quizzotic.dev.yaml) pointed at it, as `mise run dev` does.
func devStack(t *testing.T) (base string, iss *Issuer) {
	t.Helper()
	mux := http.NewServeMux()
	idp := httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	iss, err := New(idp.URL+"/dev", DevUser)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", iss.Handler())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base = "http://" + ln.Addr().String()
	data, err := os.ReadFile("../../dev/quizzotic.dev.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(data, []string{
		"QUIZZOTIC_SERVER_PUBLIC_URL=" + base,
		"QUIZZOTIC_AUTH_ISSUERS_0_ISSUER=" + idp.URL + "/dev",
		"QUIZZOTIC_STORAGE_URL=sqlite://" + filepath.Join(t.TempDir(), "q.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Seed = nil // seeds need a user; dev.sh applies them after sign-in
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, cfg, slog.New(slog.DiscardHandler), server.Options{Listener: ln})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	for range 200 {
		if resp, err := http.Get(base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base, iss
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server not ready")
	return "", nil
}

func TestBrowserLoginLandsOnToday(t *testing.T) {
	base, _ := devStack(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/" {
		t.Fatalf("GET / after sign-in = %d at %s:\n%s", resp.StatusCode, resp.Request.URL, body)
	}
	if !strings.Contains(string(body), "Today") || !strings.Contains(string(body), `action="/auth/logout"`) {
		t.Fatalf("not the signed-in Today page:\n%s", body)
	}
}

func postToken(t *testing.T, endpoint string, form url.Values) map[string]any {
	t.Helper()
	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token %v = %d %v", form.Get("grant_type"), resp.StatusCode, out)
	}
	return out
}

func getWithBearer(t *testing.T, u, tok string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestCLIAndAgentTokensAreAccepted(t *testing.T) {
	base, iss := devStack(t)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// CLI: authorization code with PKCE to a loopback redirect.
	verifier := "a-dev-verifier-that-is-long-enough-1234567890"
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {"quizzotic-cli"}, "state": {"s1"},
		"redirect_uri": {"http://127.0.0.1:9/callback"}, "scope": {"openid offline_access"},
		"code_challenge": {b64.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	resp, err := noFollow.Get(iss.url + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Query().Get("state") != "s1" || loc.Query().Get("code") == "" {
		t.Fatalf("authorize = %d to %s", resp.StatusCode, loc)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"quizzotic-cli"},
		"code": {loc.Query().Get("code")}, "redirect_uri": q["redirect_uri"]}
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("code_verifier", "wrong")
	if r, err := http.PostForm(iss.url+"/token", bad); err != nil || r.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong PKCE verifier accepted: %v %v", r, err)
	} else {
		r.Body.Close()
	}
	// The failed exchange consumed the code, as a real issuer would; get another.
	resp, _ = noFollow.Get(iss.url + "/authorize?" + q.Encode())
	resp.Body.Close()
	loc, _ = url.Parse(resp.Header.Get("Location"))
	form.Set("code", loc.Query().Get("code"))
	form.Set("code_verifier", verifier)
	cli := postToken(t, iss.url+"/token", form)
	if got := getWithBearer(t, base+"/api/v1/me", cli["access_token"].(string)); got != http.StatusOK {
		t.Fatalf("CLI token: GET /me = %d", got)
	}

	// Only loopback redirect URIs are honoured.
	evil := url.Values{}
	for k, v := range q {
		evil[k] = v
	}
	evil.Set("redirect_uri", "https://attacker.example.com/cb")
	if r, err := noFollow.Get(iss.url + "/authorize?" + evil.Encode()); err != nil || r.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-loopback redirect_uri accepted: %v %v", r, err)
	} else {
		r.Body.Close()
	}

	// Device flow, approved at once.
	resp, err = http.PostForm(iss.url+"/device", url.Values{"client_id": {"quizzotic-cli"}})
	if err != nil {
		t.Fatal(err)
	}
	var da map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&da)
	resp.Body.Close()
	dev := postToken(t, iss.url+"/token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id": {"quizzotic-cli"}, "device_code": {da["device_code"].(string)}})
	if got := getWithBearer(t, base+"/api/v1/me", dev["access_token"].(string)); got != http.StatusOK {
		t.Fatalf("device token: GET /me = %d", got)
	}

	// Agent: client credentials, acting for the dev user.
	agent := postToken(t, iss.url+"/token", url.Values{"grant_type": {"client_credentials"},
		"client_id": {"quizzotic-agent"}, "client_secret": {"anything"}})
	if got := getWithBearer(t, base+"/api/v1/problems", agent["access_token"].(string)); got != http.StatusOK {
		t.Fatalf("agent token: GET /problems = %d", got)
	}
}
