// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/platform"
)

// Repos is the set of repositories a Transactor binds to one transaction.
type Repos struct {
	Users      UserRepo
	Identities IdentityRepo
}

// Transactor runs fn atomically over transaction-bound repositories. The store
// package adapts a store.Store to it.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repos) error) error
}

// ProvisionClaims are the verified token claims provisioning needs.
type ProvisionClaims struct {
	Issuer      string
	Subject     string
	DisplayName string
	Email       string
}

// Service holds the auth rules that need storage.
type Service struct {
	tx    Transactor
	clock domain.Clock
	ids   *domain.IDGenerator
}

// NewService builds a Service.
func NewService(tx Transactor, clock domain.Clock, ids *domain.IDGenerator) *Service {
	return &Service{tx: tx, clock: clock, ids: ids}
}

// errLostRace aborts a transaction whose insert lost to a concurrent first
// login for the same (issuer, subject); the whole transaction is retried.
var errLostRace = errors.New("auth: lost provisioning race")

const maxProvisionAttempts = 5

// Provision resolves a verified token to its user, creating the identity and
// user on the first valid login (just-in-time provisioning). Display name and
// email are copied from the claims, and last_seen_at is updated. Concurrent
// first logins for one (issuer, subject) yield exactly one user.
func (s *Service) Provision(ctx context.Context, c ProvisionClaims) (_ User, err error) {
	ctx, end := platform.TraceMethod(ctx, "auth.Provision")
	defer end(&err)
	if c.Issuer == "" || c.Subject == "" {
		return User{}, errors.New("auth: claims need an issuer and a subject")
	}
	for i := 0; i < maxProvisionAttempts; i++ {
		var u User
		err = s.tx.InTx(ctx, func(r Repos) error {
			var err error
			u, err = s.provision(ctx, r, c)
			return err
		})
		if errors.Is(err, errLostRace) {
			continue
		}
		return u, err
	}
	return User{}, fmt.Errorf("auth: provisioning %q did not settle after %d attempts", c.Issuer, maxProvisionAttempts)
}

// GetUser returns the user with the given ID, or ErrNotFound.
func (s *Service) GetUser(ctx context.Context, userID domain.ID) (_ User, err error) {
	ctx, end := platform.TraceMethod(ctx, "auth.GetUser")
	defer end(&err)
	var u User
	err = s.tx.InTx(ctx, func(r Repos) error {
		var err error
		u, err = r.Users.Get(ctx, userID)
		return err
	})
	return u, err
}

func (s *Service) provision(ctx context.Context, r Repos, c ProvisionClaims) (User, error) {
	now := s.clock.Now()

	ident, err := r.Identities.GetByIssuerSubject(ctx, c.Issuer, c.Subject)
	if errors.Is(err, ErrNotFound) {
		u := User{ID: s.ids.New(), DisplayName: c.DisplayName, Email: c.Email, CreatedAt: now}
		if err := r.Users.Create(ctx, u); err != nil {
			return User{}, err
		}
		ins := Identity{
			ID: s.ids.New(), UserID: u.ID, Issuer: c.Issuer, Subject: c.Subject,
			CreatedAt: now, LastSeenAt: now,
		}
		ok, err := r.Identities.InsertIfAbsent(ctx, ins)
		if err != nil {
			return User{}, err
		}
		if !ok {
			return User{}, errLostRace // roll back the orphan user and retry
		}
		return u, nil
	}
	if err != nil {
		return User{}, err
	}

	u, err := r.Users.Get(ctx, ident.UserID)
	if err != nil {
		return User{}, err
	}
	if err := r.Identities.TouchLastSeen(ctx, u.ID, ident.ID, now); err != nil {
		return User{}, err
	}
	name, email := u.DisplayName, u.Email
	if c.DisplayName != "" {
		name = c.DisplayName
	}
	if c.Email != "" {
		email = c.Email
	}
	if name != u.DisplayName || email != u.Email {
		if err := r.Users.UpdateProfile(ctx, u.ID, name, email); err != nil {
			return User{}, err
		}
		u.DisplayName, u.Email = name, email
	}
	return u, nil
}
