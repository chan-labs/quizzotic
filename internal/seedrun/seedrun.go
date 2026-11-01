// SPDX-License-Identifier: Apache-2.0

// Package seedrun applies seed files to a store: it picks the owner and wires
// the loader to the store's services. `quizzotic admin seed` and `storage.seed`
// on serve start both use it.
package seedrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/chan-labs/quizzotic/internal/auth"
	"github.com/chan-labs/quizzotic/internal/domain"
	"github.com/chan-labs/quizzotic/internal/seed"
	"github.com/chan-labs/quizzotic/internal/store"
)

// ErrNoOwner: no owner was given and the store does not have exactly one
// user. The wrapped message says which.
var ErrNoOwner = errors.New("seed: cannot choose an owner")

// ResolveOwner returns the user who will own seeded rows: the user whose ID is
// owner, or, when owner is empty, the only user.
func ResolveOwner(ctx context.Context, st store.Store, owner string) (domain.ID, error) {
	if owner != "" {
		id, err := domain.ParseID(owner)
		if err != nil {
			return domain.Nil, fmt.Errorf("seed: --owner %q is not a user ID", owner)
		}
		if _, err := st.Users().Get(ctx, id); err != nil {
			if errors.Is(err, auth.ErrNotFound) {
				return domain.Nil, fmt.Errorf("seed: no user %s", owner)
			}
			return domain.Nil, err
		}
		return id, nil
	}
	u, err := st.Users().Sole(ctx)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return domain.Nil, fmt.Errorf("%w: there are no users yet (a user is created on first sign-in)", ErrNoOwner)
	case errors.Is(err, auth.ErrAmbiguous):
		return domain.Nil, fmt.Errorf("%w: there is more than one user; pass --owner <user id>", ErrNoOwner)
	case err != nil:
		return domain.Nil, err
	}
	return u.ID, nil
}

// ApplyFile applies a loaded seed file for ownerID.
func ApplyFile(ctx context.Context, st store.Store, ownerID domain.ID, f *seed.File) (seed.Report, error) {
	clock := domain.SystemClock{}
	return seed.NewLoader(store.SeedTx(st, clock, domain.NewIDGenerator(clock))).Apply(ctx, ownerID, f)
}

// Startup applies every spec (a file path or builtin:<name>) for the sole
// user, as `storage.seed` does on serve start. Every spec is read and checked
// first, so a bad one fails before anything is written. When there is no
// single user to own the rows it applies nothing and returns the reason as
// skipped (err stays nil): that is not a failure.
func Startup(ctx context.Context, st store.Store, specs []string, logf func(spec string, rep seed.Report)) (skipped error, err error) {
	if len(specs) == 0 {
		return nil, nil
	}
	files := make([]*seed.File, len(specs))
	for i, s := range specs {
		f, err := seed.Load(s)
		if err != nil {
			return nil, err
		}
		files[i] = f
	}
	owner, err := ResolveOwner(ctx, st, "")
	if errors.Is(err, ErrNoOwner) {
		return err, nil
	}
	if err != nil {
		return nil, err
	}
	for i, f := range files {
		rep, err := ApplyFile(ctx, st, owner, f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", specs[i], err)
		}
		if logf != nil {
			logf(specs[i], rep)
		}
	}
	return nil, nil
}
