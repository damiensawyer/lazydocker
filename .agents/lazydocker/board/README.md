# The agent board — how several LLMs work this repo at once without colliding

**This file is the coordination protocol. docs/REGISTRY_PLAN.md remains the authority on *what* to
work on; this is the authority on *who is working on it right now*.** Read both before
touching anything. REGISTRY_PLAN §1 defers to this file for task selection.

This board descends from the dadmono board (same scripts, same rules), where a fleet of agents
produced five avoidable collisions in one afternoon — double commits, one agent's work landing
inside another's commit, commits with no attribution. The rules below exist because each of
those happened; the incidents themselves belong to the other project and are not repeated here.
The scripts enforce what can be enforced; the rest is on you.

## The three rules

1. **One writer per file.** Every coordination file has exactly one agent allowed to write it.
   Your claim file is yours; your inbox is the orchestrator's. Two agents never edit one
   coordination file, so git never has to merge one.
1a. **Take your tier or below, never above.** Every plan task ends with `` `[tier | ids]` ``.
   `light` is for small local models: an in-tree template to copy, at most two files, one
   command that says pass or fail. `medium` is one specified task in one subsystem. `heavy` is
   design, cross-cutting change, **anything touching credentials or auth**, and every GATE.
   `human` is the user's. The tier is set by what going wrong costs, not by size — credential
   code is `heavy` because leaking a token is unbounded harm. If a tier looks wrong, say so in
   your claim's `note:`; never retier it to take it.
1b. **Know which instance you are.** Several sessions of the same model run at once — `glm`,
   `glm-2`, `glm-3` — and each has its own claim file. Two sessions sharing an id overwrite
   each other's claim, and the overwritten one then works with no footprint protection at all.
   Your id was given to you in your boot line; if you were not given one, stop and ask rather
   than guessing. Attribution stays model-level: `Agent: <id>` names the session,
   `Co-Authored-By:` names the model.
1c. **Claiming is a compare-and-swap, and you can lose.** Locks are one file per TASK —
   `locks/<taskId>.md` — not one per agent. `claim.sh take` fetches, checks the lock does not
   exist, writes it, and pushes. If someone got there first you are told **"LOST THE RACE:
   held by <them>"**; take something else. With several agents running that is normal, not an
   error, and not worth retrying.
   The mutex is git's own atomic ref update: two agents taking one task write the same path,
   so the second push is rejected non-fast-forward and the merge conflicts on that exact file.
   Nothing else has to coordinate, and the history says who held what and when.
2. **Claim before you work, in a commit, before the first edit.** A claim is public, timestamped
   state — not an intention you hold privately.
3. **Commit named paths directly — `git commit --only -- <paths>` — never `git add` then
   `git commit`.** The `.git/index` is shared worktree state, and `git commit` commits *the
   whole index*, not the paths you checked. A colleague's `git add` between your check and your
   commit lands their files under your message and your `Co-Authored-By`. Run
   `.agents/lazydocker/board/guard.sh` before every commit.

## Layout

```
.agents/
  README.md                 this protocol                        (orchestrator writes)
  lazydocker/board/
    roster.md               who exists, what tier they are       (orchestrator writes)
    SLATE.md                the current parallel work plan        (orchestrator writes)
    QUEUE.md                what may be taken right now           (orchestrator writes)
    claims/<agent>.md       what that agent is doing NOW          (that agent only)
    assignments/<agent>.md  their inbox: delegation + stop        (orchestrator only)
    locks/<taskId>.md       who holds a task                      (claim.sh only)
    board.sh                render the whole board
    claim.sh                take / heartbeat / release / stop
    commit.sh               the one commit door
    guard.sh                pre-commit safety check
    land.sh, worktree.sh    landing and task worktrees
  common/guard.sh           the one guard; board/guard.sh is a wrapper supplying plan-path + id-prefix
  githooks/                 pre-commit, commit-msg, pre-push (installed via core.hooksPath)
  agents/                   reference material, not coordination
```

`<agent>` is the short id from `roster.md`: `opus`, `fable`, `glm`, `glm-flash`, `sonnet`,
`qwen` — plus numbered instances (`opus-2`, `glm-flash-2`, …) registered in `agents.tsv`.

## The work cycle

Run this at the start of every task, and after every commit:

```sh
.agents/lazydocker/board/board.sh              # who is on what, and what is free
```

1. **Read your inbox** — `.agents/lazydocker/board/assignments/<you>.md`.
   - `directive: stop` → finish or abandon cleanly, release your claim, stop. Do not pick
     up anything else. This is how the user takes you off the work.
   - `directive: hold` → do not start anything new; existing claim may be finished.
   - `directive: work` with a `task:` → that task is yours, ahead of ID order.
   - No inbox file, or `directive: auto` → pick your own, per step 2.
