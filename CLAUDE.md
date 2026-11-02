# Quizzotic

Open-source spaced-repetition quiz service. Go (stdlib-first), SQLite/Postgres, any OIDC issuer, Apache-2.0.

## Sources of truth

- **Spec:** `docs/spec.md`.
- **Work plan:** the maintainer's Todoist project. Its location is in the gitignored `CLAUDE.local.md`; never commit tracker IDs or links.
  - One section per milestone: `M0 · Foundations` … `M10 · FSRS parameter fitting`.
  - Task names start with an ID (`M2.3 …`). Each description has **Spec**, **Depends on**, **Build**, **Done when**.
  - Subtasks are steps inside the same PR.
  - Label `needs-human`: the task (or its Done-when check) needs the maintainer — real IdP logins, running an instance, multi-day dogfooding, judging their own data. Agents may prepare the code but must not complete these tasks.

## Rules every session follows

1. **The spec wins.** If a task conflicts with `docs/spec.md`, do not guess: comment on the Todoist task with the conflict and skip it.
2. **One task = one PR.** Branch `m<milestone>.<n>-<short-slug>` (e.g. `m2.3-create-get-list-problems`). PR title = task name. PR body quotes its Done-when and carries no tracker links or personal details.
3. **Spec-first API.** Change `api/openapi.yaml` before handlers. The contract test must pass.
4. **Layering.** Feature packages (`internal/<feature>`) have `handler.go → service.go → repo.go`. Handlers do HTTP only; services hold rules and transactions; repos are interfaces implemented in `internal/store/<backend>`. Features call each other only through services. `internal/fsrs` and `internal/grading` are pure leaf packages.
5. **Dependencies.** Only those listed in the spec's Architecture section (`modernc.org/sqlite`, `pgx/v5`, `yaml.v3`, MCP `go-sdk`, `goldmark`, OpenTelemetry). Anything else needs the maintainer's approval and an entry in `docs/dependencies.md`.
6. **Data rules.** Attempts are append-only. Every repo method takes the owner user ID. New endpoints are registered with the owner-scoping test helper (M1.9).
7. **Tests.** New repo behavior gets conformance cases in `internal/store/storetest`. Run `mise run test ::: lint` before calling anything done.
8. **Public repository.** Nothing committed names the maintainer, their domains, or their infrastructure. Use `example.com` hosts. Deployment here means the published container image; environment-specific config stays out of the repo.
9. **Done means Done-when.** A task is complete only when its Done-when check passes and CI is green on the PR.

## Commands

- Tasks live in `mise.toml` (`mise install` once; it pins Go, Node and staticcheck). `mise run build` · `mise run test` (`go test -race ./...`) · `mise run lint` (`go vet` + `staticcheck` + Redocly lint of `api/openapi.yaml`) · chain with `mise run test ::: lint`.
- `make <task>` still works as a thin shim over `mise run <task>`; it will be removed.
- `mise run dev` runs the server and web UI locally at `http://localhost:8089/` with a fake OIDC issuer (`tools/devissuer`) and seed data; also `dev-issuer`, `dev-server`, `dev-reset`. Agents can start it with `preview_start` (`.claude/launch.json`). See CONTRIBUTING.md.
- `quizzotic serve --config quizzotic.yaml`
- Coordinate a milestone: `/coordinate M2` (see `.claude/skills/coordinate/SKILL.md`)
