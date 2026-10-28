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
| L0 | Board + plan scaffolding | **done** (L0.1, L0.2) |
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
- **Models** live in `pkg/registry/` itself (`Connection`, `Repository`, `Tag`, `Platform`,
  `Layer`, …). lazydocker has no `pkg/commands/models/` (that is lazygit's layout); its own
  models sit flat in `pkg/commands/*.go`. Wire types are **not** hand-rolled: use the
  already-vendored `opencontainers/image-spec/specs-go/v1` (manifest, index, image config),
  `opencontainers/go-digest`, and `distribution/reference` (ref parsing) (D-07).
- **Credentials** come from what is already vendored: `github.com/docker/cli/cli/config`
  parses `~/.docker/config.json` and resolves credential helpers through
  `docker-credential-helpers/client` — no resty, no go-keyring, no xdg (D-02, D-03). Podman's
  auth file is **not** read by `cli/config` on its own: it is resolved separately
  (`$REGISTRY_AUTH_FILE`, then `$XDG_RUNTIME_DIR/containers/auth.json`, then
  `~/.config/containers/auth.json`) and parsed with `configfile.LoadFromReader` (same format).
- **UI.** A `Registries` **side panel** joins Projects/Services/Containers/Images/Volumes/
  Networks — lazydocker's side panels are stacked `SideListPanel[T]`s, not tabs (S-04). It is
  ONE panel over a sum type (`*registry.Item`, kind = registry | repository | tag) plus a
  drill stack in gui state, because `SideListPanel[T]` is generic over a single `T` (D-08).
  Enter drills down, `Esc` walks back up, lazydocker's filter idiom works at each level. A
  tag's details render in the main view's tabs (`ContextState` main tabs, S-05): Summary,
  Layers, History, Config, Labels, RootFS. Mutations go through the existing confirmation
  panel.
- **Built here, because lazydocker does not have it** (verified against the tree
  2026-10-28): multi-select in a side list (L4.4), a masked prompt for secrets (L3.4),
  clipboard copy (L4.3), and image pull with progress (L7.1 — `pkg/commands` has no pull at
  all). Each is its own row; none hides inside a feature row.
- **Zero regression = zero side effects until asked.** The panel is visible by default (this
  fork exists for it; `registries.enabled: false` in config hides it), but nothing — no
  config read, no credential-helper exec (which can pop a keychain unlock dialog), no
  network — happens until the Registries panel is first focused (D-09). Every phase's review
  (see `REVIEW.md`) checks this first.
- **Security posture.** Credentials are read into memory, used, and dropped — never logged,
  never rendered, never written to any file lazydocker owns. Saving a connection writes the
  *non-secret* part (kind, URL, username) to `registries.yml` in lazydocker's `ConfigDir`,
  and the secret through the user's docker credential store exactly as `docker login`
  would (D-06). Credentials are only ever sent to the registry (or its token realm / its
  own API host, e.g. hub.docker.com, api.github.com) they belong to. That is why every
  credential row is ⚠️ `heavy`.
- **Remote deletes are by digest.** The OCI API deletes a *manifest*, so deleting tag `a`
  also deletes every other tag pointing at the same digest. The client reports the
  co-tagged set before any delete; the UI must show it in the confirmation. Docker Hub and
  ghcr do not support registry-API deletes — they delete through their own APIs (hub
  repository-tag delete, GitHub packages version delete), and plain `registry:2` has no
  repository delete at all (only per-manifest, and only with `REGISTRY_STORAGE_DELETE_ENABLED`).
- **Transport realities.** Plain-HTTP registries (`localhost:5000`, entries in the daemon's
  `insecure-registries`) and custom CAs (`/etc/docker/certs.d/<host>/ca.crt`) are first-class
  in L1.1, not an afterthought.
- **Async.** Registry loads run off the UI thread in a goroutine and land via `gui.g.Update`;
  each load carries the drill-stack generation it was issued for and is dropped if the user
  has since moved (S-03). Long-running pulls use the existing `pkg/tasks` manager so the
  main view streams progress.

## 4. Phases

### Phase L0 — scaffolding (done)

Copy the dadmono board machinery, retarget it to this repo (paths, trunk, `L` id prefix),
reset its state, pass all selftests, and write this plan.

- [x] **L0.1 Board and plan scaffolding.** `[medium | S-08]`
- [x] **L0.2 Plan review and corrections.** `[heavy | S-08 | after L0.1]`

### Phase L1 — registry clients (`pkg/registry/`)

Pure library work: no UI, no config wiring, fully `httptest`-tested. Anything here must be
usable from a `go test` with zero docker daemon and zero network.

L1.1 ports crtui's `registryclient` onto stdlib `net/http`: transport, basic + OCI bearer
auth with per-scope token caching and 401 retry, ping / registry-info, and the model types;
plain-HTTP registries (explicit `insecure` flag, and `localhost`/`127.0.0.0/8` defaulting to
http fallback as docker does) and a per-host custom CA (`/etc/docker/certs.d/<host>/ca.crt`,
dir injectable for tests); the bearer flow must refuse to send credentials to a token
realm on a different host unless that realm was named in the registry's own
`WWW-Authenticate` challenge (and never over plain http to a non-loopback host). The first
`httptest` OCI fixture (S-07) lives here. Wire types are the vendored `image-spec` v1 ones
(D-07). L1.2 adds `ListRepositories` (`/v2/_catalog` + `Link` pagination), `ListTags`, and
`GetTag` handling both image manifests and index manifests (fetch by digest). L1.3 adds
the config-blob fetch → tag details (env, entrypoint, cmd, workdir, user, labels, history,
rootfs, layers with sizes), `TagsSharingDigest` (every tag whose digest equals the target's
— the blast radius of a delete), and `DeleteManifest` returning a typed
`ErrDeleteUnsupported` on 405/`UNSUPPORTED`. L1.4 is the Docker Hub client: hub.docker.com
repository listing with pagination and PUBLIC/PRIVATE visibility, anonymous and token auth
(`/v2/users/login` → JWT, PAT accepted), `Validate`, and tag delete via the hub API
(`DELETE /v2/repositories/<ns>/<repo>/tags/<tag>/` — hub deletes a tag, not a digest) — no
catalog endpoint exists there, so this client IS the repo listing for Docker Hub. L1.5 is
the ghcr client: GitHub packages API listing (user vs org detection, pagination,
visibility) plus the ghcr.io OCI bearer flow, and delete via the packages API (resolve the
package version whose tags contain the target; deleting the version removes all its tags —
same blast-radius reporting as L1.3).

- [x] **L1.1 Registry client core.** `[medium | F-0008,S-01,S-07 | after L0.1]`
- [x] **L1.2 Catalog, tags, manifests.** `[medium | F-0008,F-0013,S-01 | after L1.1]`
- [x] **L1.3 Config blob and delete.** `[medium | F-0012,F-0015,F-0016,F-0017,F-0018,S-01 | after L1.2]`
- [x] **L1.4 Docker Hub client.** `[medium | F-0007,F-0012,S-01 | after L1.1]`
- [x] **L1.5 ghcr client.** `[medium | F-0006,F-0012,S-01 | after L1.1]`

### Phase L2 — credentials & saved connections

Security-sensitive. Read-only auto-detection first, explicit persistence second; both are
⚠️ rows and `heavy` because a leak here is unbounded harm. Neither depends on the HTTP
client — they produce `registry.Credential` values the client consumes.

L2.1 works through the vendored `docker/cli/config`: enumerate `~/.docker/config.json`
(honouring `$DOCKER_CONFIG`) auth entries plus the Podman auth file (resolution order in §3,
parsed with `configfile.LoadFromReader`), resolve each through credential helpers
(`credsStore` / `credHelpers`, via `docker-credential-helpers/client`) **lazily, per
registry, only when that registry is first used** — not at startup, because a helper may
block on a keychain prompt (D-09). Expose read-only `AuthSource` values (registry URL,
username, source file) to the UI; the secret stays inside the resolver. Auto-detected
sources are never editable and never re-saved. Tests use a temp `DOCKER_CONFIG` and a fake
`docker-credential-test` helper script on `PATH`. L2.2 adds the saved-connections store:
`registries.yml` under lazydocker's `ConfigDir` (`pkg/config/app_config.go`, never a
hardcoded `~/.config/lazydocker`), mode 0600, holding kind/URL/username/insecure only —
**never a secret**. Secrets are written through the docker credential store
(`configfile.GetCredentialsStore(host).Store`), exactly as `docker login` would (D-06), and
erased from it on connection delete only if lazydocker wrote them. A documented schema and
create/update/delete behind a small store API.