2. **Pick a task** (only when your inbox lets you). REGISTRY_PLAN §1's "first unchecked task in ID
   order" still applies, **narrowed by three filters**:
   - it is **unclaimed** (`board.sh` shows no live claim), and
   - it is **within your tier** (`roster.md`), and
   - its **file footprint does not overlap** a live claim's `files:` globs.
   Take the first task that passes all three. Skipping a blocked-by-claim task is not
   reordering the phase — it is the concurrency rule, and it is deliberate.
3. **Claim it, and commit the claim, before your first edit:**

   ```sh
   .agents/lazydocker/board/claim.sh take glm-flash L1.1 "pkg/registry/**"
   ```

   List the globs you actually intend to touch. Too broad blocks your colleagues; too narrow
   and the guard will stop you mid-task (widen it then — `claim.sh files` — and say why).
4. **Work.** Heartbeat on every commit: `.agents/lazydocker/board/claim.sh beat glm-flash`.
5. **Commit with `commit.sh`. It is the four steps below in one command, and unlike the four
   steps it cannot be held wrong:**

   ```sh
   .agents/lazydocker/board/commit.sh glm-flash -m "[L1.1] …" -- <explicit paths>
   .agents/lazydocker/board/commit.sh glm-flash -F - -- <explicit paths>   # long message on stdin
   ```

   It stages the named paths, then runs `guard.sh <you>` **and checks its exit status**, then
   `git commit --only -- <paths>`, then `git push`. Any failure stops the rest.

   The hand-rolled form still works and is what `commit.sh` does:

   ```sh
   git add <explicit paths>     # so guard.sh can inspect what you intend
   .agents/lazydocker/board/guard.sh glm-flash   # refuses if you are about to take someone else's work
   git commit --only -- <the same explicit paths>   # NOT a bare `git commit`
   git push origin mydev                          # every commit, straight away
   ```

   **Use the wrapper anyway.** The classic failure is piping the guard through something else
   in an `&&` chain, so the shell tests *that* command's exit status instead of the guard's
   refusal — the guard prints FAIL and the commit proceeds anyway. The wrapper exists so the
   exit status cannot be lost to a shell operator.

   **And know what `--only` does NOT do.** It snapshots the named paths from the WORKING TREE, so
   it closes the shared-index race and closes nothing at all against a colleague
   editing the same hot file in the same worktree. `docs/REGISTRY_PLAN.md` has several editors at
   once, continuously. `guard.sh`'s plan-diff check is what catches that — which is exactly why
   its exit status has to reach the commit rather than your terminal.

   `guard.sh` also runs `plan-check.py` when your commit touches `docs/REGISTRY_PLAN.md` or
   `docs/features.yaml`, so an untagged task, a credential task tiered below `heavy`, or an id
   pointing at nothing is refused here rather than discovered later.
   **Push after every commit.** Pushing is free; an unpushed commit, on a branch several models
   share, is work nobody else can see and nobody can build on. Never rebase, never force-push,
   never `git stash`.

6. **Release, then GO STRAIGHT BACK TO STEP 1. Do not stop. Do not ask.**

   ```sh
   .agents/lazydocker/board/claim.sh done <you> <taskId>   # writes your note, releases the lock
   .agents/lazydocker/board/board.sh                       # and immediately pick the next one
   ```

   Finishing a task is not the end of your work. It is step 5 of a loop that returns to step 1.

---

## You never ask what to do next

**This is the rule agents break most, and it costs the most.** You are running unattended,
usually while the user is asleep or elsewhere. A question is not a pause — it is a full stop
that lasts until a human happens to look at your terminal, which may be hours.

**So: never end a turn by asking the user which task to take, never present a menu of options
and wait, and never report that you are idle.** If you can see the choice clearly enough to
list the candidates, you can see it clearly enough to pick one. A wrong choice is cheap here —
it is one commit on a fork branch, and it is reviewed.

**When the answer is not obvious, it is still not a question.** Apply these in order and stop
at the first that yields a task:

1. The first row in `QUEUE.md` **Available now** that passes the three filters in step 2
   (unclaimed, your tier or below, footprint free).
2. Any unreviewed task from `bash .agents/lazydocker/board/review-queue.sh` **that you did not
   author** (`git log` knows; `review-queue.sh` does not). Reviewing is real work and it is a
   gate — a GATE cannot open until that queue is empty.
3. The first unchecked task in `docs/REGISTRY_PLAN.md` in id order passing the same three filters.
4. Only if all three are genuinely empty: write what you found in
   `assignments/<you>.md` under `blocked:`, commit it, push, and stop. **That file is how you
   talk to the orchestrator — not the terminal.** Nobody is reading your terminal.

**Ties do not need resolving.** If two rows both qualify, take the lower id and move.

**The two things that ARE worth stopping for**: a `[human]`-tiered task, and anything that
would send credentials over the network to a host other than the registry it belongs to, or
write them somewhere new on disk. Those go in `assignments/<you>.md` under `blocked:` — still
not the terminal, and still not a menu.

