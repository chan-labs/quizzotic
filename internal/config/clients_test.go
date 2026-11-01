// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

func TestAgentClientForbiddenScopes(t *testing.T) {
	for _, sc := range []string{"attempts:write", "sessions:write", "admin"} {
		y := strings.Replace(validYAML, `allowed_scopes: ["problems:read"]`, `allowed_scopes: ["problems:read", "`+sc+`"]`, 1)
		_, err := Parse([]byte(y), []string{})
		if err == nil || !strings.Contains(err.Error(), sc) {
			t.Errorf("scope %s on agent: err = %v, want refusal", sc, err)
		}
	}
}

func TestHumanClientMayHoldAnyScope(t *testing.T) {
	y := strings.Replace(validYAML, `kind: "human"
          secret: ""`, `kind: "human"
          allowed_scopes: ["admin"]`, 1)
	if _, err := Parse([]byte(y), []string{}); err != nil {
		t.Errorf("human with admin: %v", err)
	}
}
