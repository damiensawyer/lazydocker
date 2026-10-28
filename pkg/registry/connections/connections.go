// Package connections manages saved remote container registry connections for
// lazydocker.
//
// # Storage and Security Model (D-06, S-02, F-0024)
//
// Non-secret connection metadata is persisted in `registries.yml` inside the
// configuration directory supplied by the caller (typically
// `config.AppConfig.ConfigDir`, never a hardcoded `~/.config/lazydocker` path).
// The file is written atomically via a temporary file in the same directory
// with permissions `0600` (`rw-------`) before renaming into place.
//
// `registries.yml` NEVER stores passwords, identity tokens, registry tokens, or
// any other secret material. When a connection is saved with a non-empty
// `credentials.Secret`, the secret is written through the user's configured
// Docker credential store (`configfile.GetCredentialsStore(host).Store(...)`),
// matching `docker login` behaviour, and the connection entry in
// `registries.yml` records `secret_owner: lazydocker`.
//
// If the Docker credential store already holds a credential for that registry
// that was not written by lazydocker (`secret_owner` is not `"lazydocker"`),
// saving a connection with a secret refuses with `ErrExternalCredentialExists`
// unless `Options.OverwriteExternal` is true (in which case the credential is
// overwritten in the store, but `secret_owner` is left empty so `Store.Delete`
// never erases it).
//
// When a connection is deleted via `Store.Delete`, its credential store entry
// is erased ONLY if `secret_owner` is `"lazydocker"`. Pre-existing credentials
// created outside lazydocker (for example via `docker login` or `podman login`)
// are left untouched.
//
// # Schema (`registries.yml`)
//
//	connections:
//	  - kind: registry          # required: "registry" | "dockerhub" | "ghcr"
//	    url: registry.local:5000 # required: registry URL or host[:port]
//	    username: alice          # optional: login username
//	    insecure: false          # optional: allow HTTP fallback / self-signed TLS
//	    secret_owner: lazydocker # optional: set when lazydocker wrote the secret
package connections

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	dchcreds "github.com/docker/docker-credential-helpers/credentials"
	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
	"gopkg.in/yaml.v3"
)

const (
	// FileName is the file name used for saved registry connections inside
	// lazydocker's configuration directory.
	FileName = "registries.yml"

	// FileMode is the permission mode enforced on registries.yml.
	FileMode os.FileMode = 0o600

	// SecretOwnerLazydocker is recorded in Connection.SecretOwner when
	// lazydocker writes a secret to the Docker credential store.
	SecretOwnerLazydocker = "lazydocker"

	defaultDockerHubURL = "docker.io"
	defaultGHCRURL      = "ghcr.io"
	dockerHubStoreKey   = "https://index.docker.io/v1/"
)

var (
	// ErrEmptyConfigDir is returned when Store is constructed without a ConfigDir.
	ErrEmptyConfigDir = errors.New("connections: config directory must not be empty")
	// ErrInvalidKind is returned when Connection.Kind is not one of registry, dockerhub, or ghcr.
	ErrInvalidKind = errors.New("connections: invalid connection kind (must be registry, dockerhub, or ghcr)")
	// ErrEmptyURL is returned when Connection.URL is empty or normalises to an empty host.
	ErrEmptyURL = errors.New("connections: connection url must not be empty")
	// ErrNotFound is returned when a requested connection does not exist in registries.yml.
	ErrNotFound = errors.New("connections: connection not found")
	// ErrAlreadyExists is returned by Create or Update when the target registry host already exists.
	ErrAlreadyExists = errors.New("connections: connection already exists")
	// ErrExternalCredentialExists is returned when saving a connection with a
	// non-empty secret for a host that already has a credential in the Docker
	// credential store not owned by lazydocker, and Options.OverwriteExternal is
	// false.
	ErrExternalCredentialExists = errors.New("connections: the host already has a docker login; it will be used as-is")
)

// Kind identifies the backend type of a saved registry connection.
type Kind string

