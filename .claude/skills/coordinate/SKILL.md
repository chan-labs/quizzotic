---
name: coordinate
description: Coordinate one Quizzotic milestone (a Todoist section, e.g. M2) end to end — build the task dependency graph, dispatch ready tasks to implementer subagents in parallel worktrees, review each with a fresh reviewer, open PRs, and keep Todoist in sync. Use when the user runs /coordinate <milestone> or asks to work through a milestone or epic. Also runs a cross-session retro with /coordinate retro.
argument-hint: <milestone, e.g. M2, or retro> [--max-parallel N] [--auto-merge] [--dry-run]
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
- Run `mise run doctor` and keep its output. Pass it to every subagent you dispatch, so none of them has to rediscover what this machine lacks (e.g. "docker MISSING" means rely on CI's `test (postgres)` job; a missing `workflow` scope means workflow changes can't be pushed). Stop if it exits non-zero.
- `gh` is pinned in `mise.toml`; call it as `mise exec -- gh` (it may not be on `PATH`). `mise exec -- gh auth status` succeeds and the repo has a GitHub remote. **M0 exception:** if there is no repo yet, M0.1 creates it (`mise exec -- gh repo create quizzotic --public --source . --push`) before anything else is dispatched.

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
3. **Open the PR** with `mise exec -- gh pr create`: title = task name; body = the Done-when (no tracker links), and the reviewer's evidence.
4. **Record** on the Todoist task: a comment with the PR URL and review verdict. Tick the subtasks the implementer reports done.
5. **CI:** `mise exec -- gh pr checks --watch`. Red CI goes back to the implementer and counts as a fix round.
6. **Merge:** merge needs a reviewer PASS and every required check green on the exact head commit; re-check after any branch update or push. `main` requires its checks and binds admins: never use `--admin`, and never rename or remove required check jobs. With `--auto-merge`, run `mise exec -- gh pr merge <N> --squash --auto --delete-branch`. **Prefer the merge queue once it is on for `main`:** check with `mise exec -- gh api graphql -F owner='{owner}' -F name='{repo}' -f query='query($owner:String!,$name:String!){repository(owner:$owner,name:$name){mergeQueue(branch:"main"){url}}}' --jq '.data.repository.mergeQueue.url // "off"'`. It prints the queue's URL when the queue is on and `off` when it isn't, and works whether the queue was set in branch protection or a ruleset. With the queue on, that same command queues the PR. The queue tests it against the latest `main` (CI runs on `merge_group`), so don't merge `main` into a queued PR by hand. Merge `main` in only to resolve a real conflict, and that needs a fresh review per step 2. Without the queue, the command enables plain auto-merge, and a branch that falls behind needs `main` merged into it; no force-push and no rebase of pushed branches. After merging `main`, and before pushing, run `mise run test ::: lint` locally whenever the merge touched files the task changed (plus `mise run test-postgres` for store or migration changes); CI is not the first check. Without `--auto-merge`, comment "PR ready for review" and treat the task as waiting.
   - **Auth work** (login, sessions, token validation, OIDC settings) is not done until it has been exercised against a real OIDC issuer, a real provider the maintainer controls. The mock dev issuer (`mise run dev`) and other mocks do not count: mock-only tests have hidden a real login failure. If no agent can reach a real issuer, label the check `needs-human` and say so in the report instead of marking the auth work done.
7. **Complete** the Todoist task only when it is merged, its Done-when passed, and it is not `needs-human`.
   - **After every merge, update the tracker.** Done-when verified and no `needs-human` label: complete the task. Otherwise comment `Code merged in #<N>. Remaining: <the exact Done-when check>` and add the `needs-human` label if it is not there. Never leave a task whose PR has merged unlabelled and silent: the next reader will take it for unstarted work and the milestone looks less done than it is.
8. Recompute the ready set. A task is ready only when every dependency is merged to `main`; pull `main` before the next dispatch.

## 3a. Multi-lane execution (separate executor sessions)

Sometimes the coordinator hands tasks to separate executor sessions ("lanes"), each with its own brief, instead of to subagents. Every lane follows section 3 for its own tasks, plus these rules:

