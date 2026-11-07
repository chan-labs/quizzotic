# Contributing to Quizzotic

Thanks for helping. Please read the [Code of Conduct](CODE_OF_CONDUCT.md). The design lives in [`docs/spec.md`](docs/spec.md); when a change conflicts with the spec, the spec wins, so open an issue to discuss changing it first.

## Ground rules

1. **Spec-first API.** Change `api/openapi.yaml` before writing handlers. Every operation needs a stable `operationId` and `x-quizzotic-scopes`. The contract test (every route matches the spec and vice versa) must pass.
2. **Layering.** Feature packages (`internal/<feature>`) go `handler.go` -> `service.go` -> `repo.go`. Handlers do HTTP only; services hold rules and transactions; repos are interfaces implemented in `internal/store/<backend>`. Features call each other only through services. `internal/fsrs` and `internal/grading` are pure leaf packages with no I/O.
3. **Dependencies.** Only those in [`docs/dependencies.md`](docs/dependencies.md). Anything else needs maintainer approval and an entry there.
4. **Data rules.** Attempts are append-only. Every repo method takes the owner user ID. Register new endpoints with the owner-scoping test helper.
5. **Tests.** New repo behavior gets conformance cases in `internal/store/storetest`. Run `mise run test ::: lint` before opening a PR; if you touch repo/store code, `migrations/` or `internal/store/**`, also run `mise run test-postgres`.
6. **SPDX headers.** New Go files start with `// SPDX-License-Identifier: Apache-2.0`.
7. **One change, one PR**, with a clear description. Add notable changes to [CHANGELOG.md](CHANGELOG.md) under "Unreleased".

## Development

```sh
mise install          # once: Go, Node and staticcheck at the pinned versions
mise run build        # build the quizzotic binary
mise run test         # go test -race ./...
mise run lint         # go vet + staticcheck + Redocly lint of api/openapi.yaml
mise run golden       # regenerate CLI help and MCP tool-list goldens
mise run test-postgres  # Postgres store suite in a throwaway postgres:17 container (needs docker)
mise run build-all    # cross-compile for linux, darwin, windows (CGO_ENABLED=0)
mise run ci           # everything CI runs: lint, test, test-postgres, build-all, release-check
mise run test ::: lint  # several tasks in one go
```

## Run it locally

```sh
mise run dev          # fake OIDC issuer + server + seed data; Ctrl-C stops both
```

