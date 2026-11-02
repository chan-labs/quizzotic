// SPDX-License-Identifier: Apache-2.0

package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/cli"
)

func runReview(t *testing.T, dir string) (int, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Review(context.Background(), nil, cli.Env{
		Stdin: strings.NewReader(""), Stdout: &o, Stderr: &e,
		Getenv: func(k string) string {
			switch k {
			case "XDG_CONFIG_HOME":
				return dir
			case "QUIZZOTIC_TOKEN":
				return "tok"
			}
			return ""
		},
	})
	return code, e.String()
}

func TestReviewUsesConfigFileServer(t *testing.T) {
	srv, hits := hitCounter(t)
	dir := writeConfig(t, "server: "+srv.URL+"\n", 0o644)
	// The fake answers /me without a human kind, so review stops there; the
	// hit proves the config-file server was used.
	code, errOut := runReview(t, dir)
	if code != 1 || *hits != 1 || !strings.Contains(errOut, "human login only") {
		t.Fatalf("code=%d hits=%d err=%q", code, *hits, errOut)
	}
}

func TestReviewMalformedConfigFile(t *testing.T) {
	dir := writeConfig(t, "server: [bad\n", 0o644)
	code, errOut := runReview(t, dir)
	if code != 1 || !strings.Contains(errOut, "config.yaml") || !strings.Contains(errOut, "not valid") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}
