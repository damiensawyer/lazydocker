package connections

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
)

func isolateTestEnv(t *testing.T) (configDir, dockerDir, homeDir string) {
	t.Helper()
	base := t.TempDir()
	configDir = filepath.Join(base, "lazydocker-config")
	dockerDir = filepath.Join(base, "docker-config")
	homeDir = filepath.Join(base, "home")

	for _, dir := range []string{configDir, dockerDir, homeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	t.Setenv("HOME", homeDir)
	t.Setenv("DOCKER_CONFIG", dockerDir)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, "xdg"))
	return configDir, dockerDir, homeDir
}

func basicAuthB64(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func installFakeStoreHelper(t *testing.T, name, scriptBody string) {
	t.Helper()
	binDir := t.TempDir()
	helperPath := filepath.Join(binDir, "docker-credential-"+name)
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\n"+scriptBody+"\n"), 0o755); err != nil {
		t.Fatalf("write fake helper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSaveAndLoadNeverWritesSecretsToYAMLAndEnforcesMode0600(t *testing.T) {
	configDir, dockerDir, _ := isolateTestEnv(t)
	store := NewStoreWithOptions(Options{
		ConfigDir:       configDir,
		DockerConfigDir: dockerDir,
	})

	// Before any save, List returns empty without creating registries.yml.
	initial, err := store.List()
	if err != nil {
		t.Fatalf("initial List failed: %v", err)
	}
	if len(initial) != 0 {
		t.Fatalf("expected 0 initial connections, got %d", len(initial))
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registries.yml should not exist before first write")
	}

	passSecret := "ultra-secret-password-alpha-98765"
	tokenSecret := "ghp_ultra_secret_identity_token_54321"

	err = store.Save(Connection{
		Kind:     KindRegistry,
		URL:      "https://registry.example.com:5000",
		Username: "alice",
		Insecure: true,
	}, credentials.Secret{
		Username: "alice",
		Password: passSecret,
	})
	if err != nil {
		t.Fatalf("Save registry connection failed: %v", err)
	}

	err = store.Save(Connection{
		Kind:     KindGHCR,
		URL:      "", // should default to ghcr.io
		Username: "octocat",
	}, credentials.Secret{
		IdentityToken: tokenSecret,
	})
	if err != nil {
		t.Fatalf("Save ghcr connection failed: %v", err)
	}

	err = store.Save(Connection{
		Kind:     KindDockerHub,
		URL:      "", // should default to docker.io
		Username: "hubuser",
	}, credentials.Secret{})
	if err != nil {
		t.Fatalf("Save dockerhub connection failed: %v", err)
	}

	// 1. Check file mode is strictly 0600.
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat registries.yml: %v", err)
	}
	if gotPerm := info.Mode().Perm(); gotPerm != 0o600 {
		t.Fatalf("registries.yml permissions = %#o, want 0600", gotPerm)
	}

	// Loosen permissions to 0644 and verify atomic re-save restores 0600.
	if err := os.Chmod(store.Path(), 0o644); err != nil {
		t.Fatalf("chmod 0644: %v", err)
	}
	if err := store.Save(Connection{
		Kind:     KindDockerHub,
		URL:      "docker.io",
		Username: "hubuser-updated",
	}, credentials.Secret{}); err != nil {
		t.Fatalf("re-save dockerhub failed: %v", err)
	}
	info, err = os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat registries.yml after re-save: %v", err)
	}
	if gotPerm := info.Mode().Perm(); gotPerm != 0o600 {
		t.Fatalf("registries.yml permissions after re-save = %#o, want 0600", gotPerm)
	}

	// 2. Assert raw YAML content never contains any secret or encoded secret.
	rawYAML, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read registries.yml: %v", err)
	}
	yamlText := string(rawYAML)
	for _, forbidden := range []string{
		passSecret,
		tokenSecret,
		basicAuthB64("alice", passSecret),
		base64.StdEncoding.EncodeToString([]byte(passSecret)),
		base64.StdEncoding.EncodeToString([]byte(tokenSecret)),
		"password:",
		"identity_token:",
		"registry_token:",
	} {
		if strings.Contains(yamlText, forbidden) {
			t.Fatalf("registries.yml leaked forbidden content %q:\n%s", forbidden, yamlText)
		}
	}

	// 3. Verify loaded connections and secret_owner tracking.
	conns, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(conns) != 3 {
		t.Fatalf("expected 3 connections, got %d: %+v", len(conns), conns)
	}

	if conns[0].Kind != KindRegistry || conns[0].URL != "https://registry.example.com:5000" ||
		conns[0].Username != "alice" || !conns[0].Insecure || conns[0].SecretOwner != SecretOwnerLazydocker {
		t.Errorf("unexpected conns[0]: %+v", conns[0])
	}
	if conns[1].Kind != KindGHCR || conns[1].URL != "ghcr.io" ||
		conns[1].Username != "octocat" || conns[1].SecretOwner != SecretOwnerLazydocker {
		t.Errorf("unexpected conns[1]: %+v", conns[1])
	}
	if conns[2].Kind != KindDockerHub || conns[2].URL != "docker.io" ||
		conns[2].Username != "hubuser-updated" || conns[2].SecretOwner != "" {
		t.Errorf("unexpected conns[2]: %+v", conns[2])
	}

	// 4. Verify secrets are resolvable from the Docker credential store.
	ctx := context.Background()
	regSec, err := store.ResolveSecret(ctx, conns[0])
	if err != nil {
		t.Fatalf("ResolveSecret(registry.example.com:5000) failed: %v", err)
	}
	if regSec.Username != "alice" || regSec.Password != passSecret {
		t.Fatalf("unexpected resolved secret for registry.example.com:5000: user=%q", regSec.Username)
	}

	ghcrSec, err := store.ResolveSecret(ctx, conns[1])
	if err != nil {
		t.Fatalf("ResolveSecret(ghcr.io) failed: %v", err)
	}
	if ghcrSec.IdentityToken != tokenSecret {
		t.Fatalf("unexpected resolved token for ghcr.io")
	}
}

