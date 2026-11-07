# Quizzotic

Open-source spaced-repetition quiz service. Written in Go (stdlib-first), backed by SQLite or Postgres, authenticated through any OIDC identity provider. Licensed under Apache-2.0.

> **Status:** early development. This README is a stub; a 5-minute quickstart will land with the first runnable milestone.

## What it will be

- One static binary, `quizzotic`: web UI and REST API (`serve`), MCP server (`mcp`), and a spec-driven CLI.
- FSRS-based scheduling, with an append-only attempt history.
- SQLite by default, Postgres optional, any OIDC IdP.

See [`docs/spec.md`](docs/spec.md) for the full design. OpenTelemetry tracing is off by default and enabled with standard `OTEL_*` variables: see [`docs/tracing.md`](docs/tracing.md).

## Contributing

Tooling is managed with [mise](https://mise.jdx.dev): `mise install`, then `mise run test ::: lint`. See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: see [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
