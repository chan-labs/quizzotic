---
name: coordinate
description: Coordinate one Quizzotic milestone (a Todoist section, e.g. M2) end to end — build the task dependency graph, dispatch ready tasks to implementer subagents in parallel worktrees, review each with a fresh reviewer, open PRs, and keep Todoist in sync. Use when the user runs /coordinate <milestone> or asks to work through a milestone or epic.
argument-hint: <milestone, e.g. M2> [--max-parallel N] [--auto-merge] [--dry-run]
---

# Coordinate a milestone

You are the **coordinator**. You plan, dispatch, verify and record. You do not write feature code yourself; implementer subagents do. Read `AGENTS.md` first and follow its rules.

Arguments: `$ARGUMENTS` — the milestone (`M0`…`M10`), plus optional flags:
- `--max-parallel N` (default 3): most implementers running at once.
- `--auto-merge`: squash-merge a PR yourself once review passes and CI is green. Without it, leave PRs for the maintainer.
- `--dry-run`: build and show the plan, change nothing.

## 0. Preflight

- `docs/spec.md` exists. If not, stop and ask the maintainer.
- The `todoist` MCP server is connected (`/mcp`). If not, stop and say how to add it.
- `gh auth status` succeeds and the repo has a GitHub remote. **M0 exception:** if there is no repo yet, M0.1 creates it (`gh repo create quizzotic --public --source . --push`) before anything else is dispatched.

## 1. Load the milestone

1. With the `todoist` MCP tools, find the project named in `CLAUDE.local.md` and the section whose name starts with the milestone (`M2 ·`).
2. Load every open task in that section with its description, labels, comments and subtasks.
3. Parse each task: ID from the name (`M2.3`), **Depends on** IDs, **Done when**, `needs-human` label.
4. Resolve dependencies outside this section: a dependency on an earlier milestone counts as met only if that Todoist task is completed, or (for `needs-human` tasks) its code PR is merged and a comment says so. If any are unmet, stop and report which.

## 2. Plan

1. Build the dependency graph inside the section; reject cycles.
2. Group into waves: wave 1 = tasks with every dependency met; wave N = tasks whose dependencies sit in earlier waves.
3. `needs-human` tasks are scheduled like any other, but the implementer only prepares the code and writes the manual check as a checklist. You never complete them; you comment on the task what the maintainer must do.
4. Show the plan as a short table (wave · task · depends on · human-gated). With `--dry-run`, stop here. Otherwise ask the maintainer to confirm once, then run unattended.

## 3. Execute (loop until the section is done or blocked)

For each ready task, up to `--max-parallel` at a time:

1. **Dispatch** a `quizzotic-implementer` subagent with `isolation: "worktree"`, in the background so independent tasks run in parallel. Pass:
   - the full task text and its subtasks,
   - the branch name `m<milestone>.<n>-<slug>`,
   - the spec sections named on the task's **Spec** line (it reads them in `docs/spec.md`),
   - the merged dependency PRs it builds on (base is always `main`),
   - the real-server wiring rule: each user-visible feature must be installed in `internal/server/server.go` and proved by a test through `server.Run` (as `quizzotic serve` does), not only by its own package's tests.
2. **Review** each finished branch with a fresh `quizzotic-reviewer` subagent that has not seen the implementation conversation. Pass the task text and branch name, and tell it to check `internal/server/server.go` for real-server wiring and FAIL features only tested with hand-built dependencies.
   - `FAIL` → send the findings back to the same implementer with SendMessage. At most 2 fix rounds; after that, comment the open findings on the Todoist task and mark it blocked in your notes.
   - `PASS` → continue. Any code change after a PASS, and any non-trivial conflict resolution, needs a fresh `quizzotic-reviewer` (a new one, not the previous), and merge waits for its PASS.
3. **Open the PR** with `gh pr create`: title = task name; body = the Done-when (no tracker links), and the reviewer's evidence.
4. **Record** on the Todoist task: a comment with the PR URL and review verdict. Tick the subtasks the implementer reports done.
5. **CI:** `gh pr checks --watch`. Red CI goes back to the implementer and counts as a fix round.
6. **Merge:** merge needs a reviewer PASS and every required check green on the exact head commit; re-check after any branch update or push. `main` requires its checks and binds admins: never use `--admin`, and never rename or remove required check jobs. With `--auto-merge`, enable GitHub auto-merge (`gh pr merge --squash --auto`) or use the merge queue, and delete the branch once merged. If the branch is behind, merge `main` into it; no force-push and no rebase of pushed branches. Without `--auto-merge`, comment "PR ready for review" and treat the task as waiting.
7. **Complete** the Todoist task only when it is merged, its Done-when passed, and it is not `needs-human`.
8. Recompute the ready set. A task is ready only when every dependency is merged to `main`; pull `main` before the next dispatch.

## 4. Stop conditions

- **Spec conflict** reported by an implementer or reviewer: comment the conflict on the task, skip it and its dependents, continue with the rest.
- **Blocked** after 2 fix rounds, or an unmeetable dependency: same as above.
- **Waiting on the maintainer** (unmerged PRs without `--auto-merge`, or `needs-human` checks): keep going on whatever else is ready; when nothing is ready, stop.
- If git (signing, push, the 1Password agent) is refused or fails, stop and report the exact refusal; see `AGENTS.md` rule 10. Tell every subagent the same.
- Never add a dependency outside the allowed list, never edit `docs/spec.md`, never push to `main` directly.

## 5. Report

Finish with a short summary:
- tasks completed and PRs open (with links),
- tasks blocked and why,
- `needs-human` tasks with the exact manual step for each,
- whether the milestone's exit task ("(Mx exit)") is done, and the next milestone to run.

Post the same summary as a comment on the milestone's exit task so it is visible in Todoist.
