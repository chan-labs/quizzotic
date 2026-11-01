// SPDX-License-Identifier: Apache-2.0

package platform

import "sort"

// RootRoutes are the patterns served at the host root, outside APIPrefix
// (see Health.Register and Metrics.Register; /metrics is mounted only when
// observability.metrics_enabled is set). The OpenAPI contract test checks them against the
// spec operations whose path item overrides servers with "/".
var RootRoutes = []string{"GET /healthz", "GET /readyz", MetricsRoute}

// Patterns returns every pattern registered with Handle or HandleFunc, sorted,
// relative to APIPrefix. The OpenAPI contract test uses it.
func (rt *Router) Patterns() []string {
	out := append([]string(nil), rt.patterns...)
	sort.Strings(out)
	return out
}
