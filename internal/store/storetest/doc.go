// SPDX-License-Identifier: Apache-2.0

// Package storetest is the backend-agnostic conformance suite that every
// store implementation must pass.
//
// Layer: persistence backend (test support).
//
// # Running the suite
//
// A backend calls Run from an ordinary test in its own package:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) (store.Store, storetest.Scratch) {
//			// open a fresh, migrated, empty store; register cleanup with t
//		})
//	}
//
// newStore is called once per case, so every case starts from an empty store.
//
// Both backends call Run: SQLite in internal/store/sqlite and Postgres in
// internal/store/postgres (CI runs the latter against a service container,
// locally set QUIZZOTIC_TEST_POSTGRES_URL). A case registered in Run therefore
// runs on both, and a new repo that only one backend implements fails to
// compile there, because each backend asserts store.Store. A new migration
// needs a twin in both migrations/ directories (TestMigrationParity).
//
// Stored JSON is compared by value (sameJSON), not bytes: Postgres jsonb
// normalizes whitespace and key order.
//
// # Adding cases for a new repo
//
// When a feature task adds a repo interface to store.Store:
//
//  1. Create a file in this package named after the repo (for example
//     problems.go) with a function runProblems(t *testing.T, newStore NewStore).
//  2. Cover the repo's behavior through the store.Store interface only: no
//     backend-specific types or SQL. Include owner-scoping cases (a second
//     user must never see the first user's rows).
//  3. Register it by adding one line to Run in storetest.go.
//
// A case that fails on one backend but not another is a backend bug, not a
// reason to special-case the suite.
//
// Scratch is a minimal, suite-owned table that lets the transaction cases
// observe commit and rollback before any real repo exists. Backends implement
// it in their test files only.
package storetest
