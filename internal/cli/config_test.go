// SPDX-License-Identifier: Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/cli"
)

// runCfg runs "quizzotic meta me" with a config dir and extra env.
func runCfg(t *testing.T, dir string, env map[string]string, args ...string) (int, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Run(context.Background(), append([]string{"meta", "me"}, args...), cli.Env{
		Stdout: &o, Stderr: &e,
		Getenv: func(k string) string {
			switch k {
			case "XDG_CONFIG_HOME":
				return dir
			case "QUIZZOTIC_TOKEN":
				return "tok"
			}
			return env[k]
		},
	})
	return code, e.String()
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "quizzotic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quizzotic", "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func hitCounter(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestConfigFileServer(t *testing.T) {
	srv, hits := hitCounter(t)
	// 0644 on purpose: the file holds no secrets, so no mode is required.
	dir := writeConfig(t, "server: "+srv.URL+"\n")
	if code, errOut := runCfg(t, dir, nil); code != 0 || *hits != 1 {
		t.Fatalf("file only: code=%d hits=%d err=%s", code, *hits, errOut)
	}
}

func TestEnvOverridesConfigFile(t *testing.T) {
	file, fileHits := hitCounter(t)
	envSrv, envHits := hitCounter(t)
	dir := writeConfig(t, "server: "+file.URL+"\n")
	if code, errOut := runCfg(t, dir, map[string]string{"QUIZZOTIC_SERVER": envSrv.URL}); code != 0 || *envHits != 1 || *fileHits != 0 {
		t.Fatalf("env: code=%d env=%d file=%d err=%s", code, *envHits, *fileHits, errOut)
	}
}

func TestFlagOverridesEnvAndConfigFile(t *testing.T) {
	file, fileHits := hitCounter(t)
	envSrv, envHits := hitCounter(t)
	flagSrv, flagHits := hitCounter(t)
	dir := writeConfig(t, "server: "+file.URL+"\n")
	code, errOut := runCfg(t, dir, map[string]string{"QUIZZOTIC_SERVER": envSrv.URL}, "--server", flagSrv.URL)
	if code != 0 || *flagHits != 1 || *envHits != 0 || *fileHits != 0 {
		t.Fatalf("flag: code=%d flag=%d env=%d file=%d err=%s", code, *flagHits, *envHits, *fileHits, errOut)
	}
}

func TestCLIConfigEnvPath(t *testing.T) {
	srv, hits := hitCounter(t)
	p := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(p, []byte("server: "+srv.URL+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, errOut := runCfg(t, t.TempDir(), map[string]string{"QUIZZOTIC_CLI_CONFIG": p}); code != 0 || *hits != 1 {
		t.Fatalf("code=%d hits=%d err=%s", code, *hits, errOut)
	}
}

func TestMissingConfigFileIsFine(t *testing.T) {
	srv, hits := hitCounter(t)
	code, errOut := runCfg(t, t.TempDir(), map[string]string{"QUIZZOTIC_SERVER": srv.URL})
	if code != 0 || *hits != 1 {
		t.Fatalf("code=%d hits=%d err=%s", code, *hits, errOut)
	}
}

func TestMalformedConfigFile(t *testing.T) {
	for name, content := range map[string]string{
		"not yaml":    "server: [unclosed\n",
		"unknown key": "servr: https://quiz.example.com\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeConfig(t, content)
			code, errOut := runCfg(t, dir, nil)
			if code == 0 || !strings.Contains(errOut, "config.yaml") || !strings.Contains(errOut, "not valid") {
				t.Fatalf("code=%d err=%q", code, errOut)
			}
		})
	}
}

func TestEmptyConfigFileFallsBackToDefault(t *testing.T) {
	dir := writeConfig(t, "")
	// Default is localhost:8080; nothing listens there in tests, so expect a
	// connection error mentioning it, not a config error.
	code, errOut := runCfg(t, dir, nil)
	if code == 0 || strings.Contains(errOut, "config file") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}
