// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
)

// md renders untrusted Markdown. Raw HTML is disabled (goldmark's default:
// html.WithUnsafe is deliberately not set), so inline and block HTML is
// replaced by a comment, and javascript:/data: link targets are dropped.
var md = goldmark.New()

// RenderMarkdown converts untrusted Markdown to HTML that is safe to embed.
func RenderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}
