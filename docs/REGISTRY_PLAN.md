# lazydocker — the crtui registry mashup — build plan

**Status: ACTIVE.** This file is the coordination artifact for merging crtui's
remote-registry browsing into this lazydocker fork — the same mashup move as merging `gh`
into lazygit: crtui (`github.com/ksckaan1/crtui`) supplies the features; lazydocker's gocui
UI supplies the vehicle. It is deliberately empty of everything except what the board's
`plan-check.py` reads and what an agent needs to work: protocol, phases, system ids, decision
log, commit index.

`.agents/lazydocker/board/plan-check.py` reads this file and `docs/features.yaml` on every
commit that touches either, via `guard.sh`. Sections §5a and §21 must exist or the check
fails.

## 1. Protocol for every worker (LLM or human)

Read [`.agents/lazydocker/board/README.md`](../.agents/lazydocker/board/README.md) and claim
your task before your first edit. Take your tier or below, never above; your tier is in
[`.agents/lazydocker/board/agents.tsv`](../.agents/lazydocker/board/agents.tsv) and nowhere
else.

Task selection: the **first unchecked task in ID order** that is unclaimed, within your tier,
and whose file footprint does not overlap a live claim. `QUEUE.md` is the curated, tier-split
view of the same list — when it is current, take from it.

Every task line carries a `` `[tier | ids]` `` tag citing at least one `F-nnnn` feature from
[`features.yaml`](features.yaml) or an `S-nn` system id from §5a. No tag = the check fails.
`` ` | after L1.1 L1.2]` `` names the rows a task waits on.

Task ids are `L`-prefixed: `L1.1`, `L8.GATE`. ⚠️ marks the **credential/security path** —
those rows are `heavy`, always. Build commands: **every Go command needs
`GOFLAGS=-mod=vendor`** (deps are vendored; see `CLAUDE.md`). Tests never touch the network —
registry client work runs against the in-process `httptest` OCI fixtures of S-07.

## 2. Status dashboard

| Phase | What | Status |
| --- | --- | --- |
| L0 | Board + plan scaffolding | **done** (L0.1) |
| L1 | Registry clients (`pkg/registry/`) | open |
| L2 | Credentials & saved connections | open |
| L3 | Registries side panel | open |
| L4 | Repository & tag browsing | open |
| L5 | Tag details in the main view | open |
| L6 | Remote mutations (deletes) | open |
| L7 | The mashup: pull / run from remote | open |
| L8 | Polish & gate | open |

## 3. The shape of the merge

The lazygit/`gh` precedent, applied. That merge put GitHub's API in-repo
(`pkg/commands/github_commands/`, `pkg/commands/models/github_repository.go`), wired a new
surface into the gocui GUI, and kept `gh` itself out of the loop. Here `gh` has no equivalent
to shell out to — crtui is a TUI, not a CLI — so the features come in as **ported client
code + lazydocker-native UI**:

- **Port the client layer, not the TUI.** crtui's `internal/infra/{registryclient,
  dockerhubclient,githubclient}` is plain HTTP logic and ports almost as-is, onto stdlib
  `net/http` (D-01) in a new `pkg/registry/` package. crtui's Bubbletea/Lipgloss UI does not
  port — lazydocker is gocui; the interactions are re-expressed in lazydocker's idioms:
  side panels, list panels, the menu panel, confirmation panels, `pkg/i18n` strings.
- **Models** live in `pkg/commands/models/registry.go` (`RegistryConnection`, `Tag`,
  `Platform`, `Layer`, …), following the `github_repository.go` precedent.
- **Credentials** come from what is already vendored: `github.com/docker/cli/cli/config`
  parses `~/.docker/config.json` and Podman's `auth.json` and shells out to credential
  helpers — no resty, no go-keyring, no xdg (D-02, D-03).
- **UI.** A `Registries` tab joins the side panel (S-04). Drill-down happens inside the
  panel: registry → repositories → tags, with lazydocker's filter idiom at each level and
  `Esc` walking back up. A tag's details render in the main view's tab structure (S-05):
  Summary, Layers, History, Config, Labels, RootFS. Mutations go through the existing
  confirmation panel. Pulling a tag uses lazydocker's existing command/tasks machinery so
  progress lands in the main view and the image appears in the Images panel.
- **Zero regression.** With no registry config and no keypress into the new tab, the app is
  stock lazydocker. Every phase's review (see `REVIEW.md`) checks this first.
- **Security posture.** Credentials are read into memory, used, and dropped — never logged,
  never rendered, never written anywhere except the connections file on an explicit save
  (0600). That is why every credential row is ⚠️ `heavy`.

## 4. Phases

### Phase L0 — scaffolding (done)

Copy the dadmono board machinery, retarget it to this repo (paths, trunk, `L` id prefix),
reset its state, pass all selftests, and write this plan.

- [x] **L0.1 Board and plan scaffolding.** `[medium | S-08]`

### Phase L1 — registry clients (`pkg/registry/`)

Pure library work: no UI, no config wiring, fully `httptest`-tested. Anything here must be
usable from a `go test` with zero docker daemon and zero network.

L1.1 ports crtui's `registryclient` onto stdlib `net/http`: transport, basic + OCI bearer
auth with per-scope token caching and 401 retry, ping / registry-info, and the model types;
the first `httptest` OCI fixture (S-07) lives here. L1.2 adds `ListRepositories`
(`/v2/_catalog` + pagination), `ListTags`, and `GetTag` handling both image manifests and
index manifests (fetch by digest). L1.3 adds the config-blob fetch → tag details (env,
entrypoint, cmd, workdir, user, labels, history, rootfs, layers with sizes) and
`DeleteTag`. L1.4 is the Docker Hub client: hub.docker.com repository listing with
pagination and PUBLIC/PRIVATE visibility, anonymous and token auth, `Validate` — no catalog
endpoint exists there, so this client IS the repo listing for Docker Hub. L1.5 is the ghcr
client: GitHub packages API listing (user vs org detection, pagination, visibility) plus
the ghcr.io OCI bearer flow; a fine-grained token with `packages:read` lists repositories.

- [ ] **L1.1 Registry client core.** `[medium | F-0008,F-0021,S-01,S-07 | after L0.1]`
- [ ] **L1.2 Catalog, tags, manifests.** `[medium | F-0008,F-0013,S-01 | after L1.1]`
- [ ] **L1.3 Config blob and delete.** `[medium | F-0012,F-0015,F-0016,F-0017,F-0018,S-01 | after L1.2]`
- [ ] **L1.4 Docker Hub client.** `[medium | F-0007,S-01 | after L1.1]`
- [ ] **L1.5 ghcr client.** `[medium | F-0006,S-01 | after L1.1]`

### Phase L2 — credentials & saved connections

Security-sensitive. Read-only auto-detection first, explicit persistence second; both are
⚠️ rows and `heavy` because a leak here is unbounded harm.

L2.1 works through the vendored `docker/cli/config`: enumerate `~/.docker/config.json` and
`~/.config/containers/auth.json` auth entries, resolve each through credential helpers
(`docker-credential-*`) when configured, and expose read-only `AuthSource` values (registry
URL, username) to the UI; auto-detected sources are never editable and never re-saved. L2.2
adds `~/.config/lazydocker/registries.yml` (mode 0600) with a documented schema and
create/update/delete plumbing behind a small store API; passwords are written only on
explicit save.

- [ ] **L2.1 Credential auto-detection.** ⚠️ `[heavy | F-0005,S-02 | after L1.1]`
- [ ] **L2.2 Saved connections.** ⚠️ `[heavy | F-0024,F-0002,F-0003,F-0004,S-02 | after L1.1]`

### Phase L3 — the Registries side panel

The front door. Everything downstream of it stays closed until this works.

L3.1 adds a side-panel tab joining Services/Containers/Images — a list panel of registry
connections (saved + auto-detected merged, auto-detected marked), async ping on refresh
with status (online / offline / unauthorized) and response duration, `R` re-fetches. L3.2
adds the connection CRUD flows: add/edit via a popup form (lazydocker's menu/panel idioms),
delete behind the confirmation panel and never offered for auto-detected rows. L3.3 puts
every user-facing string through `pkg/i18n` and renders status strings and durations per
the options line's conventions.

- [ ] **L3.1 Registries panel.** `[medium | F-0001,F-0020,S-03,S-04 | after L2.1 L2.2]`
- [ ] **L3.2 Connection CRUD flows.** `[medium | F-0002,F-0003,F-0004,S-04 | after L3.1]`
- [ ] **L3.3 Status rendering and i18n.** `[light | F-0001,F-0021,S-06 | after L3.1]`

### Phase L4 — repository & tag browsing

L4.1: enter on a registry and its repositories replace the panel list (breadcrumb/title
shows where you are); filter-as-you-type reuses lazydocker's filtering; `Esc` walks back up
the drill path; background loading never blocks the UI thread. L4.2: enter on a repository
and its tags list the same way; paginated sources (Docker Hub, ghcr) load more as you
scroll. L4.3: one keypress copies `docker pull <ref>` for the selected tag (add clipboard
support to `OSCommand` if absent — lazygit has the precedent to copy).

- [ ] **L4.1 Repository drill-down.** `[medium | F-0009,S-04 | after L3.1]`
- [ ] **L4.2 Tag list.** `[medium | F-0010,S-03,S-04 | after L4.1]`
- [ ] **L4.3 Copy pull command.** `[light | F-0019,S-04,S-06 | after L4.2]`

### Phase L5 — tag details in the main view

L5.1: enter on a tag and the main view renders the inspected tag in tabs — Summary
(platforms, total size), Layers (media type + size each), History, Config (env, entrypoint,
cmd, workdir, user), Labels, RootFS — with human-readable sizes. L5.2: index manifests
offer a platform submenu; the chosen platform's config and layers are fetched by digest and
rendered; the summary names the selection.

- [ ] **L5.1 Tag details.** `[medium | F-0014,F-0015,F-0016,F-0017,F-0018,S-05 | after L4.2]`
- [ ] **L5.2 Platform picker.** `[medium | F-0013,S-05 | after L5.1]`

### Phase L6 — remote mutations

Destructive and irrevocable — `heavy`, independently reviewable, never batched with other
work in one commit.

L6.1: multi-select tags (lazydocker's select idiom), typed confirmation, resolve each tag's
digest, DELETE per tag with progress and per-tag error reporting, refresh after. L6.2:
all-tags delete behind a stronger gate — type the repository name to confirm — and a clean
refusal when the registry does not support deletion.

- [ ] **L6.1 Delete tags.** `[heavy | F-0012,S-03 | after L4.2]`
- [ ] **L6.2 Delete repository.** `[heavy | F-0011,S-03 | after L6.1]`

### Phase L7 — the mashup: pull and run from remote

Where the fork earns its existence over plain crtui: the remote world connects to
lazydocker's local one.

L7.1: one keypress runs `docker pull` through the existing commands layer with a progress
task in the main view; on completion the image is present in the Images panel. L7.2:
pull-if-missing, then create via the custom-command preset; the container lands in the
Containers panel.

- [ ] **L7.1 Pull a remote tag.** `[medium | F-0022,S-03 | after L5.1]`
- [ ] **L7.2 Create container from a remote tag.** `[medium | F-0023,S-03 | after L7.1]`

### Phase L8 — polish & gate

L8.1 sweeps every registry network operation into the status manager with durations; errors
surface once, in one place. L8.2 writes the README section and a docs page for the feature
and puts the keybindings into the cheatsheet. The GATE: review queue empty (see
`REVIEW.md`), `GOFLAGS=-mod=vendor go test ./...` green, the zero-regression check done in
a fresh environment, and the credential-leak audit walked item by item.

- [ ] **L8.1 Status feedback sweep.** `[medium | F-0021,S-03 | after L3.1 L4.1 L5.1 L6.1 L7.1]`
- [ ] **L8.2 Fork docs.** `[light | S-06 | after L7.2]`
- [ ] **L8.GATE Phase gate.** `[heavy | S-01,S-02,S-08 | after L8.1 L8.2]`

## 5a. System ids — engineering that serves every feature and no single one

| id | what it covers |
| --- | --- |
| `S-01` | `pkg/registry/` — the registry client layer: transport, auth, catalog/tags/manifests, config blobs, deletes; stdlib `net/http`; fully httptest-tested |
| `S-02` | Credential resolution — docker/podman config parsing, credential-helper exec, the saved-connections store; the rules that keep secrets out of logs and disks |
| `S-03` | Async registry state — gui state for drill-down paths, background loads, refresh, status-manager feedback |
| `S-04` | Side-panel conventions — SideListPanel usage, tab registration, keybindings, the filter idiom, menu/confirmation flows |
| `S-05` | Main-view detail tabs — the tab structure registry details render into, reusing the existing tabbed main view |
| `S-06` | i18n and docs conventions — every user-facing string via `pkg/i18n`; cheatsheet and README updates |
| `S-07` | Registry test fixtures — in-process `httptest` OCI registries (auth, catalog, tags, manifests, blobs) so no test touches a network |
| `S-08` | Coordination infrastructure — the board, hooks, this plan, features.yaml |

## 9. Decision log

Append at the top; take the next free `D-0NN` at commit time.

| id | decision | why |
| --- | --- | --- |
| D-01 | Port crtui's clients onto stdlib `net/http`, dropping resty | resty v3 is a beta dependency; stdlib covers it; fewer vendored deps to review. |
| D-02 | No go-keyring/xdg; credentials from vendored `docker/cli/config` + helper exec | auto-detected logins cover the real cases; a keyring service would be new trust surface for zero demonstrated need. |
| D-03 | New dependencies are their own reviewed commit | `vendor/` is part of the audit surface; mixing a dep bump into feature work hides it. |
| D-04 | Task ids prefixed `L` (lazydocker) | board id-prefix; `L1.1`-style ids are greppable in subjects. |
| D-05 | The fork's trunk is `mydev`; the board lands there | matches how this fork already works (lazygit fork lands on its own trunk the same way). |

## 21. Commit index

| task | commit | note |
| --- | --- | --- |
| L0.1 | this commit | board scaffold + this plan + features.yaml, by glm-flash |