- [x] **L2.1 Credential auto-detection.** ⚠️ `[heavy | F-0005,S-02 | after L0.1]`
- [x] **L2.2 Saved connections.** ⚠️ `[heavy | F-0024,F-0002,F-0003,F-0004,S-02 | after L2.1]`

### Phase L3 — the Registries side panel

The front door. Everything downstream of it stays closed until this works.

L3.1 adds the Registries side panel (a new `SideListPanel[*registry.Item]` next to
Images/Volumes/Networks, D-08) — registry connections (saved + auto-detected merged,
auto-detected marked), async ping on refresh with status (online / offline / unauthorized)
and response duration, refresh re-fetches; `registries.enabled: false` hides it; nothing
is read or contacted until the panel first gets focus (D-09). L3.4 adds `createMaskedPromptPanel` next to
`createPromptPanel` (`pkg/gui/confirmation_panel.go`) — it sets the vendored gocui
`View.Mask = '*'` so typed characters render masked, and on confirm *and* on close resets
`Mask` to 0 and clears the view buffer so the next prompt neither masks nor shows the secret;
the buffer is never logged. Needed because the only prompt lazydocker has echoes its input. L3.2 adds the connection CRUD flows: add/edit as a short sequence of
prompts (kind via the menu panel, URL, username, then the masked secret), delete behind the
confirmation panel and never offered for auto-detected rows. L3.3 puts every user-facing
string through `pkg/i18n` and renders status strings and durations per the options line's
conventions.