const (
	// KindRegistry is a generic OCI Distribution / Docker Registry v2 endpoint.
	KindRegistry Kind = "registry"
	// KindDockerHub is Docker Hub (hub.docker.com / docker.io).
	KindDockerHub Kind = "dockerhub"
	// KindGHCR is GitHub Container Registry (ghcr.io).
	KindGHCR Kind = "ghcr"
)

// Valid reports whether k is one of the supported connection kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindRegistry, KindDockerHub, KindGHCR:
		return true
	default:
		return false
	}
}

// File is the top-level YAML structure persisted in registries.yml.
type File struct {
	Connections []Connection `yaml:"connections"`
}

// Connection represents a saved registry connection entry in registries.yml.
// It intentionally contains NO secret fields; secrets live exclusively in the
// user's Docker credential store (D-06).
type Connection struct {
	Kind        Kind   `yaml:"kind" json:"kind"`
	URL         string `yaml:"url" json:"url"`
	Username    string `yaml:"username,omitempty" json:"username,omitempty"`
	Insecure    bool   `yaml:"insecure,omitempty" json:"insecure,omitempty"`
	SecretOwner string `yaml:"secret_owner,omitempty" json:"secret_owner,omitempty"`
}

// Host returns the canonical normalised registry hostname (or host:port) for
// the connection using credentials.NormalizeRegistry.
func (c Connection) Host() string {
	return credentials.NormalizeRegistry(c.URL)
}

// Normalize returns a copy of c with trimmed strings and default URLs applied
// for KindDockerHub ("docker.io") and KindGHCR ("ghcr.io") when URL is blank.
func (c Connection) Normalize() Connection {
	out := Connection{
		Kind:        Kind(strings.ToLower(strings.TrimSpace(string(c.Kind)))),
		URL:         strings.TrimSpace(c.URL),
		Username:    strings.TrimSpace(c.Username),
		Insecure:    c.Insecure,
		SecretOwner: strings.TrimSpace(c.SecretOwner),
	}
	if out.URL == "" {
		switch out.Kind {
		case KindDockerHub:
			out.URL = defaultDockerHubURL
		case KindGHCR:
			out.URL = defaultGHCRURL
		}
	}
	return out
}

// Validate checks that the connection has a valid Kind and a non-empty URL that
// normalises to a valid registry host.
func (c Connection) Validate() error {
	norm := c.Normalize()
	if !norm.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidKind, c.Kind)
	}
	if norm.URL == "" || norm.Host() == "" {
		return ErrEmptyURL
	}
	return nil
}

// Options configures a saved-connections Store.
type Options struct {
	// ConfigDir is lazydocker's configuration directory where registries.yml
	// will be read and written (e.g. config.AppConfig.ConfigDir). Required.
	ConfigDir string

	// DockerConfigDir overrides $DOCKER_CONFIG (the directory containing
	// Docker's config.json) for credential store operations. When empty,
	// $DOCKER_CONFIG or ~/.docker is used.
	DockerConfigDir string

	// DiscoverOptions provides optional overrides (such as HomeDir,
	// RegistryAuthFile, XDGRuntimeDir) used by ResolveSecret.
	DiscoverOptions credentials.DiscoverOptions

	// OverwriteExternal controls whether saving a connection with a secret is
	// allowed to overwrite a pre-existing credential in the Docker credential
	// store that was not created by lazydocker (SecretOwner !=
	// SecretOwnerLazydocker). When false (the default), Create, Save, and
	// Update refuse with ErrExternalCredentialExists. When true, the existing
	// credential is overwritten in the store, but Connection.SecretOwner is
	// left empty so a later Delete never erases the user's pre-existing login.
	OverwriteExternal bool
}

// Store manages saved registry connections in registries.yml and coordinates
// secret persistence with the Docker credential store. It is safe for
// concurrent use within a single process.
type Store struct {
	mu                sync.Mutex
	configDir         string
	dockerConfigDir   string
	discoverOpts      credentials.DiscoverOptions
	overwriteExternal bool
}

