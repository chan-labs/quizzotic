// SPDX-License-Identifier: Apache-2.0

package platform

import "context"

type readOnlyKey struct{}

// ReadOnlyTx marks ctx so that the store transaction a service opens with it
// is read-only: no write lock on SQLite, BEGIN READ ONLY on Postgres. Services
// that only read call it before InTx. A write attempted inside fails.
func ReadOnlyTx(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyKey{}, true)
}

// IsReadOnlyTx reports whether ctx was marked by ReadOnlyTx.
func IsReadOnlyTx(ctx context.Context) bool {
	v, _ := ctx.Value(readOnlyKey{}).(bool)
	return v
}
