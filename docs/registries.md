# Saved Registry Connections (`registries.yml`)

lazydocker stores saved remote container registry connections in a dedicated `registries.yml` file inside lazydocker's configuration directory (`AppConfig.ConfigDir`, such as `~/.config/lazydocker/registries.yml` on Linux or `~/Library/Application Support/lazydocker/registries.yml` on macOS, or `$CONFIG_DIR/registries.yml` when `CONFIG_DIR` is set).

## Security & Credential Storage (D-06)

- **No secrets in `registries.yml`:** `registries.yml` stores only non-secret connection metadata (`kind`, `url`, `username`, `insecure`, and `secret_owner`). Passwords, personal access tokens, identity tokens, and bearer tokens are **never** written to `registries.yml`.
- **Docker credential store integration:** When you save a connection with a password or token in lazydocker, the secret is written through your configured Docker credential store (`configfile.GetCredentialsStore(host).Store`), using `credHelpers`, `credsStore` (e.g. `secretservice`, `osxkeychain`, `wincred`, `pass`), or `$DOCKER_CONFIG/config.json` — identical to `docker login`.
- **Safe deletion (`secret_owner`):** When lazydocker writes a secret to the Docker credential store, it records `secret_owner: lazydocker` on that connection entry in `registries.yml`. Deleting a connection in lazydocker erases its credential store entry **only** if `secret_owner` is `lazydocker`. Pre-existing credentials created outside lazydocker (for example via `docker login` or `podman login`) are never deleted.
- **File permissions & atomic writes:** `registries.yml` is written atomically via a temporary file in `ConfigDir` with mode `0600` (`rw-------`) and renamed into place.

## Schema

```yaml
connections:
  - kind: registry
    url: https://registry.example.com:5000
    username: alice
    insecure: false
    secret_owner: lazydocker

  - kind: ghcr
    url: ghcr.io
    username: octocat
    secret_owner: lazydocker

  - kind: dockerhub
    url: docker.io
    username: hubuser
```

### Per-Connection Fields

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `kind` | `string` | Yes | Backend kind: `registry` (OCI Distribution / Docker Registry v2), `dockerhub` (Docker Hub), or `ghcr` (GitHub Container Registry). |
| `url` | `string` | Yes | Registry URL or `host[:port]` (defaults to `docker.io` for `dockerhub` and `ghcr.io` for `ghcr` when omitted on save). |
| `username` | `string` | No | Login username associated with the connection. |
| `insecure` | `bool` | No | When `true`, allows HTTP fallback and self-signed TLS certificates for the registry. |
| `secret_owner` | `string` | No | Set to `lazydocker` when lazydocker wrote the connection's secret to the Docker credential store. Omitted when no secret was written by lazydocker. |

## Auto-Detected Credentials

In addition to connections explicitly saved in `registries.yml`, lazydocker auto-detects existing logins from:

1. Docker client configuration (`$DOCKER_CONFIG/config.json`, or `~/.docker/config.json`)
2. Podman auth configuration (`$REGISTRY_AUTH_FILE`, then `$XDG_RUNTIME_DIR/containers/auth.json`, then `~/.config/containers/auth.json`)

Auto-detected entries are read-only and resolved lazily only when a registry is accessed.
