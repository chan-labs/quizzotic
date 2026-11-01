# 0006. Stdlib-first dependency policy

Status: Accepted

## Context

Quizzotic is a self-hosted service meant to be a single, easy-to-audit binary. Each third-party dependency adds supply-chain and maintenance cost.

## Decision

Use the Go standard library for HTTP, routing (Go 1.22+ `ServeMux` patterns), templates, crypto and JSON. A small named set of third-party modules is allowed: `modernc.org/sqlite` (pure-Go, keeps the binary cgo-free), `github.com/jackc/pgx/v5`, `gopkg.in/yaml.v3`, the official MCP `go-sdk`, `github.com/yuin/goldmark` (raw HTML disabled) and OpenTelemetry. Each needs a one-line justification in `docs/dependencies.md`. Anything else requires the maintainer's approval and an entry there.

## Consequences

- A small dependency surface and a cgo-free, single-binary build.
- Some code (JWT verification, JWKS, PKCE, sessions) is written in-house rather than imported, and must be tested carefully.
- Contributors cannot add a library casually; new ones need an explicit decision.
