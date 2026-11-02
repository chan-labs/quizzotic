// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckIssuerURLRequiresHTTPSExceptLoopback(t *testing.T) {
	ok := []string{
		"https://idp.example.com", "http://localhost:8080", "http://127.0.0.1:9",
		"http://127.1.2.3", "http://[::1]:80", "https://127.0.0.1",
	}
	bad := []string{
		"http://idp.example.com", "http://10.0.0.1", "http://localhost.example.com",
		"ftp://localhost", "idp.example.com", "",
	}
	for _, u := range ok {
		if err := checkIssuerURL(u); err != nil {
			t.Errorf("%q should be allowed: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := checkIssuerURL(u); err == nil {
			t.Errorf("%q should be rejected", u)
		}
	}
}

func TestResolveIssuerRejectsHTTPAuthorizationServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"authorization_servers":["http://idp.example.com"]}`))
	}))
	defer srv.Close()
	env := Env{HTTP: srv.Client(), Getenv: func(string) string { return "" }}
	_, err := resolveIssuer(context.Background(), env, "", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("want https error, got %v", err)
	}
}

func TestResolveIssuerRejectsHTTPNonLoopbackServer(t *testing.T) {
	env := Env{HTTP: http.DefaultClient, Getenv: func(string) string { return "" }}
	if _, err := resolveIssuer(context.Background(), env, "", "http://quiz.example.com"); err == nil {
		t.Fatal("http non-loopback server must be rejected")
	}
}

func TestResolveIssuerAllowsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"authorization_servers":["http://127.0.0.1:5556/"]}`))
	}))
	defer srv.Close()
	env := Env{HTTP: srv.Client(), Getenv: func(string) string { return "" }}
	iss, err := resolveIssuer(context.Background(), env, "", srv.URL)
	if err != nil || iss != "http://127.0.0.1:5556" {
		t.Fatalf("got %q, %v", iss, err)
	}
}

func TestDiscoverRejectsHTTPEndpoints(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(body, "ISS", "http://"+r.Host)))
	}))
	defer srv.Close()
	body = `{"issuer":"ISS","token_endpoint":"http://idp.example.com/token"}`
	if _, err := discover(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("http token_endpoint on non-loopback host must be rejected")
	}
	body = `{"issuer":"ISS","token_endpoint":"ISS/token","device_authorization_endpoint":"http://idp.example.com/d"}`
	if _, err := discover(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("http device endpoint on non-loopback host must be rejected")
	}
	body = `{"issuer":"ISS","token_endpoint":"ISS/token"}`
	if _, err := discover(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatalf("loopback http should pass: %v", err)
	}
}
