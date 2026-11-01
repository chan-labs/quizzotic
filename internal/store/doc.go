// SPDX-License-Identifier: Apache-2.0

// Package store defines Store, which composes every feature's Repo
// interface. Backends live in the sqlite and postgres subpackages, and the
// storetest subpackage holds the shared conformance suite.
//
// Layer: persistence boundary.
package store
