# SLATE — the current parallel work plan

Orchestrator writes this file.

## 2026-10-28 (rev 2, opus) — four streams, non-Anthropic agents first

The user asked for the work to favour `glm`, `gemini` and `swift` over `opus`. `opus`
orchestrates, reviews heavy work, and only takes a task when the others are blocked.

| Stream | Agent | Work | Footprint |
| --- | --- | --- | --- |
| **A** | `glm` (GLM 5.3) | L1.1 → L1.2 → L1.3 → L1.4/L1.5 — the registry clients | `pkg/registry/*.go`, `pkg/registry/registrytest/**`, later `pkg/registry/dockerhub/**`, `pkg/registry/ghcr/**` |
| **B** | `gemini` (Gemini 4 Argon) | L2.1 ⚠️ → L2.2 ⚠️ — credentials, then L4.4 multi-select | `pkg/registry/credentials/**`, then `pkg/gui/panels/**` |
| **C** | `swift` (Swift 1.5, local) | L3.4 masked prompt, then L3.3 once L3.1 lands | `pkg/gui/confirmation_panel*.go` |
| **R** | `opus` | review of A/B as they land; L3.1 if nobody heavy is free | — |

A and B share `pkg/registry/` as a parent but not a file: credentials live in their own
subpackage and expose a `Credential` type the client consumes through an interface defined
in L1.1 (`registry.Authenticator`). If B needs that interface before A lands it, B defines
the minimal struct in its own package and A adapts — do not edit each other's files.

### What would make this round a success

1. `GOFLAGS=-mod=vendor go test ./pkg/registry/...` green against in-process httptest
   registries (no network in tests).
2. `lazydocker` starts unchanged, with no config read or helper exec, for a user who never
   focuses the Registries panel.
3. The Registries panel lists auto-detected credentials with a live status.
