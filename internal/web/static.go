// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// staticMaxAge is how long browsers may reuse a static asset before
// revalidating it with If-None-Match. Asset URLs are not fingerprinted, so it
// stays short enough for a deploy to reach every browser within the hour.
const staticMaxAge = time.Hour

// staticAsset is one embedded file, hashed and (when it pays) gzipped once at
// startup.
type staticAsset struct {
	name   string
	body   []byte
	etag   string
	gz     []byte // nil when gzip would not make it smaller
	gzETag string
}

// staticHandler serves the files of fsys by path, with a strong ETag
// (If-None-Match answers 304), Cache-Control, and a precompressed gzip body
// for clients that accept it. Embedded files carry no modification time, so
// http.FileServer alone sends no validator at all.
type staticHandler map[string]*staticAsset

func newStaticHandler(fsys fs.FS) (staticHandler, error) {
	h := staticHandler{}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		tag := hex.EncodeToString(sum[:16])
		a := &staticAsset{name: name, body: body, etag: `"` + tag + `"`}
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return err
		}
		if _, err := zw.Write(body); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		if buf.Len() < len(body) {
			a.gz, a.gzETag = buf.Bytes(), `"`+tag+`-gzip"`
		}
		h[name] = a
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("web: static assets: %w", err)
	}
	return h, nil
}

// ServeHTTP serves r.URL.Path (already stripped of /static/).
func (h staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, ok := h[strings.TrimPrefix(r.URL.Path, "/")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	hdr := w.Header()
	hdr.Set("Cache-Control", "public, max-age="+strconv.Itoa(int(staticMaxAge.Seconds())))
	body, etag := a.body, a.etag
	if a.gz != nil {
		hdr.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			body, etag = a.gz, a.gzETag
			hdr.Set("Content-Encoding", "gzip")
		}
	}
	hdr.Set("ETag", etag)
	// ServeContent answers If-None-Match with 304 and sets the Content-Type
	// from the name's extension (the identity body's, also for gzip).
	http.ServeContent(w, r, a.name, time.Time{}, bytes.NewReader(body))
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip (or *)
// with a non-zero quality.
func acceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding != "gzip" && coding != "*" {
			continue
		}
		if q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q="); ok {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v == 0 {
				continue
			}
		}
		return true
	}
	return false
}