**If your inbox says `directive: stop`, stop. That is the only stop signal.** An empty-looking
queue is not one.

## The staging race — why `--only` is not optional

```sh
git add container.go registry.go     # your two files
git diff --cached --name-only        # prints exactly those two — true at this instant
.agents/lazydocker/board/guard.sh glm-flash   # passes — true at this instant
                                     # ← another agent runs `git add` here
git commit -m "..."                  # commits YOUR two files AND THEIRS
```

`git commit` commits the whole index. The colleague's half-finished file, their plan edit,
their 10 MB test fixture — all of it lands under your `[L1.1]` subject and your
`Co-Authored-By`, and the §21 row you write names a commit that carries work it should not.

**`git commit --only -- <paths>` snapshots those paths from the working tree and ignores
whatever else the index holds.** There is no window. Use it every time (or `commit.sh`, which
uses it for you). `git add` still has a job — it is how `guard.sh` sees your intent — but it
is no longer what decides the commit's contents. If the two disagree, `--only` wins and you
are safe.

## Claim file format

`.agents/lazydocker/board/claims/<agent>.md`, plain `key: value` lines. Only that agent writes it.

```
agent: glm-flash
model: GLM 5.3 Flash (Z.ai, via pi)
tier: medium
task: L1.1
status: working
files: pkg/registry/**
started: 2026-10-28T10:00Z
heartbeat: 2026-10-28T10:21Z
note: transport + token cache done; ping next
```

`status:` is one of `idle` · `claimed` · `working` · `blocked` · `stopping`.
`idle` with an empty `task:` means "I am not on anything" — that is what releasing looks like,
and what the user means by "remove your name from that file".

## Stale claims

A claim whose `heartbeat:` is over **45 minutes** old, with no commits from that agent since,
is stale. Any agent may then take the task, but must:

- write the takeover and the stale claim's timestamp in **its own** claim file's `note:`, and
- say it in the commit body.

Never edit or delete another agent's claim file, stale or not. The orchestrator (or the user)
is the only one who reaps a claim file, and does it by writing `directive: stop` into that
agent's inbox first.

**A crashed client does not wait out the 45 minutes.** A client that dies mid-work leaves its
staged work and its live lock behind, and making everyone wait three-quarters of an hour to
touch recoverable work is waste when the death is already known. When a holder's session is
attested DEAD — the user watched it crash, or no such process exists — any agent may take the
task over immediately:

```sh
.agents/lazydocker/board/claim.sh take --force <you> <taskId> "<globs>" "<who attested the crash, what died>"
```

No staleness check, but the reason is **mandatory** and lands in the lock's `note:` and the
takeover commit's subject. Division of labour: `--stale` is the UNATTENDED takeover rule (the
session is silent and *might* come back); `--force` is the SUPERVISED rescue rule (somebody
checked that the session is *gone*). The rescue flow is then the ordinary cycle: commit the
dead agent's staged paths with `commit.sh`, credit the model that actually wrote the work in
the commit body, tick the plan row and say the rescue there, then `claim.sh done`. One task at
a time — take, commit, done, then the next.

## Hot shared files

These attract two agents at once. They are not claimable — everyone needs them — so the rule
is procedural instead:

| File | Rule |
| --- | --- |
| `docs/REGISTRY_PLAN.md` | Edit **only the lines of tasks you own** plus your own §21 commit-index row. Stage it by explicit path. `guard.sh` refuses the commit if the staged diff touches a task ID claimed by someone else. Never `git add docs/REGISTRY_PLAN.md` without reading `git diff --cached docs/REGISTRY_PLAN.md` first. |
| `docs/REGISTRY_PLAN.md` §2 dashboard | Only the agent completing the task that changes the phase touches it. |
| `docs/features.yaml` | Orchestrator writes feature rows; append `plan:` refs only for tasks that exist. |
| `AGENTS.md`, `.agents/lazydocker/board/README.md`, `roster.md`, `QUEUE.md`, `SLATE.md` | Orchestrator only. |
| `go.mod` / `vendor/**` | Claim them explicitly in your `files:` before editing; a dependency change is its own commit (`go mod vendor`, never hand-edit `vendor/`). |
| `pkg/i18n/english.go` | Append-only per task: add your keys at the end of your task's block; conflicts are trivial, edit wars are not. |

## Commit discipline

- Message starts with the task ID: `[L1.1] …`. A commit whose message names no task ID is not
  reviewable and must not happen. Coordination commits use `[coord]`.
- Every agent commit carries `Agent: <id>` and `Co-Authored-By: <the exact model string from
  agents.tsv> <address>`. It is the only record of who did the work; `commit.sh` and the
  `commit-msg` hook both refuse a message without it.
- One task per commit. If you find yourself staging two tasks' files, that is two commits.
- **Never rewrite shared history.** If your work landed inside someone else's commit, do not
  rebase it out — record the truth in §21 and move on.
