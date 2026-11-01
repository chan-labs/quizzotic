# Contributing to Quizzotic

Thanks for helping. Please read the [Code of Conduct](CODE_OF_CONDUCT.md). The design lives in [`docs/spec.md`](docs/spec.md); when a change conflicts with the spec, the spec wins, so open an issue to discuss changing it first.

## Ground rules

1. **Spec-first API.** Change `api/openapi.yaml` before writing handlers. Every operation needs a stable `operationId` and `x-quizzotic-scopes`. The contract test (every route matches the spec and vice versa) must pass.
2. **Layering.** Feature packages (`internal/<feature>`) go `handler.go` -> `service.go` -> `repo.go`. Handlers do HTTP only; services hold rules and transactions; repos are interfaces implemented in `internal/store/<backend>`. Features call each other only through services. `internal/fsrs` and `internal/grading` are pure leaf packages with no I/O.
3. **Dependencies.** Only those in [`docs/dependencies.md`](docs/dependencies.md). Anything else needs maintainer approval and an entry there.
4. **Data rules.** Attempts are append-only. Every repo method takes the owner user ID. Register new endpoints with the owner-scoping test helper.
5. **Tests.** New repo behavior gets conformance cases in `internal/store/storetest`. Run `make test lint` before opening a PR.
6. **SPDX headers.** New Go files start with `// SPDX-License-Identifier: Apache-2.0`.
7. **One change, one PR**, with a clear description. Add notable changes to [CHANGELOG.md](CHANGELOG.md) under "Unreleased".

## Development

```sh
make build   # build the quizzotic binary
make test    # go test -race ./...
make lint    # go vet + staticcheck
```

## Adding a store backend

The store is the only place that knows about a database. Features depend on `Repo` interfaces; `internal/store` composes them into `Store`.

1. Create `internal/store/<backend>` implementing every feature's `Repo` interface and the `Store` interface (transactions, migrations, close).
2. Put SQL migrations in `migrations/<backend>` (embedded).
3. Keep owner scoping: every method takes the owner user ID and must never return another user's rows.
4. Wire the backend into config validation and store construction (`internal/config`, `cmd/quizzotic`), selected by the `storage` section of `quizzotic.yaml`.
5. Run the shared conformance suite from `internal/store/storetest` against your backend in a `_test.go` file, as the SQLite and Postgres backends do. It must pass with `-race`. Add CI coverage for the new backend.
6. If you add repo behavior, add the matching conformance cases to `storetest` so every backend is held to it.
7. Document it in the README and record the decision in `docs/adr/`.

## Adding an IdP

Quizzotic works with any standards-compliant OIDC issuer; nothing is IdP-specific in code. Pocket ID is only the documented example. Supporting a new IdP is normally configuration, not code:

1. Set the issuer under `auth.issuers` in `quizzotic.yaml` (v1 allows exactly one) and declare each client ID under `auth.clients` as `human` or `agent`, with `allowed_scopes` (and `acts_for` for client-credentials clients).
2. Verify discovery (`/.well-known/openid-configuration`), JWKS signing algorithms (RS256, ES256 or EdDSA), and that access tokens are JWTs carrying the claims Quizzotic checks (`iss`, `aud`, `exp`, `nbf`, client ID, `scope`).
3. If the IdP needs quirks handled (for example a non-standard claim for the client ID), add a narrowly scoped, tested option to `internal/auth` driven by config. Do not hard-code provider names.
4. Add a deployment example under `deploy/` and a short note in the docs.

## Reporting security issues

Do not file public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).
