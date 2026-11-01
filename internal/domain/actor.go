// SPDX-License-Identifier: Apache-2.0

package domain

import "fmt"

// ActorKind says what sort of principal performed a write.
type ActorKind string

const (
	ActorHuman  ActorKind = "human"
	ActorAgent  ActorKind = "agent"
	ActorSystem ActorKind = "system"
)

// Valid reports whether k is one of the defined kinds.
func (k ActorKind) Valid() bool {
	switch k {
	case ActorHuman, ActorAgent, ActorSystem:
		return true
	}
	return false
}

// Actor records who performed a write: {kind, user_id, client_id}.
type Actor struct {
	Kind     ActorKind `json:"kind"`
	UserID   ID        `json:"user_id"`
	ClientID string    `json:"client_id"`
}

// Validate checks the actor is well formed. Human and agent actors must carry
// a user; system actors need not.
func (a Actor) Validate() error {
	if !a.Kind.Valid() {
		return fmt.Errorf("domain: invalid actor kind %q", a.Kind)
	}
	if a.Kind != ActorSystem && a.UserID.IsNil() {
		return fmt.Errorf("domain: %s actor requires a user id", a.Kind)
	}
	return nil
}
