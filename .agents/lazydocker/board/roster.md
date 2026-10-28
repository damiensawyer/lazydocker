# Roster — who works this repo, and what they should be given

Orchestrator writes this file. This board is fresh (scaffolded 2026-10-28 from the dadmono
board, which the protocol and scripts descend from), so there is no commit history to reason
from yet. Tiers below are the starting assumption per model; they are revised after each
model has landed work here. **The machine-readable registry is
[`agents.tsv`](agents.tsv)** — id, tier, model, one line each, and the single place those
facts live. This document is the reasoning.

| id | Model / harness | Tier | Give it |
| --- | --- | --- | --- |
| `opus` | Claude Opus 5.5 (Anthropic, via Claude Code) | heavy + orchestrator | Design, plan authoring, credential/auth code review, delegation, review. |
| `fable` | Claude Fable 5.1 (Anthropic, via Claude Code) | heavy | Everything `opus` gets. Multi-file phases, cross-cutting change. Must ship tests. |
| `glm` | GLM 5.3 (Z.ai, via pi) | heavy | Multi-file phases and cross-cutting change. Must ship tests. |
| `glm-flash` | GLM 5.3 Flash (Z.ai, via pi) | medium | Well-specified single tasks with in-tree precedent to copy. Must ship tests. |
| `sonnet` | Claude Sonnet 5 (Anthropic, via Claude Code) | medium | Well-specified single tasks with an in-tree pattern to copy, and reviews of `light`/`medium` tasks. Must ship tests. |
| `gemini` | Gemini 4 Argon (Google, via agy) | heavy (provisional) | Same as `glm`. Unproven on this board — first commits get an `opus` review before anything builds on them. |
| `gemini-flash` | Gemini 3.8 Flash (Google, via agy) | medium | Added 2026-10-28 when Gemini 4 Argon's agy quota ran out (resets in ~7 days). Well-specified single tasks; must ship tests. |
| `swift` | Swift 1.5 (Qwen3.8 27B fine-tune, via pi / llama.cpp ROCm, local) | light | Same as `qwen`. Runs on the local GPU — one instance only. |
| `qwen` | Qwen3 27B (local, via pi / llama.cpp) | light | Mechanical, template-following, one or two files, verified by one command. |

Tier is set by what going wrong costs, not by model size. `heavy` is design, cross-cutting
change, anything touching **credentials or auth**, and every GATE. `medium` is one specified
task in one subsystem. `light` is an in-tree template to copy, at most two files, one command
that says pass or fail. `human` is the user's.

## Running several instances of the same model

**The board id is not the model.** Several sessions of one model run at once, and each needs
its own id so the board can tell them apart. **The bare id is instance 1**; `-2`, `-3` … are
the rest. Two sessions sharing an id overwrite each other's claim file and the board silently
loses one of them. Adding an instance is a **single edit: one row in
[`agents.tsv`](agents.tsv)**. `claim.sh` refuses an unknown agent rather than write an
unattributable commit. Tier follows the model, so a second instance is more hands, never a
higher tier. Attribution stays model-level: `Agent: <id>` names the session,
`Co-Authored-By:` names the model.