func TestDeleteRemovesLazydockerSecretButLeavesPreExistingCredentialAlone(t *testing.T) {
	configDir, dockerDir, _ := isolateTestEnv(t)

	// Pre-populate Docker's config.json with two pre-existing credentials (as if from `docker login`).
	dockerCfgPath := filepath.Join(dockerDir, "config.json")
	preExistingJSON := fmt.Sprintf(`{
	"auths": {
		"preexisting.example.com": {
			"auth": %q
		},
		"other.example.com": {
			"auth": %q
		}
	}
}`, basicAuthB64("pre-user", "pre-pass-111"), basicAuthB64("other-user", "other-pass-222"))

	if err := os.WriteFile(dockerCfgPath, []byte(preExistingJSON), 0o600); err != nil {
		t.Fatalf("write pre-existing config.json: %v", err)
	}

	store := NewStoreWithOptions(Options{
		ConfigDir:       configDir,
		DockerConfigDir: dockerDir,
	})

	// 1. Save a connection for preexisting.example.com WITHOUT a new secret (so SecretOwner is "").
	if err := store.Save(Connection{
		Kind:     KindRegistry,
		URL:      "preexisting.example.com",
		Username: "pre-user",
	}, credentials.Secret{}); err != nil {
		t.Fatalf("Save preexisting connection failed: %v", err)
	}

	// 2. Save a connection for lazydocker.example.com WITH a secret (so SecretOwner is "lazydocker").
	if err := store.Save(Connection{
		Kind:     KindRegistry,
		URL:      "lazydocker.example.com",
		Username: "lazy-user",
	}, credentials.Secret{
		Username: "lazy-user",
		Password: "lazy-pass-333",
	}); err != nil {
		t.Fatalf("Save lazydocker connection failed: %v", err)
	}

	ctx := context.Background()

	// Both should resolve before Delete.
	if sec, err := credentials.Resolve(ctx, "lazydocker.example.com"); err != nil || sec.Password != "lazy-pass-333" {
		t.Fatalf("expected lazydocker.example.com credential before delete, err=%v", err)
	}
	if sec, err := credentials.Resolve(ctx, "preexisting.example.com"); err != nil || sec.Password != "pre-pass-111" {
		t.Fatalf("expected preexisting.example.com credential before delete, err=%v", err)
	}

	// 3. Delete lazydocker.example.com: secret WAS written by lazydocker, so it MUST be erased.
	if err := store.Delete("lazydocker.example.com"); err != nil {
		t.Fatalf("Delete(lazydocker.example.com) failed: %v", err)
	}
	if _, err := credentials.Resolve(ctx, "lazydocker.example.com"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("expected lazydocker-owned credential to be erased on Delete, got %v", err)
	}

	// 4. Delete preexisting.example.com: secret was NOT written by lazydocker, so it MUST remain intact!
	if err := store.Delete("preexisting.example.com"); err != nil {
		t.Fatalf("Delete(preexisting.example.com) failed: %v", err)
	}
	preSec, err := credentials.Resolve(ctx, "preexisting.example.com")
	if err != nil {
		t.Fatalf("pre-existing credential was wrongly removed on Delete: %v", err)
	}
	if preSec.Username != "pre-user" || preSec.Password != "pre-pass-111" {
		t.Fatalf("pre-existing credential altered: user=%q", preSec.Username)
	}

	// And other.example.com is also still intact.
	otherSec, err := credentials.Resolve(ctx, "other.example.com")
	if err != nil || otherSec.Username != "other-user" || otherSec.Password != "other-pass-222" {
		t.Fatalf("unrelated credential was altered: err=%v", err)
	}

	// Both connections are gone from registries.yml.
	remaining, err := store.List()
	if err != nil {
		t.Fatalf("List after deletes failed: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected 0 connections remaining, got %+v", remaining)
	}
}

