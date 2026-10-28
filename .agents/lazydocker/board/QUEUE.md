# The queue — what an agent may take right now

**Orchestrator writes this.** `- [ ]` in the plan does not mean "available": `[human]`,
`[decide]`, `[partial]` and gated rows are all unchecked boxes. This file is the filtered
list.

Take the first row your tier allows whose footprint is free. **Claim the PLAN id**
(`claim.sh take <you> L1.1 "<globs>"`), not the row letter — the row letter is only the
order. The plan row is the authority on what the work is.

Last curated: **2026-10-28 19:45 (opus).** Only rows whose `after` deps
have all landed are listed under "Available now"; everything else is under "Next", which is
NOT takeable yet. Before claiming, still run `git log origin/mydev --grep '[<id>]'` — rows
move fast once several agents run.

## Live footprints — stay out of these

| Who | Task | Footprint |
| --- | --- | --- |
| (none — session shut down by the user 2026-10-28 ~21:30) | | |

## Resume here (2026-10-28 shutdown)

- **L4.2** — WIP on branch `work/gemini-flash-3/L4.2` (dbedc18, builds, tests not re-run). Continue there; the brief also asks for the L5.1 wiring half in the same run.
- **Quota:** Z.ai GLM 5.3 exhausted (5-hour window), agy Gemini 4 Argon exhausted (~7 days). Gemini 3.8 Flash on agy (`gemini-flash*`) and swift (local) still work.
- **Next after L4.2:** L5.1 wiring → L4.3 key → L3.3 (swift) → L5.2 → L6.1/L6.2 ⚠️ heavy → L7.1 UI ⚠️ heavy → L7.2 → L8.1, L8.2, GATE.
- Old worktrees from dead sessions (`glm-L1.2`, `glm-2-L1.4`, `glm-3-L3.1`, `gemini-3-L3.2`, `gemini-L4.3`) are superseded — their work landed through takeovers. Safe to remove with `worktree.sh abandon <agent> <task> --force` once you have looked.

## Available now

### Heavy — opus, fable, glm, gemini

| N | Task |
| --- | --- |
| ~~A1~~ | ~~**L2.1**~~ landed (gemini, d94348f), reviewed by opus |
| ~~A2~~ | ~~**L4.4**~~ landed (gemini-3, d672b26), reviewed by opus |
| ~~A3~~ | ~~**L2.2**~~ ⚠️ Saved connections (no secrets on disk; secret via docker credential store, D-06). `[heavy \| F-0024, S-02]` — footprint `pkg/registry/connections/**`, `docs/registries.md` |

### Medium — also glm-flash, sonnet

| N | Task |
| --- | --- |
| ~~B1~~ | ~~**L1.1**~~ landed (glm, 87cd5ab); review fix rides with L1.2 |
| ~~B2~~ | ~~**L3.1**~~ Registries side panel. `[medium \| F-0001, F-0020, S-03, S-04]` — footprint `pkg/gui/registries_panel*.go`, `pkg/gui/gui.go`, `pkg/gui/arrangement.go`, `pkg/gui/keybindings.go`, `pkg/gui/views.go`, `pkg/config/app_config.go`, `pkg/i18n/english.go` |

### Light — anyone

| N | Task |
| --- | --- |
| ~~C1~~ | ~~**L3.4**~~ landed (swift, d186616), reviewed by opus |

## Next — NOT takeable until the named rows land

| Task | Waits on |
| --- | --- |
| L1.3 config blob + delete | L1.2 |
| L3.2 ⚠️ connection CRUD | L3.1, L2.2 |
| L3.3 status + i18n (light) | L3.1 |
| ~~L4.1~~ → L4.2 → L4.3 browsing, copy | L3.1, L1.2 |
| L5.1 → L5.2 tag details | L4.2, L1.3 |
| L6.1 → L6.2 deletes | L4.2, L4.4, L1.3 |
| L7.1 ⚠️ pull → L7.2 run | L5.1 |
| L8.1, L8.2, L8.GATE | everything above |

## Built but unticked — verify and tick, do not rebuild

None. Board is fresh.

## Waiting on the user — skip these

| Id | The question |
| --- | --- |
| (none) | |
