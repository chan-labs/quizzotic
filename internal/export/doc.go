// SPDX-License-Identifier: Apache-2.0

// Package export dumps all of one user's data as a versioned JSON document and
// restores it into an empty instance (`GET /api/v1/export`, `quizzotic admin
// export`, `quizzotic admin import`).
//
// The document mirrors the source-of-truth tables row for row, so nothing a
// feature stores can be forgotten by a hand-written mapping: schema.go lists
// every exported table and column, and the backends read and write rows by
// that list. Cards and mastery are projections and are rebuilt on import.
package export
