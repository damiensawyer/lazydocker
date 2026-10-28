package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	dockercreds "github.com/docker/cli/cli/config/credentials"
	"github.com/docker/docker-credential-helpers/client"
	dchcreds "github.com/docker/docker-credential-helpers/credentials"
)

const (
	remoteCredentialsPrefix = "docker-credential-" //nolint:gosec
	tokenUsername           = "<token>"
	redactedPlaceholder     = "[REDACTED]"
	dockerHubCanonical      = "docker.io"
	dockerHubLegacyAuthKey  = "https://index.docker.io/v1/"
)

// ErrNotFound is returned by Resolve when no credentials exist for the requested registry.
var ErrNotFound = errors.New("credentials not found")

// AuthSource describes an auto-detected registry credential entry without
// holding any secret material.
type AuthSource struct {
	Registry string
	Username string
	Source   string
}

// Secret holds resolved authentication material for a single registry.
// Its String, GoString, and Format implementations redact all fields so secrets
// cannot leak through fmt verbs or logging.
type Secret struct {
	Username      string
	Password      string
	IdentityToken string
	RegistryToken string
}

// String implements fmt.Stringer and redacts the secret.
func (s Secret) String() string {
	return redactedPlaceholder
}

// GoString implements fmt.GoStringer and redacts the secret.
func (s Secret) GoString() string {
	return "credentials.Secret{" + redactedPlaceholder + "}"
}

// Format implements fmt.Formatter so all formatting verbs (%v, %+v, %#v, %s, %q)
// redact the secret.
func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, s.GoString())
		return
	}
	_, _ = io.WriteString(f, s.String())
}

// MarshalJSON implements json.Marshaler and redacts the secret so accidental
// JSON serialization cannot leak credentials.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedPlaceholder)
}

// MarshalYAML implements yaml.Marshaler and redacts the secret so accidental
// YAML serialization cannot leak credentials.
func (s Secret) MarshalYAML() (interface{}, error) {
	return redactedPlaceholder, nil
}

// IsEmpty reports whether the Secret contains no authentication material.
func (s Secret) IsEmpty() bool {
	return s.Username == "" &&
		s.Password == "" &&
		s.IdentityToken == "" &&
		s.RegistryToken == ""
}

// DiscoverOptions configures the paths and environment overrides used when
// discovering or resolving Docker and Podman registry credentials.
type DiscoverOptions struct {
	// DockerConfigDir overrides $DOCKER_CONFIG (directory containing config.json).
	DockerConfigDir string
	// RegistryAuthFile overrides $REGISTRY_AUTH_FILE (Podman auth file path).
	RegistryAuthFile string
	// XDGRuntimeDir overrides $XDG_RUNTIME_DIR (used to locate containers/auth.json).
	XDGRuntimeDir string
	// HomeDir overrides the user home directory (~).
	HomeDir string
}

// NormalizeRegistry normalises a registry URL or hostname into a canonical
// lowercase host[:port], collapsing Docker Hub aliases (docker.io,
// index.docker.io, registry-1.docker.io, https://index.docker.io/v1/) to
// "docker.io".
func NormalizeRegistry(registry string) string {
	trimmed := strings.TrimSpace(registry)
	if trimmed == "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(dockercreds.ConvertToHostname(trimmed)))
	host = strings.TrimSuffix(host, ".")
	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return dockerHubCanonical
	default:
		return host
	}
}

// Discover reads the Docker configuration file and Podman auth file and returns
// all discovered registry authentication sources without executing any
// credential helper binaries. Usernames that require a credential helper are
// left empty and resolved lazily via Resolve.
//
// Each credential file source (Docker config.json and Podman auth.json) is
// loaded independently: if one file is malformed or unreadable, Discover still
// returns the AuthSource entries from the healthy file alongside a joined,
// sanitized non-secret error describing the failed source.
func Discover(opts DiscoverOptions) ([]AuthSource, error) {
	files, loadErr := loadConfigFiles(opts)

	byRegistry := make(map[string]AuthSource)
	var order []string

	for _, cf := range files {
		fileEntries := discoverFromConfigFile(cf)
		for _, entry := range fileEntries {
			existing, exists := byRegistry[entry.Registry]
			if !exists {
				byRegistry[entry.Registry] = entry
				order = append(order, entry.Registry)
			} else if existing.Username == "" && entry.Username != "" && existing.Source == entry.Source {
				existing.Username = entry.Username
				byRegistry[entry.Registry] = existing
			}
		}
	}

	sort.Strings(order)
	out := make([]AuthSource, 0, len(order))
	for _, reg := range order {
		out = append(out, byRegistry[reg])
	}
	return out, loadErr
}

