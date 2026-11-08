// SPDX-License-Identifier: Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/cli"
)

// fakeIDP is a minimal OIDC issuer with a device flow that records how it was
// used.
type fakeIDP struct {
	srv       *httptest.Server
	discovery int
	clientID  string
}

func newIDP(t *testing.T) *fakeIDP {
	t.Helper()
	f := &fakeIDP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		f.discovery++
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                        f.srv.URL,
			"token_endpoint":                f.srv.URL + "/token",
			"device_authorization_endpoint": f.srv.URL + "/device",
		})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.clientID = r.PostForm.Get("client_id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc", "user_code": "UC", "verification_uri": f.srv.URL + "/v", "interval": 1,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// login runs "quizzotic login --device" with a config dir and extra env.
func login(t *testing.T, dir string, env map[string]string, args ...string) (int, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Login(context.Background(), append([]string{"--device"}, args...), cli.Env{
		Stdout: &o, Stderr: &e,
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return dir
			}
			return env[k]
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
		Now:   time.Now,
	})
	return code, e.String()
}

func TestConfigIssuerPrecedence(t *testing.T) {
	file, env, flag := newIDP(t), newIDP(t), newIDP(t)
	dir := writeConfig(t, "issuer: "+file.srv.URL+"\n")

	if code, e := login(t, dir, nil); code != 0 || file.discovery != 1 {
		t.Fatalf("file only: code=%d file=%d err=%s", code, file.discovery, e)
	}
	if code, e := login(t, dir, map[string]string{"QUIZZOTIC_ISSUER": env.srv.URL}); code != 0 || env.discovery != 1 || file.discovery != 1 {
		t.Fatalf("env beats file: code=%d env=%d file=%d err=%s", code, env.discovery, file.discovery, e)
	}
	code, e := login(t, dir, map[string]string{"QUIZZOTIC_ISSUER": env.srv.URL}, "--issuer", flag.srv.URL)
	if code != 0 || flag.discovery != 1 || env.discovery != 1 || file.discovery != 1 {
		t.Fatalf("flag beats both: code=%d flag=%d env=%d file=%d err=%s", code, flag.discovery, env.discovery, file.discovery, e)
	}
}

func TestConfigLoginClientIDPrecedence(t *testing.T) {
	idp := newIDP(t)
	dir := writeConfig(t, "issuer: "+idp.srv.URL+"\nlogin_client_id: from-file\n")
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"file", nil, nil, "from-file"},
		{"env beats file", map[string]string{"QUIZZOTIC_LOGIN_CLIENT_ID": "from-env"}, nil, "from-env"},
		{"flag beats env", map[string]string{"QUIZZOTIC_LOGIN_CLIENT_ID": "from-env"}, []string{"--client-id", "from-flag"}, "from-flag"},
	} {
		idp.clientID = ""
		if code, e := login(t, dir, tc.env, tc.args...); code != 0 || idp.clientID != tc.want {
			t.Fatalf("%s: code=%d client=%q want %q err=%s", tc.name, code, idp.clientID, tc.want, e)
		}
	}
}

func TestLoginMalformedConfig(t *testing.T) {
	idp := newIDP(t)
	dir := writeConfig(t, "issuer: [unclosed\n")
	cases := map[string]map[string]string{
		// client-id path: reached first when no flag/env supplies a client ID.
		"client id path": nil,
		// resolveIssuer path: client ID is supplied, issuer is not.
		"issuer path": {"QUIZZOTIC_LOGIN_CLIENT_ID": "c", "QUIZZOTIC_SERVER": idp.srv.URL},
	}
	for name, env := range cases {
		code, e := login(t, dir, env)
		if code != 1 || !strings.Contains(e, "config.yaml") || !strings.Contains(e, "not valid") {
			t.Fatalf("%s: code=%d err=%q", name, code, e)
		}
	}
	if idp.discovery != 0 {
		t.Fatalf("no IdP call expected, got %d", idp.discovery)
	}
}

func TestServerBaseReadsConfigWithNilGetenv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte("server: https://quiz.example.com/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUIZZOTIC_CLI_CONFIG", p)
	t.Setenv("QUIZZOTIC_SERVER", "")
	got, err := cli.ServerBase("", cli.Env{})
	if err != nil || got != "https://quiz.example.com" {
		t.Fatalf("got %q err %v", got, err)
	}
	if got, _ := cli.ServerBase("https://flag.example.com", cli.Env{}); got != "https://flag.example.com" {
		t.Fatalf("flag: %q", got)
	}

	if err := os.WriteFile(p, []byte("server: [bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ServerBase("", cli.Env{}); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("malformed: %v", err)
	}
}