Prerequisite: Docker. Then open <http://localhost:8089/>. The fake issuer is
[navikt/mock-oauth2-server](https://github.com/navikt/mock-oauth2-server)
(image pinned in `dev/dev.sh`, config in `dev/mock-oauth2.json`, issuer URL
`http://localhost:8099/dev`). It signs in a fixed test user (`dev@example.com`)
with no password, so the browser lands straight on the Today page. The database is `.dev/quizzotic-<issuer port>.db`
(gitignored; one per issuer port, so changing the port starts a fresh, seeded one), seeded with the starter pack plus `dev/seed.yaml`. Ports come
from `QUIZZOTIC_DEV_PORT` (8089) and `QUIZZOTIC_DEV_ISSUER_PORT` (8099).
`mise run dev-issuer` and `mise run dev-server` run the halves separately, and
`mise run dev-reset` deletes the database. `.claude/launch.json` lets an agent
start the stack with `preview_start`. The issuer and the `dev-secret` client
secret are fake and for local development only; nothing here is shipped in the
`quizzotic` binary.

## Golden files

The generated CLI help (`internal/cli/testdata`, `cmd/quizzotic/testdata`) and the MCP tool list (`internal/mcp/testdata`) are checked against golden files. Changing an operation summary, description or schema in `api/openapi.yaml` fails `mise run test` until you run `mise run golden` and commit the reviewed diff.

## Owner-scoping tests

Every user's data is private to them. The helper in `internal/testsupport/ownerscope` (test code only; production code must never import it) drives the real router, auth and SQLite behind a fake IdP as two users, A and B, and checks that B cannot reach A's data. Register each new endpoint with it in the same PR that adds the endpoint.

Register from an `init` function in a `*_test.go` file in `internal/server` (see `ownerscope_test.go`), on the shared `ownerScope` registry:

- **Resource by ID or collection** (`GET /problems/{id}`, `PATCH /problems/{id}`, `DELETE ...`, `GET /problems`): call `ownerScope.Resource`. Give it a `Create` that makes the resource as user A and returns its path parameters plus `Secrets`, strings from A's data (a prompt, a title) that must never reach B. Each `Op` is a spec pattern (`"PATCH /problems/{id}"`), with an optional request body. For every op the helper asserts:
  - unauthenticated gets 401;
  - user B gets 404 (not 403, not a success) as a problem response that contains none of the secrets;
  - user A gets the expected success status, so the check cannot pass vacuously.
  Mark a collection with `List: true`: B gets a success that must not contain A's secrets. Set `Intact` to confirm after B's attempt that an update or delete changed nothing.
- **Self endpoint** (no ID, answers for the caller, like `GET /me`): call `ownerScope.Self` with a `UserID` function that reads the user ID from the body. The helper asserts each caller sees only their own identity and that neither body contains the other user's ID, email or name.
- **Exempt**: an authenticated endpoint that can reach no user-owned data (for example the logout cookie) goes in `ownerScope.Exempt(pattern, reason)`. A reason is required and is reviewed like code.

`TestOwnerScopeCoverage` fails the build if any non-public operation in `api/openapi.yaml` is neither registered nor exempted, so a new endpoint cannot ship untested. Public operations (no security scheme, no scopes) need nothing.

## Avoiding merge conflicts between features

Parallel features should touch only their own files.

- **Server wiring.** Add `internal/server/wire_<feature>.go` with an `init` that calls `addFeature(feature{Name, After, Build, Routes, Root, Site})` (see `wire.go`). Do not edit `Handler` or the router; they run whatever is registered, in `After` order. Name the features whose services yours needs in `After`; a service others will need becomes a field of `wiring`.
- **Migrations.** Name new migrations `YYYYMMDDHHMMSS_name.sql` (UTC timestamp, 14 digits) in both `migrations/sqlite` and `migrations/postgres`, for example `20261102120000_attempt_notes.sql`. Never pick the "next number": the existing `0001` to `0014` files keep their numbers and sort first, and anything else (other digit counts) is rejected by `migrate.Load`. Never rename an applied migration.
- **API spec.** `api/openapi.yaml` stays one file (Redocly lint, the contract test and the CLI/MCP generators read it whole). Add new paths and schemas in a block next to the feature's neighbours rather than appending at the end of the file, so two features do not both extend the same last lines.

## Tracing service methods
Every exported method of a feature's `Service` that takes a `context.Context` opens with a span, so each request's trace shows HTTP, then service, then repo:

```go
func (s *Service) Create(ctx context.Context, ...) (_ View, err error) {
	ctx, end := platform.TraceMethod(ctx, "problems.Create")
	defer end(&err)
```

The span is named `<package>.<Method>`, carries no attributes (no user data or IDs), and uses the returned `ctx` for everything it calls. Return the error through the named `err` result. Client errors (a `*platform.Problem` below 500) do not fail the span; anything else does. `TestEveryServiceMethodIsTraced` fails when a method lacks the span.

## Adding a store backend

The store is the only place that knows about a database. Features depend on `Repo` interfaces; `internal/store` composes them into `Store`.

1. Create `internal/store/<backend>` implementing every feature's `Repo` interface and the `Store` interface (transactions, migrations, close).
2. Put SQL migrations in `migrations/<backend>` (embedded), named with a timestamp as above.
3. Keep owner scoping: every method takes the owner user ID and must never return another user's rows.
4. Wire the backend into config validation and store construction (`internal/config`, `cmd/quizzotic`), selected by the `storage` section of `quizzotic.yaml`.
5. Run the shared conformance suite from `internal/store/storetest` against your backend in a `_test.go` file, as the SQLite and Postgres backends do. It must pass with `-race`. Add CI coverage for the new backend.
6. If you add repo behavior, add the matching conformance cases to `storetest` so every backend is held to it.
7. Document it in the README and record the decision in `docs/adr/`.

## Adding an IdP

Quizzotic works with any standards-compliant OIDC issuer; nothing is IdP-specific in code. Pocket ID is only the documented example. Supporting a new IdP is normally configuration, not code:

1. Set the issuer under `auth.issuers` in `quizzotic.yaml` (v1 allows exactly one) and declare each client ID under `auth.issuers[0].clients` as `human` or `agent`, with `allowed_scopes` (and `acts_for` for client-credentials clients).
2. Verify discovery (`/.well-known/openid-configuration`), JWKS signing algorithms (RS256, ES256 or EdDSA), and that access tokens are JWTs carrying the claims Quizzotic checks (`iss`, `aud`, `exp`, `nbf`, client ID, `scope`).
3. If the IdP needs quirks handled (for example a non-standard claim for the client ID), add a narrowly scoped, tested option to `internal/auth` driven by config. Do not hard-code provider names.
4. Add a deployment example under `deploy/` and a short note in the docs.

## Reporting security issues

Do not file public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).