// NewStore creates a Store that persists registries.yml inside configDir and
// uses the standard Docker configuration ($DOCKER_CONFIG or ~/.docker) for
// credential store operations.
func NewStore(configDir string) *Store {
	return NewStoreWithOptions(Options{ConfigDir: configDir})
}

// NewStoreWithOptions creates a Store with explicit Options (such as an
// injected DockerConfigDir for tests).
func NewStoreWithOptions(opts Options) *Store {
	discoverOpts := opts.DiscoverOptions
	if discoverOpts.DockerConfigDir == "" && opts.DockerConfigDir != "" {
		discoverOpts.DockerConfigDir = opts.DockerConfigDir
	}
	return &Store{
		configDir:         strings.TrimSpace(opts.ConfigDir),
		dockerConfigDir:   strings.TrimSpace(opts.DockerConfigDir),
		discoverOpts:      discoverOpts,
		overwriteExternal: opts.OverwriteExternal,
	}
}

// Path returns the full filesystem path to registries.yml.
func (s *Store) Path() string {
	return filepath.Join(s.configDir, FileName)
}

// List reads registries.yml and returns all saved connections. If
// registries.yml does not exist yet, List returns an empty slice and nil error
// without creating the file.
func (s *Store) List() ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadUnlocked()
}

// Load is an alias for List.
func (s *Store) Load() ([]Connection, error) {
	return s.List()
}

// Get returns the saved connection matching urlOrHost (compared by canonical
// normalised host or exact URL), or ErrNotFound if no matching connection is
// stored.
func (s *Store) Get(urlOrHost string) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	conns, err := s.loadUnlocked()
	if err != nil {
		return Connection{}, err
	}
	idx := findConnectionIndex(conns, urlOrHost)
	if idx < 0 {
		return Connection{}, ErrNotFound
	}
	return conns[idx], nil
}

// Create adds a new connection to registries.yml and, when secret is non-empty,
// writes the secret to the Docker credential store and sets
// SecretOwner = SecretOwnerLazydocker (unless overwriting a pre-existing
// external login with Options.OverwriteExternal, which leaves SecretOwner
// empty). It returns ErrAlreadyExists if a connection for the same registry
// host already exists, or ErrExternalCredentialExists if a pre-existing Docker
// login exists and OverwriteExternal is false.
func (s *Store) Create(conn Connection, secret credentials.Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureConfigDir(); err != nil {
		return err
	}
	conn, err := prepareConnection(conn, secret)
	if err != nil {
		return err
	}

	conns, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if findConnectionIndex(conns, conn.URL) >= 0 {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, conn.Host())
	}

	if !secret.IsEmpty() {
		owner, err := s.storeSecretUnlocked(conn, secret, false)
		if err != nil {
			return err
		}
		conn.SecretOwner = owner
	} else {
		conn.SecretOwner = ""
	}

	conns = append(conns, conn)
	return s.saveUnlocked(conns)
}

// Save creates or updates conn in registries.yml (matched by canonical
// normalised registry host).
//
// When secret is non-empty, Save writes the secret through the Docker
// credential store (configfile.GetCredentialsStore(host).Store) exactly as
// `docker login` would, and records `secret_owner: lazydocker` in
// registries.yml unless overwriting a pre-existing external login with
// Options.OverwriteExternal (which leaves SecretOwner empty). If a pre-existing
// external login exists and OverwriteExternal is false, Save refuses with
// ErrExternalCredentialExists and writes nothing.
//
// When secret is empty, Save does not touch the Docker credential store; if
// updating an existing connection in registries.yml, its prior SecretOwner is
// preserved unless explicitly set on conn.
func (s *Store) Save(conn Connection, secret credentials.Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureConfigDir(); err != nil {
		return err
	}
	conn, err := prepareConnection(conn, secret)
	if err != nil {
		return err
	}

	conns, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	idx := findConnectionIndex(conns, conn.URL)
	alreadyOwned := idx >= 0 && conns[idx].SecretOwner == SecretOwnerLazydocker

	if !secret.IsEmpty() {
		owner, err := s.storeSecretUnlocked(conn, secret, alreadyOwned)
		if err != nil {
			return err
		}
		conn.SecretOwner = owner
	} else if idx >= 0 {
		if conn.SecretOwner == "" {
			conn.SecretOwner = conns[idx].SecretOwner
		}
	} else {
		conn.SecretOwner = ""
	}

	if idx >= 0 {
		conns[idx] = conn
	} else {
		conns = append(conns, conn)
	}
	return s.saveUnlocked(conns)
}