// Resolve lazily resolves the credentials for a single registry using the
// current environment ($DOCKER_CONFIG, $REGISTRY_AUTH_FILE, $XDG_RUNTIME_DIR,
// $HOME). Credential helpers are executed only for the target registry.
func Resolve(ctx context.Context, registry string) (Secret, error) {
	return ResolveWithOptions(ctx, registry, DiscoverOptions{})
}

// ResolveWithOptions resolves the credentials for a single registry using the
// provided DiscoverOptions.
//
// Docker config.json and Podman auth.json are loaded independently: if one
// file is malformed, ResolveWithOptions still resolves credentials from the
// remaining healthy file and succeeds without error when found. If the target
// registry is not found in any healthy file, ErrNotFound is returned (joined
// with any non-secret config load error).
func ResolveWithOptions(ctx context.Context, registry string, opts DiscoverOptions) (Secret, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Secret{}, err
	}

	normReg := NormalizeRegistry(registry)
	if normReg == "" {
		return Secret{}, ErrNotFound
	}

	files, loadErr := loadConfigFiles(opts)

	rawTrimmed := strings.TrimSpace(registry)
	for _, cf := range files {
		sec, found, err := resolveFromConfigFile(ctx, cf, rawTrimmed, normReg)
		if err != nil {
			return Secret{}, err
		}
		if found {
			return sec, nil
		}
	}

	if loadErr != nil {
		return Secret{}, errors.Join(ErrNotFound, loadErr)
	}
	return Secret{}, ErrNotFound
}

func discoverFromConfigFile(cf *configfile.ConfigFile) []AuthSource {
	if cf == nil {
		return nil
	}

	byReg := make(map[string]AuthSource)

	// Collect keys from AuthConfigs in sorted order for determinism.
	authKeys := make([]string, 0, len(cf.AuthConfigs))
	for k := range cf.AuthConfigs {
		authKeys = append(authKeys, k)
	}
	sort.Strings(authKeys)

	for _, rawKey := range authKeys {
		norm := NormalizeRegistry(rawKey)
		if norm == "" {
			continue
		}
		ac := cf.AuthConfigs[rawKey]
		entry, exists := byReg[norm]
		if !exists {
			entry = AuthSource{
				Registry: norm,
				Username: ac.Username,
				Source:   cf.Filename,
			}
		} else if entry.Username == "" && ac.Username != "" {
			entry.Username = ac.Username
		}
		byReg[norm] = entry
	}

	// Collect keys from CredentialHelpers without executing any helper.
	helperKeys := make([]string, 0, len(cf.CredentialHelpers))
	for k := range cf.CredentialHelpers {
		helperKeys = append(helperKeys, k)
	}
	sort.Strings(helperKeys)

	for _, rawKey := range helperKeys {
		if strings.TrimSpace(cf.CredentialHelpers[rawKey]) == "" {
			continue
		}
		norm := NormalizeRegistry(rawKey)
		if norm == "" {
			continue
		}
		if _, exists := byReg[norm]; !exists {
			byReg[norm] = AuthSource{
				Registry: norm,
				Username: "",
				Source:   cf.Filename,
			}
		}
	}

	keys := make([]string, 0, len(byReg))
	for k := range byReg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	res := make([]AuthSource, 0, len(keys))
	for _, k := range keys {
		res = append(res, byReg[k])
	}
	return res
}

