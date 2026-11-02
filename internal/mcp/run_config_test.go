// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/cli"
)

func TestRunMalformedCLIConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("server: [bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var e bytes.Buffer
	code := Run(context.Background(), nil, cli.Env{
		Stderr: &e,
		Getenv: func(k string) string {
			if k == "QUIZZOTIC_CLI_CONFIG" {
				return p
			}
			return ""
		},
	}, "test")
	if code != 1 || !strings.Contains(e.String(), "config.yaml") {
		t.Fatalf("code=%d err=%q", code, e.String())
	}
}
