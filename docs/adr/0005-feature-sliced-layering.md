# 0005. Feature-sliced layering

Status: Accepted

## Context

The system has several features (auth, problems, tags, quizzes, attempts, review, metrics) and several front ends (web, CLI, MCP) over two storage backends. Without firm boundaries, business rules leak into handlers and SQL leaks into services.

## Decision

Code is grouped by feature under `internal/<feature>`, each with three layers: `handler.go` (HTTP only), `service.go` (business rules and transactions, no `net/http` or SQL) and `repo.go` (a persistence interface owned by the feature). Dependencies point one way: handler -> service -> repo interface. Repos are implemented once per backend in `internal/store/<backend>` and checked by a shared conformance suite. Features call each other only through services. Pure algorithms (`fsrs`, `grading`) are leaf packages with no I/O. The web UI, CLI and MCP are adapters over the services or API and never duplicate rules. Storage and identity are interfaces chosen by config, with no runtime plugin loading.

## Consequences

- Rules live in one place and can be tested without HTTP or a database.
- Adding a storage backend means implementing the repo interfaces and passing the conformance suite.
- More files and interfaces than a flat layout.
- Cross-feature needs must go through a service, which can force API additions.
