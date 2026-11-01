# Quizzotic

Open-source spaced-repetition quiz service. Go (stdlib-first), SQLite/Postgres, OIDC via Pocket ID, Apache-2.0.

## Sources of truth

- **Spec:** `docs/spec.md` (exported from the spec doc: https://claude.ai/artifact/NyScP4BV8jJw5cwu9Wxoak). If the two disagree, ask Josh which is current.
- **Work plan:** Todoist project `Quizzotic` (id `6hv9xWvPXrJmw6MX`), via the `todoist` MCP server.
  - One section per milestone: `M0 · Foundations` … `M10 · FSRS parameter fitting`.
  - Task names start with an ID (`M2.3 …`). Each description has **Spec**, **Depends on**, **Build**, **Done when**.
  - Subtasks are steps inside the same PR.
  - Label `needs-human`: the task (or its Done-when check) needs Josh — real IdP logins, deploying to m1610, multi-day dogfooding, judging his own data. Agents may prepare the code but must not complete these tasks.

## Rules every session follows

1. **The spec wins.** If a task conflicts with `docs/spec.md`, do not guess: comment on the Todoist task with the conflict and skip it.
2. **One task = one PR.** Branch `m<milestone>.<n>-<short-slug>` (e.g. `m2.3-create-get-list-problems`). PR title = task name. PR body links the Todoist task and quotes its Done-when.
3. **Spec-first API.** Change `api/openapi.yaml` before handlers. The contract test must pass.
4. **Layering.** Feature packages (`internal/<feature>`) have `handler.go → service.go → repo.go`. Handlers do HTTP only; services hold rules and transactions; repos are interfaces implemented in `internal/store/<backend>`. Features call each other only through services. `internal/fsrs` and `internal/grading` are pure leaf packages.
5. **Dependencies.** Only those listed in the spec's Architecture section (`modernc.org/sqlite`, `pgx/v5`, `yaml.v3`, MCP `go-sdk`, `goldmark`, OpenTelemetry). Anything else needs Josh's approval and an entry in `docs/dependencies.md`.
6. **Data rules.** Attempts are append-only. Every repo method takes the owner user ID. New endpoints are registered with the owner-scoping test helper (M1.9).
7. **Tests.** New repo behavior gets conformance cases in `internal/store/storetest`. Run `make test lint` before calling anything done.
8. **Done means Done-when.** A task is complete only when its Done-when check passes and CI is green on the PR.

## Commands

- `make build` · `make test` (`go test -race ./...`) · `make lint` (`go vet` + `staticcheck`)
- `quizzotic serve --config quizzotic.yaml`
- Coordinate a milestone: `/coordinate M2` (see `.claude/skills/coordinate/SKILL.md`)
