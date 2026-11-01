// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// WithExtraOperations registers test-only operations and their routes through
// the same router and scope guard as the spec's own. It exists only in tests.
func WithExtraOperations(ops []openapi.Operation, register func(*platform.Router)) HandlerOption {
	return func(o *handlerOptions) { o.extraOps, o.extraRegister = ops, register }
}

// AuthOptions exposes the production auth wiring to tests.
var AuthOptions = authOptions