func TestSaveAndDeleteWithNativeCredentialHelper(t *testing.T) {
	configDir, dockerDir, _ := isolateTestEnv(t)
	kvDir := filepath.Join(t.TempDir(), "kv")
	if err := os.MkdirAll(kvDir, 0o700); err != nil {
		t.Fatalf("mkdir kvDir: %v", err)
	}

	// Fake credential helper implementing store, get, erase against kvDir files.
	installFakeStoreHelper(t, "lazytest", fmt.Sprintf(`
action="$1"
kv=%q
keyfile() {
  printf '%%s' "$1" | tr '/:.' '___'
}
case "$action" in
  store)
    payload="$(cat)"
    server="$(printf '%%s' "$payload" | sed -n 's/.*"ServerURL":"\([^"]*\)".*/\1/p')"
    user="$(printf '%%s' "$payload" | sed -n 's/.*"Username":"\([^"]*\)".*/\1/p')"
    sec="$(printf '%%s' "$payload" | sed -n 's/.*"Secret":"\([^"]*\)".*/\1/p')"
    printf '{"Username":"%%s","Secret":"%%s"}\n' "$user" "$sec" > "$kv/$(keyfile "$server")"
    ;;
  get)
    read -r server
    f="$kv/$(keyfile "$server")"
    if [ -f "$f" ]; then
      cat "$f"
    else
      echo "credentials not found in native keychain"
      exit 1
    fi
    ;;
  erase)
    read -r server
    rm -f "$kv/$(keyfile "$server")"
    ;;
  *)
    exit 1
    ;;
esac
`, kvDir))

	// Configure Docker config.json to use credsStore: "lazytest" and pre-populate a non-lazydocker entry in the helper.
	if err := os.WriteFile(filepath.Join(kvDir, "ghcr_io"), []byte(`{"Username":"pre-ghcr","Secret":"pre-ghcr-token"}`), 0o600); err != nil {
		t.Fatalf("seed helper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{
	"credsStore": "lazytest",
	"auths": {
		"ghcr.io": {}
	}
}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	store := NewStoreWithOptions(Options{
		ConfigDir:       configDir,
		DockerConfigDir: dockerDir,
	})

	// Save a connection with a secret (lazydocker-owned) and one without (pre-existing).
	if err := store.Save(Connection{
		Kind:     KindRegistry,
		URL:      "registry.internal:5000",
		Username: "bob",
	}, credentials.Secret{
		Username: "bob",
		Password: "bob-helper-secret",
	}); err != nil {
		t.Fatalf("Save(registry.internal:5000) failed: %v", err)
	}

	if err := store.Save(Connection{
		Kind:     KindGHCR,
		URL:      "ghcr.io",
		Username: "pre-ghcr",
	}, credentials.Secret{}); err != nil {
		t.Fatalf("Save(ghcr.io) failed: %v", err)
	}

	ctx := context.Background()
	sec, err := store.ResolveSecret(ctx, Connection{URL: "registry.internal:5000"})
	if err != nil || sec.Username != "bob" || sec.Password != "bob-helper-secret" {
		t.Fatalf("ResolveSecret(registry.internal:5000) failed: err=%v", err)
	}

	// Delete ghcr.io (pre-existing) -> helper still has ghcr.io.
	if err := store.Delete("ghcr.io"); err != nil {
		t.Fatalf("Delete(ghcr.io) failed: %v", err)
	}
	ghcrSec, err := store.ResolveSecret(ctx, Connection{URL: "ghcr.io"})
	if err != nil || ghcrSec.Username != "pre-ghcr" || ghcrSec.Password != "pre-ghcr-token" {
		t.Fatalf("expected pre-existing ghcr.io helper credential to survive Delete, err=%v", err)
	}

	// Delete registry.internal:5000 (lazydocker-owned) -> erased from helper.
	if err := store.Delete("registry.internal:5000"); err != nil {
		t.Fatalf("Delete(registry.internal:5000) failed: %v", err)
	}
	if _, err := store.ResolveSecret(ctx, Connection{URL: "registry.internal:5000"}); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("expected registry.internal:5000 credential to be erased from helper, got %v", err)
	}
}

