// SPDX-License-Identifier: Apache-2.0

// Package migrations embeds the numbered SQL migrations, one directory per
// dialect. Files are named NNNN_description.sql.
package migrations

import "embed"

// FS holds the sqlite and postgres directories. The all: prefix keeps the
// .gitkeep placeholders so a dialect with no migrations yet still embeds.
//
//go:embed all:sqlite all:postgres
var FS embed.FS