// Update replaces an existing connection identified by previousURL with conn.
// If the connection's canonical host changes and the previous connection had
// SecretOwner == SecretOwnerLazydocker, the old host's credential store entry
// is erased.
func (s *Store) Update(previousURL string, conn Connection, secret credentials.Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureConfigDir(); err != nil {
		return err
	}
	conn, err := prepareConnection(conn, secret)
	if err != nil {
		return err
	}

	conns, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	idx := findConnectionIndex(conns, previousURL)
	if idx < 0 {
		return ErrNotFound
	}
	prev := conns[idx]

	newIdx := findConnectionIndex(conns, conn.URL)
	if newIdx >= 0 && newIdx != idx {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, conn.Host())
	}

	hostChanged := prev.Host() != conn.Host()
	alreadyOwned := !hostChanged && prev.SecretOwner == SecretOwnerLazydocker

	if !secret.IsEmpty() {
		owner, err := s.storeSecretUnlocked(conn, secret, alreadyOwned)
		if err != nil {
			return err
		}
		conn.SecretOwner = owner
		if hostChanged && prev.SecretOwner == SecretOwnerLazydocker {
			_ = s.eraseSecretUnlocked(prev)
		}
	} else if hostChanged {
		if prev.SecretOwner == SecretOwnerLazydocker {
			_ = s.eraseSecretUnlocked(prev)
		}
		conn.SecretOwner = ""
	} else if conn.SecretOwner == "" {
		conn.SecretOwner = prev.SecretOwner
	}

	conns[idx] = conn
	return s.saveUnlocked(conns)
}

// Delete removes the connection matching urlOrHost from registries.yml.
// It erases the credential from the Docker credential store ONLY when the
// connection's SecretOwner is SecretOwnerLazydocker ("lazydocker"), leaving any
// pre-existing credentials untouched.
func (s *Store) Delete(urlOrHost string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureConfigDir(); err != nil {
		return err
	}
	conns, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	idx := findConnectionIndex(conns, urlOrHost)
	if idx < 0 {
		return ErrNotFound
	}
	target := conns[idx]

	if target.SecretOwner == SecretOwnerLazydocker {
		if err := s.eraseSecretUnlocked(target); err != nil {
			return err
		}
	}

	conns = append(conns[:idx], conns[idx+1:]...)
	return s.saveUnlocked(conns)
}

// ResolveSecret resolves the credentials for conn from the configured Docker /
// Podman credential sources via pkg/registry/credentials.
func (s *Store) ResolveSecret(ctx context.Context, conn Connection) (credentials.Secret, error) {
	return credentials.ResolveWithOptions(ctx, conn.URL, s.discoverOptions())
}

func (s *Store) discoverOptions() credentials.DiscoverOptions {
	opts := s.discoverOpts
	if opts.DockerConfigDir == "" && s.dockerConfigDir != "" {
		opts.DockerConfigDir = s.dockerConfigDir
	}
	return opts
}

func (s *Store) ensureConfigDir() error {
	if s.configDir == "" {
		return ErrEmptyConfigDir
	}
	return nil
}

func prepareConnection(conn Connection, secret credentials.Secret) (Connection, error) {
	norm := conn.Normalize()
	if err := norm.Validate(); err != nil {
		return Connection{}, err
	}
	if norm.Username == "" && secret.Username != "" && secret.Username != "<token>" {
		norm.Username = strings.TrimSpace(secret.Username)
	}
	return norm, nil
}

func findConnectionIndex(conns []Connection, urlOrHost string) int {
	trimmed := strings.TrimSpace(urlOrHost)
	if trimmed == "" {
		return -1
	}
	targetHost := credentials.NormalizeRegistry(trimmed)
	for i, c := range conns {
		if c.URL == trimmed || (targetHost != "" && c.Host() == targetHost) {
			return i
		}
	}
	return -1
}

