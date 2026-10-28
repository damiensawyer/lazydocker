# `.agents/` — one board, for this project

```text
lazydocker/board/   the board — claims, queue, guard, commit protocol
common/guard.sh     the one guard; each board's guard.sh is a wrapper supplying
                    plan-path + id-prefix (a leftover multi-board shape from the
                    board's origin — kept, because the scripts are proven)
githooks/           pre-commit + commit-msg + pre-push, installed via core.hooksPath
agents/             reference material, not coordination
AGENTS.md           agent rules for this repo: port crtui features, never copy repos
```

**Start at your board:** `.agents/lazydocker/board/README.md`. That file is the protocol —
claiming, tiers, committing, landing. Nothing here replaces it.

## Where the plan lives

`docs/REGISTRY_PLAN.md` is the authority on *what* to work on; `docs/features.yaml` is the
feature catalogue it cites. The board is the authority on *who is working on what right now*.

## One board, not one per project

The board's origin ran five projects' boards side by side in one repo, which is why
`common/guard.sh` is shared and the board exports `BOARD_PROJECT` from its own location.
This repo has exactly one project, so the hooks resolve `lazydocker` automatically (and the
fallback in the hooks names it too). The shared-guard shape is kept because rewriting proven
scripts for a single-board repo is how subtle breakage gets introduced, not removed.

## The hooks are installed

`core.hooksPath` points at `.agents/githooks`. A commit that would carry anything runs the
guard; a commit whose message breaks the message rules is refused. Overrides:

- `git commit --no-verify` — the explicit, greppable escape hatch. Say why in the body.
- `git config --unset core.hooksPath` — if you want the machinery dormant while keeping the
  files.

Empty-index (message-only) commits pass silently — the hook does not nag on every `release:`
or `[coord]` message-only commit.