1. **Size lanes to the plan's quota.** Run only as many lanes as the account's usage window can carry (three on a Pro plan). A lane that hits the usage limit stops and says so. If its brief sets a resume time, it schedules a one-shot wake-up (for example `CronCreate` with `recurring: false`). It never works around the limit.
2. **Lanes never share files.** Give each lane tasks whose files don't overlap with any other lane's. Tasks that refactor shared files (`internal/server/server.go`, `api/openapi.yaml`, this skill, CI workflows) go to one lane, run in order, or wait until the other lanes' PRs merge.
3. **One worktree per task.** A lane branches each task from a fresh `origin/main` with `git worktree add ../qz-<lane>-<task> -b <branch> origin/main`, and never checks out a branch in the shared main checkout. Stash is shared across worktrees, so it uses temporary WIP commits instead. Once a task's PR merges, it removes the worktree with `git worktree remove`.
4. **Parallel tooling.** `.golangci.yml` sets `run.allow-parallel-runners`, so `mise run lint` in several worktrees at once no longer fails with "parallel golangci-lint is running". If that error comes back, check the worktree's `.golangci.yml` for the setting: a worktree created before FF.106 merged lacks it, so merge `main` into that branch. Go's build and test caches are safe to share. Long runs (`mise run test ::: lint`) go in the background.
5. **The brief carries the machine facts.** Pass every lane the `mise run doctor` output and the facts from `CLAUDE.local.md`: how to call `gh`, the git identity, missing docker. That way no lane has to rediscover them. Name the tracker tasks by ID, in order, and give the exact tracker comment for any task that ends `needs-human`.
6. **Review agent.** A lane started outside the repository may not have the `quizzotic-reviewer` agent type registered. In that case it runs a fresh general-purpose subagent told to read and follow `.claude/agents/quizzotic-reviewer.md`. That still counts as the fresh review step 2 requires.
7. **Be lean.** Run one full `mise run test ::: lint` per change, not one per edit. Give a clean diff one reviewer pass. Don't re-run the full suite after a review that changed nothing.
8. **Blocker rule.** If the same failure happens 3 or more times (a red check, a tool error or timeout, a non-git refusal), the lane stops retrying. Git refusals (signing, push, the 1Password agent) are never retried: stop at once and report the exact refusal (`AGENTS.md` rule 10). It files `BLOCKER: <task id> <what failed>` with the `needs-human` label in the tracker's "For Human; Don't block work" section, then moves to its next task. If the tracker itself is unreachable, it keeps working on the code and puts every tracker update it owes (PR links, verdicts, completions, labels) in its final report so the coordinator can post them.
9. **Report.** For each task, a lane's final report gives the PR link, the PR state, the tracker status, and anything that cost repeated effort. The coordinator checks each report against `origin/main` and the PR before recording it (section 6, step 2).

## 4. Stop conditions

- **Spec conflict** reported by an implementer or reviewer: comment the conflict on the task, skip it and its dependents, continue with the rest.
- **Blocked** after 2 fix rounds, or an unmeetable dependency: same as above.
- **Waiting on the maintainer** (unmerged PRs without `--auto-merge`, or `needs-human` checks): keep going on whatever else is ready; when nothing is ready, stop.
- **Never end a turn with an armed PR that is behind `main`** (unless the merge queue is on for `main`; it tests against the latest `main` itself). Plain auto-merge does not update the branch. Merge `main` in (running the local tests per step 3.6) and keep a watcher running, or say plainly in the report that the PR is armed but behind and will not merge by itself.
- If git (signing, push, the 1Password agent) is refused or fails, stop and report the exact refusal; see `AGENTS.md` rule 10. Tell every subagent the same.
- Never add a dependency outside the allowed list, never edit `docs/spec.md`, never push to `main` directly.

## 5. Report

Finish with a short summary:
- tasks completed and PRs open (with links),
- tasks blocked and why,
- `needs-human` tasks with the exact manual step for each,
- whether the milestone's exit task ("(Mx exit)") is done, and the next milestone to run.

Post the same summary as a comment on the milestone's exit task so it is visible in Todoist.

## 6. Retro (`/coordinate retro`)

Run this when the maintainer asks, or after several milestones finish. Skip sections 0 to 5. The retro changes the plan, not the code: dispatch nothing without the maintainer's go-ahead.

1. **Collect:** read each coordinator session's final report, not only its latest note. Then search transcripts for repeated refusals and errors, and tally CI: failed and cancelled runs, and which jobs failed. Also read the maintainer's comments and completions in the tracker since the last retro, and act on them: fold them into the plan, close or adjust tasks, and reply where a reply is needed.
2. **Verify against `main`:** check each reported gap on the current `origin/main` before filing it. Reports go stale: drop what's fixed, close tasks that are done, and correct their descriptions. For anything reported as wired up, check `internal/server/server.go`.
   - **Reconcile merged work with the tracker:** for each open task ID, run `git log --oneline origin/main | grep -F "<ID> "`. Every open task whose ID appears in a merged PR title must have a comment naming the merged PR and what remains, or be completed (step 3.7). Fix any that don't before filing new work.
3. **Run it:** start the service with `mise run dev` and load the main pages. Tests passing doesn't show the app works. If it won't start, that's the first finding.
4. **Report status:** progress per milestone, what the running app supports today, blockers, and the exact manual steps waiting on the maintainer.
5. **Summarize issues:** a table of each recurring problem, its evidence with counts, and a proposed fix. Lead with whatever costs the most rework. Keep process problems separate from product bugs.
6. **File:** add follow-ups to the `Fast follow` Todoist section with the next `FF.<n>` ID and **Source**, **Build** and **Done when** lines. Group small related items into one task, and skip duplicates. Label anything needing a decision, a settings change or a real-world check `needs-human`.
7. **Sort, then dispatch on approval:** once the maintainer agrees, move each task to its milestone section and hand it to that milestone's coordinator. Anything left over gets its own session. Items that change a coordinator's own in-flight work go first; refactors of shared files wait until those PRs merge. Hold worktree cleanup until no coordinator is running.
8. **Close the loop:** turn every process fix that's accepted into a rule here, in `AGENTS.md` or in the agent docs, so the next run inherits it instead of relying on a brief.
