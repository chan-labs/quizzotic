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
7. **Tests.** New repo behavior gets conformance cases in `internal/store/storetest`. Run `mise run test ::: lint` before calling anything done. Any change touching repo/store code, `migrations/` or `internal/store/**` must ship its Postgres implementation and pass `mise run test-postgres` (needs docker; CI runs the same suite) in the same PR, not a follow-up. Every user-visible feature (route, background job, metric, setting, scheduling or grading change) needs a test that goes through the real server: `server.Run` with `server.Options{Listener: ...}` and a real `config.Config`, the path `quizzotic serve` takes from `cmd/quizzotic/main.go`, so everything `internal/server/server.go` wires (`authOptions`, `WithMetrics`, `WithScheduling`, the idempotency sweep, the snapshotter, `Handler`) is exercised. A test that builds the feature's service or handler by hand in its own package does not prove it is installed; if a feature needs a new option or registration in `server.go`, add it there and prove it through the server.
8. **Public repository.** Nothing committed names the maintainer, their domains, or their infrastructure. Use `example.com` hosts. Deployment here means the published container image; environment-specific config stays out of the repo.
9. **Done means Done-when.** A task is complete only when its Done-when check passes and CI is green on the PR.
10. **Plain `git` only.** If git (signing, push, the 1Password agent) is refused or fails, stop and report the exact refusal. No `/usr/bin/git`, proxies, hook changes or disabling signing.
11. **Merging.** Reviewer PASS plus every required check green on the head commit; the coordinator's merge rules are in `.claude/skills/coordinate/SKILL.md` step 3.6.

## Parallel work

New features register themselves in `internal/server/wire_<feature>.go` (an `init` calling `addFeature`) instead of editing `server.Handler`, and new migrations are named `YYYYMMDDHHMMSS_name.sql` in both dialect directories, never the next integer. Details: CONTRIBUTING.md, "Avoiding merge conflicts between features".

## Commands

- Tasks live in `mise.toml` (`mise install` once; it pins Go, Node and staticcheck). `mise run build` · `mise run test` (`go test -race ./...`) · `mise run lint` (`go vet` + `staticcheck` + Redocly lint of `api/openapi.yaml`) · chain with `mise run test ::: lint`. `mise run test-postgres` (throwaway postgres:17 via docker) · `mise run build-all` · `mise run ci` (all of CI).
- `mise run dev` runs the server and web UI locally at `http://localhost:8089/` with a fake OIDC issuer (navikt/mock-oauth2-server in Docker) and seed data; also `dev-issuer`, `dev-server`, `dev-reset`. Agents can start it with `preview_start` (`.claude/launch.json`). See CONTRIBUTING.md.
- `quizzotic serve --config quizzotic.yaml`
- Coordinate a milestone: `/coordinate M2` (see `.claude/skills/coordinate/SKILL.md`)