func resolveFromConfigFile(ctx context.Context, cf *configfile.ConfigFile, rawRegistry, normReg string) (Secret, bool, error) {
	if cf == nil {
		return Secret{}, false, nil
	}

	// 1. Registry-specific credential helper (credHelpers).
	if helper, helperKey := matchCredentialHelper(cf, rawRegistry, normReg); helper != "" {
		candidates := uniqueStrings([]string{helperKey, rawRegistry, normReg})
		if normReg == dockerHubCanonical {
			candidates = uniqueStrings(append(candidates, dockerHubLegacyAuthKey, "index.docker.io", "registry-1.docker.io"))
		}
		for _, serverURL := range candidates {
			sec, found, err := getFromHelper(ctx, helper, serverURL)
			if err != nil {
				return Secret{}, false, err
			}
			if found {
				return sec, true, nil
			}
		}
	}

	// 2. Default credential store (credsStore).
	if storeHelper := strings.TrimSpace(cf.CredentialsStore); storeHelper != "" {
		// Prefer the auths key(s) Docker itself recorded for this registry so we
		// only invoke the credential helper once for a known registry. Only fall
		// back to the alias list when no auths key in config.json matches.
		candidates := matchingAuthConfigKeys(cf, rawRegistry, normReg)
		if len(candidates) == 0 {
			candidates = []string{rawRegistry, normReg}
			if normReg == dockerHubCanonical {
				candidates = append(candidates, dockerHubLegacyAuthKey, "index.docker.io", "registry-1.docker.io")
			}
		}
		candidates = uniqueStrings(candidates)

		for _, serverURL := range candidates {
			sec, found, err := getFromHelper(ctx, storeHelper, serverURL)
			if err != nil {
				return Secret{}, false, err
			}
			if found {
				return sec, true, nil
			}
		}
	}

	// 3. Inline credentials in AuthConfigs.
	for _, k := range matchingAuthConfigKeys(cf, rawRegistry, normReg) {
		ac := cf.AuthConfigs[k]
		sec := Secret{
			Username:      ac.Username,
			Password:      ac.Password,
			IdentityToken: ac.IdentityToken,
			RegistryToken: ac.RegistryToken,
		}
		if !sec.IsEmpty() {
			return sec, true, nil
		}
	}

	return Secret{}, false, nil
}

func matchCredentialHelper(cf *configfile.ConfigFile, rawRegistry, normReg string) (helper string, matchedKey string) {
	if len(cf.CredentialHelpers) == 0 {
		return "", ""
	}
	if h := strings.TrimSpace(cf.CredentialHelpers[rawRegistry]); h != "" {
		return h, rawRegistry
	}
	if h := strings.TrimSpace(cf.CredentialHelpers[normReg]); h != "" {
		return h, normReg
	}
	keys := make([]string, 0, len(cf.CredentialHelpers))
	for k := range cf.CredentialHelpers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if NormalizeRegistry(k) == normReg {
			if h := strings.TrimSpace(cf.CredentialHelpers[k]); h != "" {
				return h, k
			}
		}
	}
	return "", ""
}

func matchingAuthConfigKeys(cf *configfile.ConfigFile, rawRegistry, normReg string) []string {
	if len(cf.AuthConfigs) == 0 {
		return nil
	}
	var exact []string
	var others []string
	for k := range cf.AuthConfigs {
		if NormalizeRegistry(k) != normReg {
			continue
		}
		if k == rawRegistry {
			exact = append(exact, k)
		} else {
			others = append(others, k)
		}
	}
	sort.Strings(others)
	return append(exact, others...)
}

type sanitizedProgram struct {
	ctx     context.Context
	name    string
	args    []string
	stdin   io.Reader
	execErr error
}

func (p *sanitizedProgram) Input(in io.Reader) {
	p.stdin = in
}

func (p *sanitizedProgram) Output() ([]byte, error) {
	cmd := exec.CommandContext(p.ctx, p.name, p.args...) //nolint:gosec
	cmd.Stdin = p.stdin
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		p.execErr = err
		trimmed := strings.TrimSpace(string(out))
		if dchcreds.IsErrCredentialsNotFoundMessage(trimmed) ||
			dchcreds.IsCredentialsMissingServerURLMessage(trimmed) ||
			dchcreds.IsCredentialsMissingUsernameMessage(trimmed) {
			return []byte(trimmed), err
		}
		// Never pass arbitrary helper stdout back to client.Get on error, because
		// client.Get embeds stdout directly into the returned error message.
		return nil, err
	}
	return out, nil
}