- [x] **L3.1 Registries panel.** `[medium | F-0001,F-0020,S-03,S-04 | after L1.1 L2.1]`
- [x] **L3.4 Masked prompt input.** `[light | S-04 | after L0.1]`
- [x] **L3.2 Connection CRUD flows.** ⚠️ `[heavy | F-0002,F-0003,F-0004,S-04 | after L3.1 L3.4 L2.2]`
- [ ] **L3.3 Status rendering and i18n.** `[light | F-0001,F-0021,S-06 | after L3.1]`

### Phase L4 — repository & tag browsing

L4.1: enter on a registry and its repositories replace the panel list (panel title shows
the drill path); filter-as-you-type reuses lazydocker's filtering; `Esc` walks back up the
drill path; background loading never blocks the UI thread and stale results are dropped
(§3 Async). L4.2: enter on a repository and its tags list the same way; paginated sources
(Docker Hub, ghcr) load more as you scroll. L4.3: one keypress copies `docker pull <ref>`
for the selected tag. lazydocker has no clipboard code; use an OSC 52 escape written to the
terminal (no dependency, works over SSH), falling back to `wl-copy`/`xclip`/`pbcopy`/`clip`
through `OSCommand` — no new vendored dependency without its own D-03 commit. L4.4 adds
multi-select to `SideListPanel` (toggle with space, range-select, a selected-count in the
title, `SelectedItems()` falling back to the cursor item) — lazydocker has no multi-select
anywhere, and this is a change to the generic panel every side list uses, so it is heavy
and must leave every existing panel's behaviour unchanged.

- [ ] **L4.1 Repository drill-down.** `[medium | F-0009,S-04 | after L3.1]`
- [ ] **L4.2 Tag list.** `[medium | F-0010,S-03,S-04 | after L4.1 L1.2]`
- [ ] **L4.3 Copy pull command.** [partial] `[medium | F-0019,S-04,S-06 | after L4.2]`
- [x] **L4.4 Multi-select in side lists.** `[heavy | F-0012,S-04 | after L0.1]`

