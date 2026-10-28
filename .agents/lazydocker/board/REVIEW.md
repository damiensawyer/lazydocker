# The review gate — what a reviewer checks, and how

**Every commit that changes application code gets reviewed by an agent of the task's own tier
or above — which did not write the commit — before its phase GATE.** Not per-commit blocking
— that would stall agents running unattended — but a standing queue that must be empty before
a GATE can pass.

The point is not taste. This fork is judged on **not regressing lazydocker**: a user with no
registry config must see exactly the lazydocker they had before, and the registry code must
never leak credentials (they live in memory only, never in logs, never in `go.mod`-visible
config dumps). Those are checkable, so the review is a checklist rather than an opinion.

## Who reviews

**An agent of the reviewed task's own tier or above, that did not write the commit.** A
sibling instance of the author's own model counts — what matters is a different session with
different context, not a different vendor.

| Reviewed task's tier | Who may review it |
| --- | --- |
| `heavy` (incl. every ⚠️ and GATE) | `opus` `fable` `glm` |
| `medium` | the above, plus `glm-flash` `sonnet` |
| `light` | any agent |

*These rows name bare ids; the numbered siblings share their tier.* The authority for any
id's tier is [`agents.tsv`](agents.tsv) and nothing else — look yourself up there rather than
matching a name in this table.

Claim it like any task:

```sh
.agents/lazydocker/board/claim.sh take <you> REVIEW-<taskId> "<the files the commit touched>"
```

Review findings go back as either a fix commit (small, obvious) or a new plan task (anything
needing judgement). Never silently rewrite a colleague's work — say what you changed and why
in the commit body, and cite the checklist item by number.

**A SELF-REVIEW IS NOT A REVIEW, AND MUST NOT BE FILED AS ONE.** Keep re-reading your own
work — it is clearly productive — but do not claim `REVIEW-<taskId>` for a commit you wrote.
The `REVIEW-<taskId>` commit is what the phase GATE reads as "an independent agent looked at
this", and a self-review makes the gate claim an independence it does not have. Put the fixes
in a normal follow-up commit (`[L1.2] assert the bearer token is cached per scope …`), and
leave the `REVIEW-` id for someone else. A sibling INSTANCE of your own model (`glm-2`
reviewing `glm`) satisfies the rule; the same session an hour later does not.

## The checklist

1. **No regression with zero config.** Start lazydocker with no registry settings — every
   existing panel behaves exactly as before.
2. **No credential leak.** Passwords/tokens are never logged (`gui.Log`, error strings,
   panics), never rendered in a view, never written to disk except the connections file the
   user explicitly saved.
3. **Tests say pass or fail.** Registry client work runs against `httptest` fixtures — no
   network in unit tests. `GOFLAGS=-mod=vendor go test ./...` green.
4. **The plan row and §21 agree.** The task's box is ticked only with a real commit under its
   id, and the §21 row names that commit.
5. **vendor/ untouched by hand** — dependency changes go through `go mod vendor` and are
   their own commit.
