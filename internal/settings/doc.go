// SPDX-License-Identifier: Apache-2.0

// Package settings holds each user's scheduling settings: time zone, day
// rollover hour, desired retention and the daily limits. A user's stored
// values are overrides; anything unset falls back to the configured default.
//
// The package also computes the current "study day" window, which the review
// session builder uses to count what was done "today".
package settings