### Phase L5 — tag details in the main view

L5.1: enter on a tag and the main view renders the inspected tag in tabs — Summary
(platforms, total size), Layers (media type + size each), History, Config (env, entrypoint,
cmd, workdir, user), Labels, RootFS — with human-readable sizes. L5.2: index manifests
offer a platform submenu; the chosen platform's config and layers are fetched by digest and
rendered; the summary names the selection.

- [ ] **L5.1 Tag details.** [partial] `[medium | F-0014,F-0015,F-0016,F-0017,F-0018,S-05 | after L4.2 L1.3]`
- [ ] **L5.2 Platform picker.** `[medium | F-0013,S-05 | after L5.1]`

### Phase L6 — remote mutations

Destructive and irrevocable — `heavy`, independently reviewable, never batched with other
work in one commit.

L6.1: multi-select tags (L4.4), then for the selection compute the full blast radius —
every *other* tag sharing a selected tag's digest (`TagsSharingDigest`), or every tag on
the same ghcr package version — and list it in the confirmation; the user types `delete`
to confirm. Delete per digest/version (never twice for one digest) with per-item progress
and error reporting, refresh after; `ErrDeleteUnsupported` renders as a clean refusal.
L6.2: all-tags delete behind a stronger gate — type the repository name to confirm. On
`registry:2` there is no repository delete: it deletes every manifest and says the
repository name stays in the catalog until garbage collection; on Docker Hub it uses the
hub repository delete; on ghcr the package delete.

- [ ] **L6.1 Delete tags.** `[heavy | F-0012,S-03 | after L4.2 L4.4 L1.3]`
- [ ] **L6.2 Delete repository.** `[heavy | F-0011,S-03 | after L6.1]`

### Phase L7 — the mashup: pull and run from remote

Where the fork earns its existence over plain crtui: the remote world connects to
lazydocker's local one.

L7.1: lazydocker has no pull. Add `DockerCommand.PullImage` on the SDK's `ImagePull`, passing
the registry's credential as the base64 `RegistryAuth` (never on a command line, never
logged) — which is why it is ⚠️ heavy — and render the JSON progress stream (per-layer
status lines collapsed in place) as a `pkg/tasks` task in the main view; on completion the
Images panel refreshes and the image is in it. L7.2: pull-if-missing, then create via the
custom-command preset; the container lands in the Containers panel.

- [ ] **L7.1 Pull a remote tag.** [partial] ⚠️ `[heavy | F-0022,S-02,S-03 | after L5.1 L2.1]`
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
| D-09 | Lazy side effects: no registry config read, credential-helper exec, or network until the Registries panel is first focused | a credential helper can block on a keychain prompt; users who never open the panel must see stock lazydocker. |
| D-08 | One Registries side panel over a sum type (`*registry.Item`) with a drill stack | `SideListPanel[T]` is generic over one `T`; three panels would triple layout and keybinding churn. |
| D-07 | Use vendored `image-spec` v1 / `go-digest` / `distribution/reference` types instead of porting crtui's models | already audited and vendored; spec-correct media types and digest validation for free. |
| D-06 | Saved secrets go through the docker credential store (as `docker login`), `registries.yml` holds no secrets | a plaintext secrets file would be the weakest credential store on the machine; the user's credsStore is already trusted. Supersedes the plaintext half of the original L2.2. |
| D-01 | Port crtui's clients onto stdlib `net/http`, dropping resty | resty v3 is a beta dependency; stdlib covers it; fewer vendored deps to review. |
| D-02 | No go-keyring/xdg; credentials from vendored `docker/cli/config` + helper exec | auto-detected logins cover the real cases; a keyring service would be new trust surface for zero demonstrated need. |
| D-03 | New dependencies are their own reviewed commit | `vendor/` is part of the audit surface; mixing a dep bump into feature work hides it. |
| D-04 | Task ids prefixed `L` (lazydocker) | board id-prefix; `L1.1`-style ids are greppable in subjects. |
| D-05 | The fork's trunk is `mydev`; the board lands there | matches how this fork already works (lazygit fork lands on its own trunk the same way). |

