# Quizzotic coordinator setup

Drop these files into the root of the (new, empty) `quizzotic` folder:

```
CLAUDE.md                                  project rules every session reads
.mcp.json                                  Todoist MCP server (OAuth on first use)
.claude/skills/coordinate/SKILL.md         /coordinate <milestone>
.claude/agents/quizzotic-implementer.md    builds one task per worktree
.claude/agents/quizzotic-reviewer.md       fresh-eyes review per branch
```

## One-time steps

1. **Spec:** open the spec doc (https://claude.ai/artifact/NyScP4BV8jJw5cwu9Wxoak), export it as Markdown, save it as `docs/spec.md`. Re-export whenever the doc changes.
2. **Todoist:** start `claude` in the folder, run `/mcp`, pick `todoist`, and sign in. (Or `claude mcp add --transport http todoist https://ai.todoist.net/mcp`.)
3. **GitHub:** `gh auth login`. M0.1 creates the public repo with `gh repo create`.
4. **Go:** Go 1.23+ and `staticcheck` installed.

## Running it

One coordinator session per milestone, in order:

```
/coordinate M0 --dry-run      # check the plan first
/coordinate M0                # run it; PRs wait for your review
/coordinate M2 --auto-merge   # or let it merge once review + CI pass
```

- The coordinator never writes code; implementers work in parallel worktrees (default 3), each reviewed by a separate reviewer before a PR opens.
- Todoist stays in sync: PR links and review results as task comments, subtasks ticked, tasks completed on merge.
- Tasks labelled `needs-human` (Pocket ID setup, real-login checks, m1610 deploy, dogfooding, fitting on your data) get their code prepared and a checklist comment; you complete them.
- Good first moves you can do by hand any time: M1.1 (Pocket ID clients) and M4.1 (FSRS test vectors).