func getFromHelper(ctx context.Context, helperSuffix, serverURL string) (Secret, bool, error) {
	progName := remoteCredentialsPrefix + helperSuffix
	var lastProg *sanitizedProgram
	progFn := func(args ...string) client.Program {
		lastProg = &sanitizedProgram{
			ctx:  ctx,
			name: progName,
			args: args,
		}
		return lastProg
	}

	creds, err := client.Get(progFn, serverURL)
	if err != nil {
		if dchcreds.IsErrCredentialsNotFound(err) {
			return Secret{}, false, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Secret{}, false, fmt.Errorf("credential helper %q canceled: %w", progName, ctxErr)
		}
		if lastProg != nil && lastProg.execErr != nil {
			return Secret{}, false, fmt.Errorf("credential helper %q failed: %w", progName, sanitizeExecError(lastProg.execErr))
		}
		return Secret{}, false, fmt.Errorf("credential helper %q returned invalid response", progName)
	}
	if creds == nil {
		return Secret{}, false, nil
	}

	var sec Secret
	if creds.Username == tokenUsername {
		sec.IdentityToken = creds.Secret
	} else {
		sec.Username = creds.Username
		sec.Password = creds.Secret
	}
	if sec.IsEmpty() {
		return Secret{}, false, nil
	}
	return sec, true, nil
}

func sanitizeExecError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("exit status %d", exitErr.ExitCode())
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
	}
	var execError *exec.Error
	if errors.As(err, &execError) {
		return fmt.Errorf("%s: %w", execError.Name, execError.Err)
	}
	return errors.New("execution failed")
}

func loadConfigFiles(opts DiscoverOptions) ([]*configfile.ConfigFile, error) {
	var files []*configfile.ConfigFile
	var loadErrs []error

	dockerDir := resolveDockerConfigDir(opts)
	if dockerDir != "" {
		cf, err := config.Load(dockerDir)
		if err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("loading docker config %s: invalid configuration", filepath.Join(dockerDir, config.ConfigFileName)))
		} else if cf != nil && cf.ContainsAuth() {
			files = append(files, cf)
		}
	}

	podmanFile := resolvePodmanAuthPath(opts)
	if podmanFile != "" {
		cf, err := loadConfigFileFromPath(podmanFile)
		if err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("loading podman auth %s: invalid configuration", podmanFile))
		} else if cf != nil && cf.ContainsAuth() {
			files = append(files, cf)
		}
	}

	return files, errors.Join(loadErrs...)
}

func loadConfigFileFromPath(path string) (*configfile.ConfigFile, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	cf, err := config.LoadFromReader(f)
	if err != nil {
		return nil, err
	}
	cf.Filename = path
	return cf, nil
}

func resolveDockerConfigDir(opts DiscoverOptions) string {
	if opts.DockerConfigDir != "" {
		return filepath.Clean(opts.DockerConfigDir)
	}
	if envDir := os.Getenv(config.EnvOverrideConfigDir); envDir != "" {
		return filepath.Clean(envDir)
	}
	home := resolveHomeDir(opts.HomeDir)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".docker")
}

func resolvePodmanAuthPath(opts DiscoverOptions) string {
	if authFile := firstNonEmpty(opts.RegistryAuthFile, os.Getenv("REGISTRY_AUTH_FILE")); authFile != "" {
		return filepath.Clean(authFile)
	}
	if xdgRuntime := firstNonEmpty(opts.XDGRuntimeDir, os.Getenv("XDG_RUNTIME_DIR")); xdgRuntime != "" {
		candidate := filepath.Join(xdgRuntime, "containers", "auth.json")
		if fileExists(candidate) {
			return candidate
		}
	}
	if home := resolveHomeDir(opts.HomeDir); home != "" {
		candidate := filepath.Join(home, ".config", "containers", "auth.json")
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func resolveHomeDir(override string) string {
	if override != "" {
		return filepath.Clean(override)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Clean(home)
	}
	if runtime.GOOS != "windows" {
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			return filepath.Clean(u.HomeDir)
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
