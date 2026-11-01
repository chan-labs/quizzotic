// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"

	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/store"
)

// instrumentStore counts persistence failures of WithTx. An error that fn
// itself returned is a business outcome and is not counted; any other error
// (begin, commit, connection) is a store error.
func instrumentStore(s store.Store, m *platform.Metrics) store.Store {
	return &meteredStore{Store: s, m: m}
}

type meteredStore struct {
	store.Store
	m *platform.Metrics
}

func (s *meteredStore) WithTx(ctx context.Context, fn func(tx store.Store) error) error {
	var fnErr error
	err := s.Store.WithTx(ctx, func(tx store.Store) error {
		fnErr = fn(tx)
		return fnErr
	})
	if err != nil && !(fnErr != nil && errors.Is(err, fnErr)) && ctx.Err() == nil {
		s.m.RecordStoreError()
	}
	return err
}
