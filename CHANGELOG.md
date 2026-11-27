# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- The server now deletes expired web sessions and abandoned login states in an hourly background sweep, instead of keeping them forever. `auth.WebSessionRepo` gains `PurgeExpired`, and `auth.LoginStateRepo.PurgeExpired` now returns the number removed.
- The HTTP server now bounds whole-request reads (30s), idle keep-alive connections (120s) and request headers (64 KiB, larger gets 431). The CLI's loopback login server has the same limits.

## [0.3.1] - 2026-11-07

No changes since 0.3.0: `v0.3.1` tags the same commit as `v0.3.0`.

## [0.3.0] - 2026-11-07

### Changed

- Cloze blanks now have a named `match` mode instead of the `case_sensitive` flag: `relaxed` (default; trim, ignore case, collapse spaces, drop trailing `.,;:!?`) or `case-insensitive` (ignores case only; whitespace and punctuation must match exactly). The migration maps both old values to `relaxed`, so **case no longer matters on blanks that had `case_sensitive: true`**; stored problem versions are rewritten and past attempts and their grades are not touched. `case_sensitive` is still accepted on input (API, seed files, import, MCP, CLI, web form) for one version, means `relaxed`, and loses to `match` when both are sent. Responses carry only `match`. The web editor's checkbox became a select.

### Added

- `tools/devissuer`, a stdlib-only fake OIDC issuer for local development. `mise run dev` uses it when Docker is not available (or with `QUIZZOTIC_DEV_ISSUER=go`), so the dev stack runs with only Go installed.
- OpenTelemetry tracing now covers service methods, repo calls (SQLite and Postgres, one span per SQL statement and per transaction; SQL text and arguments are never recorded), OIDC discovery and JWKS fetches, and the CLI and MCP clients, which send `traceparent` so a client call and the server work it triggers form one trace. See `docs/tracing.md`.
- `mise run dev-otel`, a local Grafana, Tempo and collector stack for looking at traces (see CONTRIBUTING.md, "Local tracing").
- `golangci-lint` and a gofmt/goimports format check in `mise run lint` and CI.

## [0.2.0] - 2026-11-02

### Changed

- `auth.issuers[].audience` takes a string or a list, and a token passes when its `aud` contains any listed value, so an IdP that sets `aud` to each client's own ID (Pocket ID) works by listing the client IDs. Tokens with no `client_id` or `azp` claim are classified by the one declared client named in `aud`. See `docs/pocket-id.md`.
- `storage.url` accepts the `postgresql://` scheme as well as `postgres://`.
- The container image runs as the numeric user `65532:65532`, so it starts under Kubernetes `runAsNonRoot: true` without a `runAsUser` override.
- Tag operations: an applied operation records its effect, returned as `result` by `GET` and the list endpoint; the list endpoint returns previews for proposed operations, and the web approvals queue shows applied results and rejected operations. Adds a migration.

## [0.1] - 2026-11-02

First tag, from before the `v`-plus-semver convention in `docs/releasing.md`. It carries the initial feature set:

### Added

- Golden tests for the generated CLI help and MCP tool list; `mise run golden` regenerates them.
- `GET /healthz` (liveness) and `GET /readyz` (readiness: store, JWKS) endpoints.
- `quizzotic review`, a terminal loop for reviewing due problems.
- OpenTelemetry tracing, off by default and enabled with the standard `OTEL_*` variables (no Quizzotic config keys). It traces HTTP requests, with log lines carrying `trace_id` and `span_id`.
- Export and import. `GET /api/v1/export` (scope `admin`, humans only) and `quizzotic admin export [--user ID] [--out file]` stream everything a user owns as one versioned JSON document (users and scheduling overrides, identities, problems with every version, tags, aliases and mappings, tag operations with their snapshots and reverts, quizzes, review sessions, seed keys and the whole attempt log); `quizzotic admin import [--in file]` restores it into an empty instance, preserving IDs, and rebuilds cards, mastery and mastery history. The output is byte-identical across SQLite and Postgres. See `docs/export.md`.
- Metrics and Tags pages. `/stats` shows the overview (recall now, true retention against the desired retention, tags studied), the due forecast for the next 7 days and daily activity; `/tags` is the mastery table (recall, coverage, median stability, lapse rate, true retention, last reviewed; weakest first) and `/tags/{tag}` charts the tag's recall history. Charts are server-rendered SVG with titles, descriptions and a data table each, no script. They read through `metrics.QueryService`, so they show the same figures as the `/metrics/*` API endpoints. A tag's days with nothing studied are left off its chart.
- Metrics endpoints (scope `metrics:read`, also exposed to agents as MCP tools `metrics_overview`, `metrics_tags`, `metrics_tagHistory` and `metrics_problems`): `GET /api/v1/metrics/overview` (mean recall, true retention against desired retention, due load for the next 7 days, daily activity), `GET /api/v1/metrics/tags` (per-tag recall, coverage, median stability, lapse rate and true retention, paged), `GET /api/v1/metrics/tags/{tag}/history?from=&to=` (daily mastery snapshots, filed under the study-day dates the snapshot job writes) and `GET /api/v1/metrics/problems?sort=recall&limit=` (weakest studied problems). Recall is predicted at request time. `metrics.Repo` gains `TagLabels` and `StudiedCards`; no schema change.
- `quizzotic admin rebuild [--user ID]` and `POST /api/v1/admin/rebuild` (scope `admin`, humans only) recompute every card from the attempt log, in batches with progress logs; `auth.UserRepo` gains a paged `ListIDs` for it.
- `quizzotic admin rebuild --history [--user ID [--tag ID|slug]]` rewrites the mastery snapshot history by replaying the attempt log, and the stored problem lifecycle, to the start of every past study day, using the daily job's date convention; `metrics.Backfill` does the work inside a caller's transaction for tag operations.
- Postgres store (`postgres://` in `storage.url`) on pgx/v5 with `timestamptz`/`jsonb` migrations mirroring SQLite, passing the shared conformance suite; CI runs it against a Postgres service container.
- Optional Prometheus text-format `GET /metrics` (config `observability.metrics_enabled`, default off): request counts and latency histogram, store errors, attempts recorded and sessions served.
- Open-source project files: LICENSE (Apache-2.0), NOTICE, README, CONTRIBUTING, SECURITY, CODE_OF_CONDUCT, CHANGELOG.
- `docs/dependencies.md` listing the allowed third-party dependencies, and `docs/adr/` for architecture decision records.
- GoReleaser configuration, release workflow (tag-triggered) and public multi-arch distroless image on GHCR; see `docs/releasing.md`.
- `deploy/` Docker Compose example running the published image with Pocket ID and Caddy, with a sample `quizzotic.yaml`.

[Unreleased]: https://github.com/chan-labs/quizzotic/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/chan-labs/quizzotic/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/chan-labs/quizzotic/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/chan-labs/quizzotic/compare/v0.1...v0.2.0
[0.1]: https://github.com/chan-labs/quizzotic/releases/tag/v0.1