func TestCreateUpdateAndValidation(t *testing.T) {
	configDir, dockerDir, _ := isolateTestEnv(t)
	store := NewStoreWithOptions(Options{
		ConfigDir:       configDir,
		DockerConfigDir: dockerDir,
	})

	// Validation errors.
	if err := store.Create(Connection{Kind: "invalid", URL: "example.com"}, credentials.Secret{}); !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("expected ErrInvalidKind, got %v", err)
	}
	if err := store.Create(Connection{Kind: KindRegistry, URL: "   "}, credentials.Secret{}); !errors.Is(err, ErrEmptyURL) {
		t.Fatalf("expected ErrEmptyURL, got %v", err)
	}
	emptyDirStore := NewStore("")
	if err := emptyDirStore.Save(Connection{Kind: KindRegistry, URL: "example.com"}, credentials.Secret{}); !errors.Is(err, ErrEmptyConfigDir) {
		t.Fatalf("expected ErrEmptyConfigDir, got %v", err)
	}

	// Create and duplicate check.
	conn := Connection{
		Kind:     KindRegistry,
		URL:      "reg1.example.com:5000",
		Username: "user1",
	}
	if err := store.Create(conn, credentials.Secret{Username: "user1", Password: "pass1"}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if err := store.Create(conn, credentials.Secret{}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists on duplicate Create, got %v", err)
	}

	// Update metadata without changing secret preserves SecretOwner == "lazydocker".
	if err := store.Update("reg1.example.com:5000", Connection{
		Kind:     KindRegistry,
		URL:      "reg1.example.com:5000",
		Username: "user1",
		Insecure: true,
	}, credentials.Secret{}); err != nil {
		t.Fatalf("Update metadata failed: %v", err)
	}
	got, err := store.Get("reg1.example.com:5000")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Insecure || got.SecretOwner != SecretOwnerLazydocker {
		t.Fatalf("expected Insecure=true and SecretOwner=lazydocker after metadata Update, got %+v", got)
	}

	// Update host to reg2.example.com:5000 with a new secret erases old host and stores new host.
	if err := store.Update("reg1.example.com:5000", Connection{
		Kind:     KindRegistry,
		URL:      "reg2.example.com:5000",
		Username: "user2",
	}, credentials.Secret{Username: "user2", Password: "pass2"}); err != nil {
		t.Fatalf("Update host failed: %v", err)
	}

	ctx := context.Background()
	if _, err := credentials.Resolve(ctx, "reg1.example.com:5000"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("expected old host credential to be erased after host rename, got %v", err)
	}
	if sec, err := credentials.Resolve(ctx, "reg2.example.com:5000"); err != nil || sec.Password != "pass2" {
		t.Fatalf("expected new host credential after host rename, err=%v", err)
	}
}

func TestStoreHelperFailureDoesNotLeakSecret(t *testing.T) {
	configDir, dockerDir, _ := isolateTestEnv(t)
	sensitive := "TOP_SECRET_HELPER_STDIN_777"

	// Helper echoes stdin (which contains the secret JSON) to stdout and fails.
	installFakeStoreHelper(t, "lazytest", `
cat
exit 1
`)

	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{
	"credsStore": "lazytest"
}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	store := NewStoreWithOptions(Options{
		ConfigDir:       configDir,
		DockerConfigDir: dockerDir,
	})

	err := store.Save(Connection{
		Kind:     KindRegistry,
		URL:      "registry.example.com",
		Username: "alice",
	}, credentials.Secret{
		Username: "alice",
		Password: sensitive,
	})
	if err == nil {
		t.Fatalf("expected error when credential helper fails on Store")
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatalf("error message leaked secret from helper stdout: %v", err)
	}
}
