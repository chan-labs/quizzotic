// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/server"
	"github.com/chan-labs/quizzotic/internal/testsupport/serverrun"
)

// runLimitsServer starts server.Run from the example config with a short read
// timeout and returns the listen address.
func runLimitsServer(t *testing.T, readTimeout time.Duration) string {
	t.Helper()
	data, err := os.ReadFile("../../quizzotic.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(data, []string{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.URL = "sqlite://" + filepath.Join(t.TempDir(), "q.db")
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":%q}`, idp.URL, idp.URL+"/jwks")
		case "/jwks":
			fmt.Fprint(w, `{"keys":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	cfg.Auth.Issuers[0].Issuer = idp.URL

	srv := serverrun.StartWith(t, cfg, server.Options{ReadTimeout: readTimeout})
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestServerRejectsOversizedHeaders(t *testing.T) {
	addr := runLimitsServer(t, 0)
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Well over MaxHeaderBytes (plus net/http's 4 KiB slack), well under the
	// 1 MiB default it replaces.
	req.Header.Set("X-Big", strings.Repeat("a", server.MaxHeaderBytes+16<<10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("oversized header: status %d, want 431", resp.StatusCode)
	}

	req.Header.Set("X-Big", strings.Repeat("a", 1<<10))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("normal header: status %d, want 200", resp.StatusCode)
	}
}

func TestServerCutsOffSlowBody(t *testing.T) {
	const readTimeout = 300 * time.Millisecond
	addr := runLimitsServer(t, readTimeout)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Promise a body and trickle only part of it. Without a read timeout the
	// server would wait for the rest forever.
	start := time.Now()
	fmt.Fprintf(conn, "POST /api/v1/problems HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{", addr)
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	for {
		if _, err := br.ReadByte(); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatal("server held the slow-body connection open past 10s")
			}
			break // EOF or reset: the server closed the connection
		}
		// Keep trickling while the server is still reading.
		_, _ = conn.Write([]byte(" "))
	}
	if el := time.Since(start); el < readTimeout {
		t.Fatalf("connection closed after %v, before the %v read timeout", el, readTimeout)
	}
}
