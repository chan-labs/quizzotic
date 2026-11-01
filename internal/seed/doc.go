// SPDX-License-Identifier: Apache-2.0

// Package seed loads tags, problems and quizzes from YAML. Every item has a
// stable key, so re-running a file updates in place instead of duplicating.
// The loader writes only through the tags, problems and quizzes services, so
// validation, versioning and tag resolution apply as they do over the API.
//
// Layer: feature (loader -> services, key repo).
package seed
