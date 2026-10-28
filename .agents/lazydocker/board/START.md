# Starting an agent — the exact words to give it

An agent cannot claim work if it does not know **which agent it is**. "Do the next task" on
its own leaves that unanswered, and a fresh session has no way to guess whether it is `glm`
or `qwen`. So give it one line naming its id. Everything else it reads for itself.

## The boot line

Paste this into a fresh agent session, changing only the id:

> You are agent `glm-flash` on this repo. Read `.agents/lazydocker/board/README.md` and follow it. Take the next
> task you are allowed to take from `.agents/lazydocker/board/QUEUE.md`, claim it first, then do it per
> docs/REGISTRY_PLAN.md. All Go commands need `GOFLAGS=-mod=vendor` (see CLAUDE.md). Keep going task after
> task without asking permission to continue.

Valid ids, and what each may take (`.agents/lazydocker/board/roster.md`):
`opus` `fable` `glm` heavy · `glm-flash` `glm-flash-2` `sonnet` `sonnet-2` medium ·
`qwen` `qwen-2` light.

**`glm` and `glm-flash` are different models, not instances of one.** Take the id that names
the model you actually are; `claim.sh` stamps its tier into your lock from `agents.tsv`.

**Running several instances of one model? Give each a different id.** Two sessions sharing an
id overwrite each other's claim file and the board silently loses one of them. The bare id is
instance 1; use `-2`, `-3` for the rest. Attribution stays model-level either way — the commit
carries `Agent: glm-flash-2` alongside `Co-Authored-By: GLM 5.3 Flash (Z.ai, via pi)`, so
history still reads "GLM 5.3 Flash did this". Tier follows the model, so a second instance is
more hands, never a higher tier.

If an agent has an assignment waiting (`.agents/lazydocker/board/assignments/<id>.md`), that outranks the
queue and it will pick it up on its own — you do not need to mention it.

## Taking one off the work

Do not chase the session. Write the directive and it stops at its next checkpoint:

```sh
printf 'to: glm-flash\nfrom: user\ndirective: stop\nposted: %s\nreason: |\n  Stop after the current commit; release your claim.\n' \
  "$(date -u '+%Y-%m-%dT%H:%MZ')" > .agents/lazydocker/board/assignments/glm-flash.md
git add .agents/lazydocker/board/assignments/glm-flash.md && git commit -m "[coord] stop directive for glm-flash" && git push origin mydev
```

`directive: hold` is the softer form: finish what you hold, start nothing new.
The agent then runs `.agents/lazydocker/board/claim.sh stop glm-flash` itself, which empties its `task:` and sets
`status: idle` — its name comes off the board.

## Before you start several at once

1. `.agents/lazydocker/board/board.sh` — see who holds what and whether the queue has room.
2. Read `SLATE.md` — the honest capacity is two agents right now (stream A + stream B);
   a third adds contention on `docs/REGISTRY_PLAN.md` for no parallel gain.
3. Give each its own id (see above) and its own boot line.
