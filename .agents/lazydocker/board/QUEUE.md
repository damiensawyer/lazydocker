# The queue — what an agent may take right now

**Orchestrator writes this.** `- [ ]` in the plan does not mean "available": `[human]`,
`[decide]`, `[partial]` and gated rows are all unchecked boxes. This file is the filtered
list.

Take the first row your tier allows whose footprint is free. **Claim the PLAN id**
(`claim.sh take <you> L1.1 "<globs>"`), not the row letter — the row letter is only the
order. The plan row is the authority on what the work is.

Last curated: **2026-10-28 (glm-flash, board scaffold).** Everything is open; nothing has a
commit under its id yet. Before claiming, still run `git log origin/mydev --grep '[<id>]'` —
rows move fast once several agents run.

## Live footprints — stay out of these

| Who | Task | Footprint |
| --- | --- | --- |
| (none — no live claims) | | |

Locks change by the minute — `board.sh` beats this table.

## Available now

### Heavy (2) — opus, fable, glm

| N | Task |
| --- | --- |
| A1 | **L2.1** Credential resolution: Docker/Podman config + credential helpers. `[heavy \| F-0005, S-02]` |
| A2 | **L8.GATE** — blocked until L1–L7 land. Do not take early. |

### Medium (8) — also glm-flash, sonnet

| N | Task |
| --- | --- |
| B1 | **L1.1** Registry client: transport, auth token flow, ping. `[medium \| F-0008, S-01]` |
| B2 | **L1.2** Registry client: catalog, tag list, manifests. `[medium \| F-0008, F-0013, S-01]` |
| B3 | **L1.3** Registry client: config blob → tag details, delete tag. `[medium \| F-0015, F-0012, S-01]` |
| B4 | **L1.4** Docker Hub client. `[medium \| F-0007, S-01]` |
| B5 | **L1.5** ghcr.io client. `[medium \| F-0006, S-01]` |
| B6 | **L2.2** Saved connections + config schema. `[medium \| F-0024, S-02]` |
| B7 | **L3.1** Registries side panel: list, status, refresh. `[medium \| F-0001, S-03, S-04]` |
| B8 | **L7.1** Pull a remote tag into local images. `[medium \| F-0022, S-03]` |

### Light (2) — anyone

| N | Task |
| --- | --- |
| C1 | **L3.2** i18n strings for the registry panel. `[light \| S-06]` |
| C2 | **L4.3** Copy pull command keybinding. `[light \| F-0019, S-04]` |

## Built but unticked — verify and tick, do not rebuild

None. Board is fresh.

## Waiting on the user — skip these

| Id | The question |
| --- | --- |
| (none) | |
