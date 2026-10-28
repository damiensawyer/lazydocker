# The queue — what an agent may take right now

**Orchestrator writes this.** `- [ ]` in the plan does not mean "available": `[human]`,
`[decide]`, `[partial]` and gated rows are all unchecked boxes. This file is the filtered
list.

Take the first row your tier allows whose footprint is free. **Claim the PLAN id**
(`claim.sh take <you> L1.1 "<globs>"`), not the row letter — the row letter is only the
order. The plan row is the authority on what the work is.

Last curated: **2026-10-28 (opus, after the plan review).** Only rows whose `after` deps
have all landed are listed under "Available now"; everything else is under "Next", which is
NOT takeable yet. Before claiming, still run `git log origin/mydev --grep '[<id>]'` — rows
move fast once several agents run.

## Live footprints — stay out of these

| Who | Task | Footprint |
| --- | --- | --- |
| (none — no live claims) | | |

Locks change by the minute — `board.sh` beats this table.

## Available now

### Heavy — opus, fable, glm, gemini

| N | Task |
| --- | --- |
| A1 | **L2.1** ⚠️ Credential auto-detection (docker config + Podman auth file + helpers, lazy). `[heavy \| F-0005, S-02]` — footprint `pkg/registry/credentials/**` |
| A2 | **L4.4** Multi-select in side lists (generic `SideListPanel` change). `[heavy \| F-0012, S-04]` — footprint `pkg/gui/panels/**` |

### Medium — also glm-flash, sonnet

| N | Task |
| --- | --- |
| B1 | **L1.1** Registry client core: transport, basic + bearer auth, plain-http/custom CA, ping, first httptest fixture. `[medium \| F-0008, S-01, S-07]` — footprint `pkg/registry/*.go`, `pkg/registry/registrytest/**` |

### Light — anyone

| N | Task |
| --- | --- |
| C1 | **L3.4** Masked prompt (`createMaskedPromptPanel`, gocui `View.Mask`). `[light \| S-04]` — footprint `pkg/gui/confirmation_panel.go`, `pkg/gui/confirmation_panel_test.go` |

## Next — NOT takeable until the named rows land

| Task | Waits on |
| --- | --- |
| L1.2 catalog/tags/manifests · L1.4 Docker Hub · L1.5 ghcr | L1.1 |
| L1.3 config blob + delete | L1.2 |
| L2.2 ⚠️ saved connections | L2.1 |
| L3.1 Registries panel | L1.1, L2.1 |
| L3.2 ⚠️ connection CRUD | L3.1, L3.4, L2.2 |
| L3.3 status + i18n (light) | L3.1 |
| L4.1 → L4.2 → L4.3 browsing, copy | L3.1, L1.2 |
| L5.1 → L5.2 tag details | L4.2, L1.3 |
| L6.1 → L6.2 ⚠️-grade deletes | L4.2, L4.4, L1.3 |
| L7.1 ⚠️ pull → L7.2 run | L5.1, L2.1 |
| L8.1, L8.2, L8.GATE | everything above |

## Built but unticked — verify and tick, do not rebuild

None. Board is fresh.

## Waiting on the user — skip these

| Id | The question |
| --- | --- |
| (none) | |
