# SLATE — the current parallel work plan

Orchestrator writes this file.

## 2026-10-28 — one stream: the crtui registry port

The whole plan is one arc — bring crtui's remote-registry browsing into lazydocker — so the
default shape is **one agent working L1 → L2 → L3 in id order**, with a second agent only
when a footprint is genuinely disjoint.

| Stream | Work | Tier wanted | Footprint | Why it is independent |
| --- | --- | --- | --- | --- |
| **A** | L1.x registry clients | medium | `pkg/registry/**` | Pure library code; no UI, no config wiring. |
| **B** | L2.1–L2.2 credentials | heavy | `pkg/registry/credentials/**`, `pkg/config/**` | Starts only after L1.1 (transport) lands; security-sensitive, so heavy. |

A1 + B1 is the honest capacity of this codebase right now: `pkg/config/**` and
`pkg/registry/**` barely overlap, and everything else (L3+) waits on both.

### What would make this phase a success

1. `GOFLAGS=-mod=vendor go test ./pkg/registry/...` green against an in-process httptest
   registry (no network in tests).
2. `lazydocker` starts unchanged for a user with no registry config — zero regression.
3. The registries panel lists auto-detected Docker Hub credentials with a live status.
