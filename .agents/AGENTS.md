# Agent rules for this repo

## What this fork is

A mashup: **lazydocker's UI** with **crtui's remote-registry browsing** merged in, the way
the same author merged `gh` into lazygit. crtui (`github.com/ksckaan1/crtui`) is the feature
reference; docs/REGISTRY_PLAN.md is the plan.

## Principle

NEVER copy full source repos. Port features:

1. **Port the client layer, not the TUI.** crtui's registry clients
   (`internal/infra/{registryclient,dockerhubclient,githubclient}`) are plain HTTP logic and
   port nearly as-is. crtui's Bubbletea/Lipgloss UI does NOT port — lazydocker is gocui;
   reimplement the interactions in lazydocker's idioms (side panels, menus, confirmation
   panels, `pkg/i18n` strings).
2. **Prefer what is already vendored.** `github.com/docker/cli` is in `vendor/` — use
   `cli/config` for credential parsing. Do not add new dependencies (resty, go-keyring, xdg)
   when stdlib or vendored code suffices; a new dep is its own reviewed commit.
3. **Exclude:** crtui's update-check client, its figlet/nav UI toys, tests against live
   registries.

## Go rules

- **All Go commands need `GOFLAGS=-mod=vendor`** — deps are vendored (including the
  `jesseduffield/gocui` fork and the Docker SDK). `GOFLAGS=-mod=vendor go test ./...`
- Dependency changes: `go mod tidy && go mod vendor`, own commit, claimed in `files:`.

## Git commits

When you're an LLM making a git commit, always write detailed notes about the changes in the
description and the summary and identify yourself, saying these changes were made by [your
model name]. Mechanically, the board enforces this: commit through
`.agents/lazydocker/board/commit.sh`, which requires `[<task-id>]`, `Agent: <id>` and
`Co-Authored-By: <exact model string from agents.tsv>`.

## Where things live

- `.agents/lazydocker/board/README.md` — the coordination protocol. Start there.
- `docs/REGISTRY_PLAN.md` + `docs/features.yaml` — the plan and feature catalogue.
- `CLAUDE.md` — build & test commands (GOFLAGS note lives there too).
