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
// email are copied from the claims as soon as they change. last_seen_at is
// updated at most once per touchInterval, so a burst of bearer requests does
// not write on every request. Concurrent first logins for one (issuer,
// subject) yield exactly one user.
func (s *Service) Provision(ctx context.Context, c ProvisionClaims) (_ User, err error) {
	ctx, end := platform.TraceMethod(ctx, "auth.Provision")
	defer end(&err)
	if c.Issuer == "" || c.Subject == "" {
		return User{}, errors.New("auth: claims need an issuer and a subject")
	}
	// Most bearer requests change nothing: the identity exists, last_seen_at
	// is fresh and the profile matches. Check that in a read-only transaction
	// so those requests never take the write lock.
	var cur User
	var settled bool
	if err = s.tx.InTx(platform.ReadOnlyTx(ctx), func(r Repos) error {
		var err error
		cur, settled, err = s.provisionUnchanged(ctx, r, c)
		return err
	}); err != nil {
		return User{}, err
	}
	if settled {
		return cur, nil
	}
	for range maxProvisionAttempts {
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
	ctx = platform.ReadOnlyTx(ctx)
	var u User
	err = s.tx.InTx(ctx, func(r Repos) error {
		var err error
		u, err = r.Users.Get(ctx, userID)
		return err
	})
	return u, err
}

// provisionUnchanged reports the user and true when the identity exists and
// provision would write nothing: last_seen_at is fresh and the claims match the
// stored profile.
func (s *Service) provisionUnchanged(ctx context.Context, r Repos, c ProvisionClaims) (User, bool, error) {
	ident, err := r.Identities.GetByIssuerSubject(ctx, c.Issuer, c.Subject)
	if errors.Is(err, ErrNotFound) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	u, err := r.Users.Get(ctx, ident.UserID)
	if err != nil {
		return User{}, false, err
	}
	if s.clock.Now().Sub(ident.LastSeenAt) >= touchInterval {
		return User{}, false, nil
	}
	if (c.DisplayName != "" && c.DisplayName != u.DisplayName) || (c.Email != "" && c.Email != u.Email) {
		return User{}, false, nil
	}
	return u, true, nil
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
	// Like web sessions, last_seen_at is coarse: skip the write while it is
	// fresh. A clock that went backwards also counts as fresh.
	if now.Sub(ident.LastSeenAt) >= touchInterval {
		if err := r.Identities.TouchLastSeen(ctx, u.ID, ident.ID, now); err != nil {
			return User{}, err
		}
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
