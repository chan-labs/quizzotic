// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The website's static assets, served by server.Run, carry a strong ETag and
// Cache-Control, revalidate with 304, and come gzipped when the client asks.
func TestRunServesStaticAssetsWithValidators(t *testing.T) {
	e := newRunEnv(t)
	// No transparent decompression: the test sets Accept-Encoding itself.
	c := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	get := func(path string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, body
	}

	resp, plain := get("/static/app.css", nil)
	if resp.StatusCode != http.StatusOK || len(plain) == 0 {
		t.Fatalf("GET /static/app.css = %d, %d bytes", resp.StatusCode, len(plain))
	}
	etag := resp.Header.Get("ETag")
	if !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") {
		t.Fatalf("ETag = %q, want a strong validator", etag)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("identity request got Content-Encoding %q", resp.Header.Get("Content-Encoding"))
	}

	resp, body := get("/static/app.css", map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("revalidation = %d with %d bytes, want 304 and no body", resp.StatusCode, len(body))
	}
	if resp.Header.Get("ETag") != etag {
		t.Fatalf("304 ETag = %q, want %q", resp.Header.Get("ETag"), etag)
	}

	resp, gz := get("/static/app.css", map[string]string{"Accept-Encoding": "gzip"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip request = %d, Content-Encoding %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
	}
	if !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q", resp.Header.Get("Vary"))
	}
	gzTag := resp.Header.Get("ETag")
	if gzTag == "" || gzTag == etag {
		t.Fatalf("gzip ETag = %q, want one distinct from the identity %q", gzTag, etag)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plain) {
		t.Fatal("gzip body does not decode to the original")
	}
	if resp, body := get("/static/app.css", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gzTag}); resp.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("gzip revalidation = %d with %d bytes", resp.StatusCode, len(body))
	}
	if resp, _ := get("/static/app.css", map[string]string{"Accept-Encoding": "gzip;q=0"}); resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("gzip;q=0 still got gzip")
	}

	if resp, _ := get("/static/nope.css", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown asset = %d, want 404", resp.StatusCode)
	}
}