## 21. Commit index

| task | commit | note |
| --- | --- | --- |
| L0.1 | ff09ecf | board scaffold + this plan + features.yaml, by glm-flash |
| L1.1 | (see git log --grep '[L1.1]') | pkg/registry core: transport, ping, basic+bearer auth, token cache, certs.d CAs, http fallback, both credential-refusal rules + registrytest fixture, by glm |
| L3.4 | (see git log --grep '[L3.4]') | createMaskedPromptPanel; mask reset + buffer clear on close/open of the confirmation panel, by swift |
| L2.1 | (see git log --grep '[L2.1]') | docker + podman credential auto-detection and lazy helper resolution, by gemini |
| L1.5 | (see git log --grep '[L1.5]') | pkg/registry/ghcr client: GitHub packages listing (user/org detection, pagination, visibility), version listing, resolve-then-delete + package delete, and ghcr.io registry client helper, by gemini-2 |
| L2.2 | (see git log --grep '[L2.2]') | saved-connections store (`registries.yml`, mode 0600, no secrets on disk), Docker credential store integration (`secret_owner: lazydocker`), schema docs + L2.1 review follow-ups; review fixes: refuse clobbering pre-existing docker logins (`ErrExternalCredentialExists` / `Options.OverwriteExternal`) and match Docker's own store keys (`https://index.docker.io/v1/` for Hub, existing `auths`/`credHelpers` keys), by gemini |
| L4.4 | (see git log --grep '[L4.4]') | opt-in multi-select in SideListPanel (toggle, range select, key persistence, title count, row markers, SelectedItems fallback), by gemini-3 |
| L1.2 | (see git log --grep '[L1.2]') | pkg/registry catalog, tags, manifests (RFC5988 Link pagination, OCI + Docker v2 manifests and multi-arch indexes, digest verification, GetManifestByDigest) + registrytest catalog/tags/manifests fixture + L1.1 credential-scoping review fix (supervised rescue of glm's partial work), by gemini-2 |
| L3.1 | (see git log --grep '[L3.1]') | Registries side panel (`SideListPanel[*registryItem]`), lazy first-focus discovery + async per-registry ping with generation counter, `registries.enabled` config, summary main tab, and i18n keys (rescued from glm-3), by gemini-3 |
| L1.4 | (see git log --grep '[L1.4]') | pkg/registry/dockerhub client: login JWT + PAT auth, paginated repo/tag listing, PUBLIC/PRIVATE visibility, Validate, tag + repo delete, and origin/scheme/redirect guards (rescued from glm-2 + reviewed/hardened), by gemini |
| L7.1 | (see git log --grep '[L7.1]') | pkg/commands PullImage (partial: commands layer): reference.ParseNormalizedNamed validation, auth ServerAddress/domain matching (including Docker Hub aliases), base64url RegistryAuth encoding with error scrubbing, ImagePull JSON stream decoding, and FoldPullProgress in-place layer folding, by gemini |
| L1.3 | (see git log --grep '[L1.3]') | pkg/registry tag details (config blob fetch + digest verification, index platform resolution, layers + total size), TagsSharingDigest, DeleteManifest (typed ErrDeleteUnsupported / 401 / 403 / 404) + registrytest blobs/redirect/delete fixture, by gemini-2; review fix: fallback to GET+sha256 in TagsSharingDigest when HEAD lacks Docker-Content-Digest or returns 405, and error if undeterminable, by gemini-flash-2 |
| L3.2 | (see git log --grep '[L3.2]') | connection add/edit/delete via menu + prompts + masked secret; saved/auto merge; started by gemini-3, finished by opus |
| L4.3 | (see git log --grep '[L4.3]') | pkg/commands clipboard copy via OSC 52 (tmux/screen passthrough) + native fallbacks, control-char refusal and size limits (partial: infrastructure), by gemini-flash |
| L5.1 | (see git log --grep '[L5.1]') | tag-detail renderers for main view tabs (Summary, Layers, History, Config, Labels, RootFS) and i18n keys (partial: rendering), by gemini-flash |
