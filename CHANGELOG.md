# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Metrics and Tags pages. `/stats` shows the overview (recall now, true retention against the desired retention, tags studied), the due forecast for the next 7 days and daily activity; `/tags` is the mastery table (recall, coverage, median stability, lapse rate, true retention, last reviewed; weakest first) and `/tags/{tag}` charts the tag's recall history. Charts are server-rendered SVG with titles, descriptions and a data table each, no script. They read through `metrics.QueryService`, so they show the same figures as the `/metrics/*` API endpoints. A tag's days with nothing studied are left off its chart.
- Metrics endpoints (scope `metrics:read`, also exposed to agents as MCP tools `metrics_overview`, `metrics_tags`, `metrics_tagHistory` and `metrics_problems`): `GET /api/v1/metrics/overview` (mean recall, true retention against desired retention, due load for the next 7 days, daily activity), `GET /api/v1/metrics/tags` (per-tag recall, coverage, median stability, lapse rate and true retention, paged), `GET /api/v1/metrics/tags/{tag}/history?from=&to=` (daily mastery snapshots, filed under the study-day dates the snapshot job writes) and `GET /api/v1/metrics/problems?sort=recall&limit=` (weakest studied problems). Recall is predicted at request time. `metrics.Repo` gains `TagLabels` and `StudiedCards`; no schema change.
- `quizzotic admin rebuild [--user ID]` and `POST /api/v1/admin/rebuild` (scope `admin`, humans only) recompute every card from the attempt log, in batches with progress logs; `auth.UserRepo` gains a paged `ListIDs` for it.
- `quizzotic admin rebuild --history [--user ID [--tag ID|slug]]` rewrites the mastery snapshot history by replaying the attempt log, and the stored problem lifecycle, to the start of every past study day, using the daily job's date convention; `metrics.Backfill` does the work inside a caller's transaction for tag operations.
- Golden tests for the generated CLI help and MCP tool list; `mise run golden` regenerates them.
- Postgres store (`postgres://` in `storage.url`) on pgx/v5 with `timestamptz`/`jsonb` migrations mirroring SQLite, passing the shared conformance suite; CI runs it against a Postgres service container.
- Optional Prometheus text-format `GET /metrics` (config `observability.metrics_enabled`, default off): request counts and latency histogram, store errors, attempts recorded and sessions served.
- Open-source project files: LICENSE (Apache-2.0), NOTICE, README, CONTRIBUTING, SECURITY, CODE_OF_CONDUCT, CHANGELOG.
- `docs/dependencies.md` listing the allowed third-party dependencies, and `docs/adr/` for architecture decision records.
- GoReleaser configuration, release workflow (tag-triggered) and public multi-arch distroless image on GHCR; see `docs/releasing.md`.
- `deploy/` Docker Compose example running the published image with Pocket ID and Caddy, with a sample `quizzotic.yaml`.