func (s *Store) loadUnlocked() ([]Connection, error) {
	if err := s.ensureConfigDir(); err != nil {
		return nil, err
	}
	path := s.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	var file File
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing %s: invalid yaml configuration", path)
	}

	out := make([]Connection, 0, len(file.Connections))
	for _, c := range file.Connections {
		out = append(out, c.Normalize())
	}
	return out, nil
}

func (s *Store) saveUnlocked(conns []Connection) (retErr error) {
	if err := s.ensureConfigDir(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.configDir, 0o700); err != nil {
		return fmt.Errorf("creating config dir %s: %w", s.configDir, err)
	}

	sanitized := make([]Connection, len(conns))
	for i, c := range conns {
		sanitized[i] = c.Normalize()
	}

	data, err := yaml.Marshal(File{Connections: sanitized})
	if err != nil {
		return fmt.Errorf("encoding %s: %w", FileName, err)
	}

	tmp, err := os.CreateTemp(s.configDir, FileName+".tmp.*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", FileName, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(FileMode); err != nil {
		return fmt.Errorf("setting mode on temp file for %s: %w", FileName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing temp file for %s: %w", FileName, err)
	}
	if err := tmp.Close(); err != nil {
		tmp = nil
		return fmt.Errorf("closing temp file for %s: %w", FileName, err)
	}
	tmp = nil

	target := s.Path()
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("renaming temp file to %s: %w", target, err)
	}
	if err := os.Chmod(target, FileMode); err != nil {
		return fmt.Errorf("enforcing mode 0600 on %s: %w", target, err)
	}
	return nil
}

func (s *Store) loadDockerConfigFile() (*configfile.ConfigFile, error) {
	dockerDir := s.resolveDockerConfigDir()
	if dockerDir == "" {
		return nil, errors.New("connections: unable to resolve docker config directory")
	}
	cf, err := config.Load(dockerDir)
	if err != nil {
		return nil, fmt.Errorf("loading docker config %s: invalid configuration", filepath.Join(dockerDir, config.ConfigFileName))
	}
	return cf, nil
}

func (s *Store) resolveDockerConfigDir() string {
	if s.dockerConfigDir != "" {
		return filepath.Clean(s.dockerConfigDir)
	}
	if s.discoverOpts.DockerConfigDir != "" {
		return filepath.Clean(s.discoverOpts.DockerConfigDir)
	}
	if envDir := os.Getenv(config.EnvOverrideConfigDir); envDir != "" {
		return filepath.Clean(envDir)
	}
	home := s.discoverOpts.HomeDir
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	if home == "" && runtime.GOOS != "windows" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(home), ".docker")
}

func (s *Store) storeSecretUnlocked(conn Connection, secret credentials.Secret, alreadyOwned bool) (string, error) {
	cf, err := s.loadDockerConfigFile()
	if err != nil {
		return "", err
	}

	key := resolveStoreKey(cf, conn.URL)
	if key == "" {
		return "", ErrEmptyURL
	}

	credStore := cf.GetCredentialsStore(key)
	existing, getErr := credStore.Get(key)
	if getErr != nil {
		if !dchcreds.IsErrCredentialsNotFound(getErr) && !dchcreds.IsErrCredentialsNotFoundMessage(getErr.Error()) {
			return "", sanitizeStoreError("reading", key, getErr)
		}
		existing = types.AuthConfig{}
	}

	hasExisting := hasAuthCredential(existing)
	owner := SecretOwnerLazydocker
	if hasExisting && !alreadyOwned {
		if !s.overwriteExternal {
			return "", fmt.Errorf("%w: %s", ErrExternalCredentialExists, conn.Host())
		}
		owner = ""
	}

	username := strings.TrimSpace(secret.Username)
	if username == "" {
		username = strings.TrimSpace(conn.Username)
	}

	authCfg := types.AuthConfig{
		ServerAddress: key,
		Username:      username,
		Password:      secret.Password,
		IdentityToken: secret.IdentityToken,
		RegistryToken: secret.RegistryToken,
	}

	// If a password/token is supplied without any username, store it as an
	// IdentityToken so both nativeStore (which requires a non-empty username or
	// IdentityToken) and fileStore (whose decodeAuth rejects empty usernames in
	// base64 "user:pass") handle it cleanly.
	if authCfg.Username == "" && authCfg.Password != "" && authCfg.IdentityToken == "" {
		authCfg.IdentityToken = authCfg.Password
		authCfg.Password = ""
	}

	if err := credStore.Store(authCfg); err != nil {
		return "", sanitizeStoreError("storing", key, err)
	}
	return owner, nil
}

