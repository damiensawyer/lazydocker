package credentials

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateEnv(t *testing.T) (homeDir, dockerDir, xdgDir string) {
	t.Helper()
	base := t.TempDir()
	homeDir = filepath.Join(base, "home")
	dockerDir = filepath.Join(base, "docker")
	xdgDir = filepath.Join(base, "xdg")

	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(dockerDir, 0o700); err != nil {
		t.Fatalf("mkdir docker: %v", err)
	}
	if err := os.MkdirAll(xdgDir, 0o700); err != nil {
		t.Fatalf("mkdir xdg: %v", err)
	}

	t.Setenv("HOME", homeDir)
	t.Setenv("DOCKER_CONFIG", dockerDir)
	t.Setenv("XDG_RUNTIME_DIR", xdgDir)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	return homeDir, dockerDir, xdgDir
}

func basicAuthB64(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func installFakeHelper(t *testing.T, name, scriptBody string) string {
	t.Helper()
	binDir := t.TempDir()
	helperPath := filepath.Join(binDir, "docker-credential-"+name)
	writeFile(t, helperPath, "#!/bin/sh\n"+scriptBody+"\n", 0o755)
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	return helperPath
}

func TestSecretRedaction(t *testing.T) {
	sec := Secret{
		Username:      "alice-user",
		Password:      "super-secret-password-123",
		IdentityToken: "super-secret-id-token-456",
		RegistryToken: "super-secret-reg-token-789",
	}

	rendered := fmt.Sprintf("%v %+v %#v %s %q", sec, sec, sec, sec, sec)
	ptrRendered := fmt.Sprintf("%v %+v %#v", &sec, &sec, &sec)
	combined := rendered + " " + ptrRendered + " " + sec.String() + " " + sec.GoString()

	for _, sensitive := range []string{
		sec.Password,
		sec.IdentityToken,
		sec.RegistryToken,
	} {
		if strings.Contains(combined, sensitive) {
			t.Fatalf("secret value %q leaked in formatted output: %s", sensitive, combined)
		}
	}

	if !strings.Contains(sec.String(), "REDACTED") {
		t.Fatalf("expected String() to contain REDACTED, got %q", sec.String())
	}
	if !strings.Contains(sec.GoString(), "REDACTED") {
		t.Fatalf("expected GoString() to contain REDACTED, got %q", sec.GoString())
	}
}

func TestNormalizeRegistry(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"docker.io", "docker.io"},
		{"index.docker.io", "docker.io"},
		{"registry-1.docker.io", "docker.io"},
		{"https://index.docker.io/v1/", "docker.io"},
		{"https://index.docker.io/v1", "docker.io"},
		{"http://index.docker.io/v2/", "docker.io"},
		{"https://registry-1.docker.io/v2/", "docker.io"},
		{"registry.hub.docker.com", "docker.io"},
		{"https://ghcr.io/v2/", "ghcr.io"},
		{"GHCR.IO", "ghcr.io"},
		{"localhost:5000", "localhost:5000"},
		{"http://localhost:5000/v2/", "localhost:5000"},
		{"  ", ""},
	}

	for _, tc := range cases {
		if got := NormalizeRegistry(tc.in); got != tc.want {
			t.Errorf("NormalizeRegistry(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiscoverDoesNotExecuteHelper(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	markerFile := filepath.Join(t.TempDir(), "helper-was-executed")

	installFakeHelper(t, "lazytest", fmt.Sprintf(`
touch %q
cat << 'EOF'
{"ServerURL":"https://index.docker.io/v1/","Username":"helperuser","Secret":"helpersecret"}
EOF
`, markerFile))

	cfgJSON := fmt.Sprintf(`{
	"credsStore": "lazytest",
	"credHelpers": {
		"ghcr.io": "lazytest",
		"quay.io": "lazytest"
	},
	"auths": {
		"https://index.docker.io/v1/": {},
		"registry.example.com:5000": {
			"auth": %q
		}
	}
}`, basicAuthB64("inlineuser", "inlinepass"))

	dockerCfgPath := filepath.Join(dockerDir, "config.json")
	writeFile(t, dockerCfgPath, cfgJSON, 0o600)

	sources, err := Discover(DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if _, err := os.Stat(markerFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential helper was executed during Discover (marker file exists)")
	}

	want := map[string]AuthSource{
		"docker.io":                 {Registry: "docker.io", Username: "", Source: dockerCfgPath},
		"ghcr.io":                   {Registry: "ghcr.io", Username: "", Source: dockerCfgPath},
		"quay.io":                   {Registry: "quay.io", Username: "", Source: dockerCfgPath},
		"registry.example.com:5000": {Registry: "registry.example.com:5000", Username: "inlineuser", Source: dockerCfgPath},
	}

	if len(sources) != len(want) {
		t.Fatalf("got %d sources (%+v), want %d", len(sources), sources, len(want))
	}
	for _, s := range sources {
		w, ok := want[s.Registry]
		if !ok {
			t.Errorf("unexpected registry %q in %+v", s.Registry, sources)
			continue
		}
		if s != w {
			t.Errorf("for %q: got %+v, want %+v", s.Registry, s, w)
		}
	}
}

func TestResolveHelperLazyAndAliases(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	markerFile := filepath.Join(t.TempDir(), "helper-invoked")

	installFakeHelper(t, "lazytest", fmt.Sprintf(`
touch %q
read -r server
case "$server" in
  "https://index.docker.io/v1/"|"docker.io")
    echo '{"Username":"hubuser","Secret":"hubsecret-xyz"}'
    ;;
  "ghcr.io")
    echo '{"Username":"<token>","Secret":"ghp_identity_token_abc"}'
    ;;
  *)
    echo "credentials not found in native keychain"
    exit 1
    ;;
esac
`, markerFile))

	cfgJSON := `{
	"credsStore": "lazytest",
	"credHelpers": {
		"ghcr.io": "lazytest"
	},
	"auths": {
		"https://index.docker.io/v1/": {}
	}
}`
	writeFile(t, filepath.Join(dockerDir, "config.json"), cfgJSON, 0o600)

	// Before Resolve, helper has not run.
	if _, err := os.Stat(markerFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker file should not exist before Resolve")
	}

	ctx := context.Background()

	// All Docker Hub aliases should resolve to the same secret.
	for _, alias := range []string{
		"docker.io",
		"index.docker.io",
		"registry-1.docker.io",
		"https://index.docker.io/v1/",
	} {
		sec, err := Resolve(ctx, alias)
		if err != nil {
			t.Fatalf("Resolve(%q) failed: %v", alias, err)
		}
		if sec.Username != "hubuser" || sec.Password != "hubsecret-xyz" {
			t.Fatalf("Resolve(%q) got unexpected secret fields: user=%q", alias, sec.Username)
		}
	}

	if _, err := os.Stat(markerFile); err != nil {
		t.Fatalf("expected marker file to exist after Resolve: %v", err)
	}

	// Token username (<token>) maps to IdentityToken.
	ghcrSec, err := Resolve(ctx, "ghcr.io")
	if err != nil {
		t.Fatalf("Resolve(ghcr.io) failed: %v", err)
	}
	if ghcrSec.Username != "" || ghcrSec.IdentityToken != "ghp_identity_token_abc" {
		t.Fatalf("unexpected ghcr secret: user=%q hasToken=%v", ghcrSec.Username, ghcrSec.IdentityToken != "")
	}

	// Unknown registry returns ErrNotFound.
	if _, err := Resolve(ctx, "unknown.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown.example.com, got %v", err)
	}
}

func TestPodmanAuthResolutionOrder(t *testing.T) {
	homeDir, _, xdgDir := isolateEnv(t)

	// 1. Lowest priority: ~/.config/containers/auth.json
	homeAuthPath := filepath.Join(homeDir, ".config", "containers", "auth.json")
	writeFile(t, homeAuthPath, fmt.Sprintf(`{
	"auths": {
		"quay.io": {"auth": %q}
	}
}`, basicAuthB64("homeuser", "homepass")), 0o600)

	sources, err := Discover(DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover (home fallback) failed: %v", err)
	}
	if len(sources) != 1 || sources[0].Registry != "quay.io" || sources[0].Username != "homeuser" || sources[0].Source != homeAuthPath {
		t.Fatalf("unexpected sources from home auth.json: %+v", sources)
	}

	// 2. Middle priority: $XDG_RUNTIME_DIR/containers/auth.json overrides ~/.config/containers/auth.json
	xdgAuthPath := filepath.Join(xdgDir, "containers", "auth.json")
	writeFile(t, xdgAuthPath, fmt.Sprintf(`{
	"auths": {
		"quay.io": {"auth": %q}
	}
}`, basicAuthB64("xdguser", "xdgpass")), 0o600)

	sources, err = Discover(DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover (xdg) failed: %v", err)
	}
	if len(sources) != 1 || sources[0].Username != "xdguser" || sources[0].Source != xdgAuthPath {
		t.Fatalf("unexpected sources from XDG auth.json: %+v", sources)
	}

	// 3. Highest priority: $REGISTRY_AUTH_FILE overrides both XDG and home.
	explicitAuthPath := filepath.Join(t.TempDir(), "custom-auth.json")
	writeFile(t, explicitAuthPath, fmt.Sprintf(`{
	"auths": {
		"quay.io": {"auth": %q},
		"index.docker.io": {"auth": %q}
	}
}`, basicAuthB64("explicituser", "explicitpass"), basicAuthB64("podmanhub", "podmanhubpass")), 0o600)
	t.Setenv("REGISTRY_AUTH_FILE", explicitAuthPath)

	sources, err = Discover(DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover (REGISTRY_AUTH_FILE) failed: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %+v", sources)
	}
	if sources[0].Registry != "docker.io" || sources[0].Username != "podmanhub" || sources[0].Source != explicitAuthPath {
		t.Fatalf("unexpected first source: %+v", sources[0])
	}
	if sources[1].Registry != "quay.io" || sources[1].Username != "explicituser" || sources[1].Source != explicitAuthPath {
		t.Fatalf("unexpected second source: %+v", sources[1])
	}

	// Resolve via Podman auth file with Docker Hub alias normalisation.
	sec, err := Resolve(context.Background(), "registry-1.docker.io")
	if err != nil {
		t.Fatalf("Resolve from podman failed: %v", err)
	}
	if sec.Username != "podmanhub" || sec.Password != "podmanhubpass" {
		t.Fatalf("unexpected credentials resolved from podman")
	}
}

func TestHelperErrorSanitization(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	leakedSecret := "LEAKED_SUPER_SECRET_TOKEN_999"

	installFakeHelper(t, "lazytest", fmt.Sprintf(`
echo "fatal helper error: %s"
echo "stderr secret: %s" >&2
exit 1
`, leakedSecret, leakedSecret))

	writeFile(t, filepath.Join(dockerDir, "config.json"), `{
	"credHelpers": {
		"ghcr.io": "lazytest"
	}
}`, 0o600)

	_, err := Resolve(context.Background(), "ghcr.io")
	if err == nil {
		t.Fatalf("expected error from failing credential helper")
	}
	if strings.Contains(err.Error(), leakedSecret) {
		t.Fatalf("error message leaked helper output: %v", err)
	}
}

func TestHelperInvalidJSONSanitization(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	leakedSecret := "INVALID_JSON_SECRET_TOKEN_888"

	installFakeHelper(t, "lazytest", fmt.Sprintf(`
echo "malformed json output with %s"
exit 0
`, leakedSecret))

	writeFile(t, filepath.Join(dockerDir, "config.json"), `{
	"credHelpers": {
		"ghcr.io": "lazytest"
	}
}`, 0o600)

	_, err := Resolve(context.Background(), "ghcr.io")
	if err == nil {
		t.Fatalf("expected error from malformed helper JSON")
	}
	if strings.Contains(err.Error(), leakedSecret) {
		t.Fatalf("error message leaked invalid JSON payload: %v", err)
	}
}

func TestDockerAndPodmanMergeAndPrecedence(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	podmanPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("REGISTRY_AUTH_FILE", podmanPath)

	dockerCfgPath := filepath.Join(dockerDir, "config.json")
	writeFile(t, dockerCfgPath, fmt.Sprintf(`{
	"auths": {
		"https://index.docker.io/v1/": {"auth": %q},
		"ghcr.io": {"auth": %q}
	}
}`, basicAuthB64("dockerhub-user", "dockerhub-pass"), basicAuthB64("ghcr-user", "ghcr-pass")), 0o600)

	writeFile(t, podmanPath, fmt.Sprintf(`{
	"auths": {
		"docker.io": {"auth": %q},
		"quay.io": {"auth": %q}
	}
}`, basicAuthB64("podmanhub-user", "podmanhub-pass"), basicAuthB64("quay-user", "quay-pass")), 0o600)

	sources, err := Discover(DiscoverOptions{})
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if len(sources) != 3 {
		t.Fatalf("expected 3 merged sources, got %+v", sources)
	}

	// docker.io comes from Docker config (precedence over Podman).
	if sources[0].Registry != "docker.io" || sources[0].Username != "dockerhub-user" || sources[0].Source != dockerCfgPath {
		t.Errorf("unexpected docker.io source: %+v", sources[0])
	}
	if sources[1].Registry != "ghcr.io" || sources[1].Username != "ghcr-user" || sources[1].Source != dockerCfgPath {
		t.Errorf("unexpected ghcr.io source: %+v", sources[1])
	}
	if sources[2].Registry != "quay.io" || sources[2].Username != "quay-user" || sources[2].Source != podmanPath {
		t.Errorf("unexpected quay.io source: %+v", sources[2])
	}

	ctx := context.Background()
	hubSec, err := Resolve(ctx, "docker.io")
	if err != nil {
		t.Fatalf("Resolve(docker.io) failed: %v", err)
	}
	if hubSec.Username != "dockerhub-user" || hubSec.Password != "dockerhub-pass" {
		t.Errorf("expected docker config credentials to take precedence for docker.io")
	}

	quaySec, err := Resolve(ctx, "quay.io")
	if err != nil {
		t.Fatalf("Resolve(quay.io) failed: %v", err)
	}
	if quaySec.Username != "quay-user" || quaySec.Password != "quay-pass" {
		t.Errorf("expected podman credentials for quay.io")
	}
}

func TestResolveCanceledContextDoesNotExecuteHelper(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	markerFile := filepath.Join(t.TempDir(), "should-not-run")

	installFakeHelper(t, "lazytest", fmt.Sprintf(`
touch %q
echo '{"Username":"u","Secret":"s"}'
`, markerFile))

	writeFile(t, filepath.Join(dockerDir, "config.json"), `{
	"credHelpers": {
		"ghcr.io": "lazytest"
	}
}`, 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Resolve(ctx, "ghcr.io")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, statErr := os.Stat(markerFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("helper should not have been executed when context is already canceled")
	}
}

func TestIndependentConfigLoadingWhenOneSourceIsMalformed(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	podmanPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("REGISTRY_AUTH_FILE", podmanPath)

	dockerSecret := "MALFORMED_DOCKER_SECRET_111"
	dockerCfgPath := filepath.Join(dockerDir, "config.json")
	writeFile(t, dockerCfgPath, fmt.Sprintf(`{"auths": {"ghcr.io": {"auth": "%s"`, dockerSecret), 0o600)

	writeFile(t, podmanPath, fmt.Sprintf(`{
	"auths": {
		"quay.io": {"auth": %q}
	}
}`, basicAuthB64("podman-user", "podman-pass")), 0o600)

	// Discover should return the valid Podman entry alongside a non-secret error for Docker.
	sources, err := Discover(DiscoverOptions{})
	if err == nil {
		t.Fatalf("expected non-nil error when docker config.json is malformed")
	}
	if strings.Contains(err.Error(), dockerSecret) {
		t.Fatalf("error leaked secret from malformed docker config: %v", err)
	}
	if len(sources) != 1 || sources[0].Registry != "quay.io" || sources[0].Username != "podman-user" || sources[0].Source != podmanPath {
		t.Fatalf("expected Podman source despite malformed docker config, got %+v", sources)
	}

	// Resolve for quay.io should succeed without error from the healthy Podman file.
	sec, err := Resolve(context.Background(), "quay.io")
	if err != nil {
		t.Fatalf("Resolve(quay.io) failed when docker config was malformed: %v", err)
	}
	if sec.Username != "podman-user" || sec.Password != "podman-pass" {
		t.Fatalf("unexpected credentials from Podman: user=%q", sec.Username)
	}

	// Resolve for a missing registry should still match ErrNotFound and include the load error without secrets.
	_, err = Resolve(context.Background(), "missing.example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing.example.com, got %v", err)
	}
	if strings.Contains(err.Error(), dockerSecret) {
		t.Fatalf("error leaked secret from malformed docker config: %v", err)
	}

	// Now flip: valid Docker config + malformed Podman auth.json.
	podmanSecret := "MALFORMED_PODMAN_SECRET_222"
	writeFile(t, dockerCfgPath, fmt.Sprintf(`{
	"auths": {
		"ghcr.io": {"auth": %q}
	}
}`, basicAuthB64("docker-user", "docker-pass")), 0o600)
	writeFile(t, podmanPath, fmt.Sprintf(`{"auths": {"quay.io": {"auth": "%s"`, podmanSecret), 0o600)

	sources, err = Discover(DiscoverOptions{})
	if err == nil {
		t.Fatalf("expected non-nil error when podman auth.json is malformed")
	}
	if strings.Contains(err.Error(), podmanSecret) {
		t.Fatalf("error leaked secret from malformed podman auth.json: %v", err)
	}
	if len(sources) != 1 || sources[0].Registry != "ghcr.io" || sources[0].Username != "docker-user" || sources[0].Source != dockerCfgPath {
		t.Fatalf("expected Docker source despite malformed podman auth.json, got %+v", sources)
	}

	sec, err = Resolve(context.Background(), "ghcr.io")
	if err != nil {
		t.Fatalf("Resolve(ghcr.io) failed when podman auth.json was malformed: %v", err)
	}
	if sec.Username != "docker-user" || sec.Password != "docker-pass" {
		t.Fatalf("unexpected credentials from Docker: user=%q", sec.Username)
	}
}

func TestResolveCredsStoreExecCountWithAuthsKey(t *testing.T) {
	_, dockerDir, _ := isolateEnv(t)
	markerFile := filepath.Join(t.TempDir(), "helper-calls.log")

	installFakeHelper(t, "lazytest", fmt.Sprintf(`
read -r server
printf '%%s\n' "$server" >> %q
case "$server" in
  "https://index.docker.io/v1/")
    echo '{"Username":"hubuser","Secret":"hubsecret-123"}'
    ;;
  *)
    echo "credentials not found in native keychain"
    exit 1
    ;;
esac
`, markerFile))

	countHelperCalls := func() []string {
		t.Helper()
		data, err := os.ReadFile(markerFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("read marker file: %v", err)
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			return nil
		}
		return strings.Split(trimmed, "\n")
	}

	// 1. When auths records the key Docker itself wrote ("https://index.docker.io/v1/"),
	// Resolve("registry-1.docker.io") must invoke the helper ONLY ONCE with that exact key.
	writeFile(t, filepath.Join(dockerDir, "config.json"), `{
	"credsStore": "lazytest",
	"auths": {
		"https://index.docker.io/v1/": {}
	}
}`, 0o600)

	sec, err := Resolve(context.Background(), "registry-1.docker.io")
	if err != nil {
		t.Fatalf("Resolve(registry-1.docker.io) failed: %v", err)
	}
	if sec.Username != "hubuser" || sec.Password != "hubsecret-123" {
		t.Fatalf("unexpected secret: user=%q", sec.Username)
	}
	calls := countHelperCalls()
	if len(calls) != 1 || calls[0] != "https://index.docker.io/v1/" {
		t.Fatalf("expected exactly 1 helper exec for 'https://index.docker.io/v1/', got %d: %v", len(calls), calls)
	}

	// 2. When auths records a key for a registry whose credential is missing in the helper,
	// Resolve must still invoke the helper ONLY ONCE (for that recorded auths key) and
	// NOT fall back to the ~5 alias candidates.
	_ = os.Remove(markerFile)
	writeFile(t, filepath.Join(dockerDir, "config.json"), `{
	"credsStore": "lazytest",
	"auths": {
		"index.docker.io": {}
	}
}`, 0o600)

	_, err = Resolve(context.Background(), "docker.io")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound when recorded auths key is not in helper, got %v", err)
	}
	calls = countHelperCalls()
	if len(calls) != 1 || calls[0] != "index.docker.io" {
		t.Fatalf("expected exactly 1 helper exec for recorded auths key 'index.docker.io', got %d: %v", len(calls), calls)
	}

	// 3. Only when NO auths key matches does Resolve fall back to the alias list.
	_ = os.Remove(markerFile)
	writeFile(t, filepath.Join(dockerDir, "config.json"), `{
	"credsStore": "lazytest",
	"auths": {}
}`, 0o600)

	sec, err = Resolve(context.Background(), "docker.io")
	if err != nil {
		t.Fatalf("Resolve(docker.io) without auths key failed to fall back to aliases: %v", err)
	}
	if sec.Username != "hubuser" || sec.Password != "hubsecret-123" {
		t.Fatalf("unexpected secret on alias fallback: user=%q", sec.Username)
	}
	calls = countHelperCalls()
	if len(calls) < 2 {
		t.Fatalf("expected alias fallback when auths has no matching key, got %v", calls)
	}
}
