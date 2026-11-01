// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/chan-labs/quizzotic/internal/openapi"
	"github.com/chan-labs/quizzotic/internal/testsupport/ownerscope"
)

// ownerScope is the registry every feature adds its endpoints to. Register from
// an init function in the feature's own *_test.go file in this package (see
// CONTRIBUTING.md, "Owner-scoping tests"); TestOwnerScopeCoverage then fails
// for any authenticated spec operation that nobody registered or exempted.
var ownerScope = ownerscope.NewRegistry()

func init() {
	// /me answers for the caller and has no ID, so it registers as Self.
	ownerScope.Self(ownerscope.Self{
		Pattern: "GET /me",
		UserID: func(body []byte) string {
			var m meBody
			_ = json.Unmarshal(body, &m)
			return m.User.ID
		},
	})
	ownerScope.Exempt("POST /auth/logout",
		"acts only on the caller's own session cookie; takes no ID and reads no stored user data")
}

// ownerScopeEnv wires two users through the fake IdP: each is provisioned on
// first request, with a distinct subject, name and email.
func (e *itEnv) ownerScopeEnv() *ownerscope.Env {
	subs := map[ownerscope.User]string{ownerscope.UserA: "owner-a-sub", ownerscope.UserB: "owner-b-sub"}
	names := map[ownerscope.User]string{ownerscope.UserA: "Owner Alpha", ownerscope.UserB: "Owner Bravo"}
	emails := map[ownerscope.User]string{ownerscope.UserA: "alpha@example.com", ownerscope.UserB: "bravo@example.com"}
	return &ownerscope.Env{
		BaseURL: e.srv.URL,
		Credentials: func(_ testing.TB, u ownerscope.User) func(*http.Request) {
			tok := e.idp.access("quizzotic-cli", subs[u], with("name", names[u]), with("email", emails[u]))
			return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
		},
		Identity: func(t testing.TB, u ownerscope.User) ownerscope.Identity {
			id, usr := e.userFor(t,subs[u])
			return ownerscope.Identity{ID: id, Email: usr.Email, DisplayName: usr.DisplayName}
		},
	}
}

func TestOwnerScope(t *testing.T) {
	ownerScope.Run(t, newITEnv(t).ownerScopeEnv())
}

func TestOwnerScopeCoverage(t *testing.T) {
	ops, err := openapi.SpecOperations()
	if err != nil {
		t.Fatal(err)
	}
	ownerScope.VerifyCoverage(t, ops)
}