func (s *Store) eraseSecretUnlocked(conn Connection) error {
	cf, err := s.loadDockerConfigFile()
	if err != nil {
		return err
	}

	key := resolveStoreKey(cf, conn.URL)
	if key == "" {
		return nil
	}

	if err := cf.GetCredentialsStore(key).Erase(key); err != nil {
		if !dchcreds.IsErrCredentialsNotFound(err) && !dchcreds.IsErrCredentialsNotFoundMessage(err.Error()) {
			return sanitizeStoreError("erasing", key, err)
		}
	}
	return nil
}

func hasAuthCredential(ac types.AuthConfig) bool {
	return ac.Username != "" ||
		ac.Password != "" ||
		ac.IdentityToken != "" ||
		ac.RegistryToken != "" ||
		ac.Auth != ""
}

func resolveStoreKey(cf *configfile.ConfigFile, rawURL string) string {
	normHost := credentials.NormalizeRegistry(rawURL)
	if normHost == "" {
		return ""
	}
	rawTrimmed := strings.TrimSpace(rawURL)
	if cf != nil {
		if key := matchCredHelperKey(cf.CredentialHelpers, rawTrimmed, normHost); key != "" {
			return key
		}
		if key := matchAuthConfigKey(cf.AuthConfigs, rawTrimmed, normHost); key != "" {
			return key
		}
	}
	if normHost == defaultDockerHubURL {
		return dockerHubStoreKey
	}
	return normHost
}

func matchCredHelperKey(helpers map[string]string, rawTrimmed, normHost string) string {
	if len(helpers) == 0 {
		return ""
	}
	if rawTrimmed != "" && strings.TrimSpace(helpers[rawTrimmed]) != "" {
		return rawTrimmed
	}
	if normHost == defaultDockerHubURL && strings.TrimSpace(helpers[dockerHubStoreKey]) != "" {
		return dockerHubStoreKey
	}
	if strings.TrimSpace(helpers[normHost]) != "" {
		return normHost
	}
	keys := make([]string, 0, len(helpers))
	for k := range helpers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.TrimSpace(helpers[k]) != "" && credentials.NormalizeRegistry(k) == normHost {
			return k
		}
	}
	return ""
}

func matchAuthConfigKey(auths map[string]types.AuthConfig, rawTrimmed, normHost string) string {
	if len(auths) == 0 {
		return ""
	}
	if rawTrimmed != "" {
		if _, ok := auths[rawTrimmed]; ok {
			return rawTrimmed
		}
	}
	if normHost == defaultDockerHubURL {
		if _, ok := auths[dockerHubStoreKey]; ok {
			return dockerHubStoreKey
		}
	}
	if _, ok := auths[normHost]; ok {
		return normHost
	}
	keys := make([]string, 0, len(auths))
	for k := range auths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if credentials.NormalizeRegistry(k) == normHost {
			return k
		}
	}
	return ""
}

func sanitizeStoreError(action, host string, err error) error {
	if dchcreds.IsCredentialsMissingServerURL(err) {
		return fmt.Errorf("%s credentials for %s: missing server URL", action, host)
	}
	if dchcreds.IsCredentialsMissingUsername(err) {
		return fmt.Errorf("%s credentials for %s: missing username", action, host)
	}
	return fmt.Errorf("%s credentials for %s: credential store operation failed", action, host)
}
