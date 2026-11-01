# 0001. Spec-first API

Status: Accepted

## Context

Quizzotic exposes the same operations through a REST API, a CLI and an MCP server. Hand-maintaining three surfaces invites drift between them and between the code and its documentation.

## Decision

`api/openapi.yaml` is the contract and is changed before any handler. Server handlers are checked against it in CI (the contract test). The CLI and MCP server read the same embedded spec at runtime, so a new operation appears in both without hand-written glue. Scopes and CLI/MCP hints live in the spec as `x-quizzotic-*` extensions.

## Consequences

- Every API change starts as a spec change; a handler without a spec entry fails CI.
- The CLI and MCP surfaces cannot drift from the REST API.
- The spec must carry enough metadata (scopes, columns, hints) to drive the adapters, which makes it larger and stricter than a documentation-only spec.
