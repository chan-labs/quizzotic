// SPDX-License-Identifier: Apache-2.0

package openapi

import (
	"net/http"

	"github.com/chan-labs/quizzotic/api"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Spec returns the embedded OpenAPI document (api/openapi.yaml) as YAML.
func Spec() []byte { return api.Spec }

// Register mounts GET /openapi.yaml (served at /api/v1/openapi.yaml) on rt.
func Register(rt *platform.Router) {
	rt.HandleFunc("GET /openapi.yaml", serveSpec)
}

func serveSpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(api.Spec)
}
