// SPDX-License-Identifier: Apache-2.0

// Package api embeds the OpenAPI document. It exists because go:embed cannot
// reach outside its own directory; use internal/openapi to consume it.
package api

import _ "embed"

// Spec is the raw api/openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
