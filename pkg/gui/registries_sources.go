package gui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/jesseduffield/lazydocker/pkg/registry/connections"
	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
	"github.com/jesseduffield/lazydocker/pkg/registry/dockerhub"
	"github.com/jesseduffield/lazydocker/pkg/registry/ghcr"
)

// registryRepository is one image repository returned by a repository source.
type registryRepository struct {
	Name       string
	Visibility string
}

// repositorySource abstracts listing repositories across different registry kinds.
type repositorySource interface {
	ListRepositories(ctx context.Context, item *registryItem) ([]registryRepository, error)
}

// defaultRepositorySource implements repositorySource, selecting the appropriate
// client implementation (generic registry, Docker Hub, or ghcr) based on the registry kind.
type defaultRepositorySource struct {
	resolveCredential func(ctx context.Context, item *registryItem) (*registry.Credential, error)
	listGeneric       func(ctx context.Context, host string, opts registry.Options) ([]registryRepository, error)
	listDockerHub     func(ctx context.Context, opts dockerhub.Options, namespace string) ([]registryRepository, error)
	listGHCR          func(ctx context.Context, opts ghcr.Options, owner string) ([]registryRepository, error)
}

func registryKindForItem(item *registryItem) connections.Kind {
	if item == nil {
		return connections.KindRegistry
	}
	if !item.AutoDetected && item.Connection.Kind.Valid() {
		return item.Connection.Kind
	}
	canonical := credentials.NormalizeRegistry(item.Host)
	switch canonical {
	case "docker.io":
		return connections.KindDockerHub
	case "ghcr.io":
		return connections.KindGHCR
	default:
		return connections.KindRegistry
	}
}

func (s *defaultRepositorySource) ListRepositories(ctx context.Context, item *registryItem) ([]registryRepository, error) {
	if item == nil {
		return nil, errors.New("cannot list repositories for nil item")
	}

	var cred *registry.Credential
	if s.resolveCredential != nil {
		var err error
		cred, err = s.resolveCredential(ctx, item)
		if err != nil && !errors.Is(err, credentials.ErrNotFound) {
			// secret-free resolve error
		}
	}

	kind := registryKindForItem(item)
	switch kind {
	case connections.KindDockerHub:
		namespace := item.Username
		if namespace == "" && cred != nil && cred.Username != "" {
			namespace = cred.Username
		}
		if namespace == "" {
			namespace = "library"
		}
		hubOpts := dockerhub.Options{
			Namespace:  namespace,
			Credential: cred,
		}
		if s.listDockerHub != nil {
			return s.listDockerHub(ctx, hubOpts, namespace)
		}
		return defaultListDockerHub(ctx, hubOpts, namespace)

	case connections.KindGHCR:
		owner := item.Username
		if owner == "" && cred != nil && cred.Username != "" {
			owner = cred.Username
		}
		ghcrOpts := ghcr.Options{
			Credential: cred,
			Owner:      owner,
		}
		if s.listGHCR != nil {
			return s.listGHCR(ctx, ghcrOpts, owner)
		}
		return defaultListGHCR(ctx, ghcrOpts, owner)

	default: // connections.KindRegistry
		opts := registry.Options{
			Credential: cred,
			Insecure:   !item.AutoDetected && item.Connection.Insecure,
		}
		if s.listGeneric != nil {
			return s.listGeneric(ctx, item.Host, opts)
		}
		return defaultListGeneric(ctx, item.Host, opts)
	}
}

func defaultListGeneric(ctx context.Context, host string, opts registry.Options) ([]registryRepository, error) {
	client, err := registry.NewClient(pingHostForRegistry(host), opts)
	if err != nil {
		return nil, err
	}
	repos, err := client.ListRepositories(ctx, 0)
	if err != nil {
		return nil, err
	}
	res := make([]registryRepository, len(repos))
	for i, r := range repos {
		res[i] = registryRepository{
			Name:       r.Name,
			Visibility: "",
		}
	}
	return res, nil
}

func defaultListDockerHub(ctx context.Context, opts dockerhub.Options, namespace string) ([]registryRepository, error) {
	client, err := dockerhub.New(opts)
	if err != nil {
		return nil, err
	}
	repos, err := client.ListRepositories(ctx, namespace)
	if err != nil {
		return nil, err
	}
	res := make([]registryRepository, len(repos))
	for i, r := range repos {
		res[i] = registryRepository{
			Name:       r.Name,
			Visibility: string(r.Visibility),
		}
	}
	return res, nil
}

func defaultListGHCR(ctx context.Context, opts ghcr.Options, owner string) ([]registryRepository, error) {
	client, err := ghcr.NewClient(opts)
	if err != nil {
		return nil, err
	}
	pkgs, err := client.ListPackages(ctx, owner)
	if err != nil {
		return nil, err
	}
	res := make([]registryRepository, len(pkgs))
	for i, p := range pkgs {
		res[i] = registryRepository{
			Name:       p.Name,
			Visibility: string(p.Visibility),
		}
	}
	return res, nil
}

func (gui *Gui) newDefaultRepositorySource() repositorySource {
	return &defaultRepositorySource{
		resolveCredential: gui.resolveCredentialForItem,
		listGeneric:       defaultListGeneric,
		listDockerHub:     defaultListDockerHub,
		listGHCR:          defaultListGHCR,
	}
}

func (gui *Gui) resolveCredentialForItem(ctx context.Context, item *registryItem) (*registry.Credential, error) {
	if item == nil {
		return nil, nil
	}
	var cred *registry.Credential
	var resolveErr error

	resolve := credentials.Resolve
	if !item.AutoDetected {
		store := gui.registriesStore()
		sec, err := store.ResolveSecret(ctx, item.Connection)
		if err == nil {
			return credentialFromSecret(sec), nil
		}
		if !errors.Is(err, credentials.ErrNotFound) {
			resolveErr = err
		}
	}

	sec, err := resolve(ctx, item.Host)
	if err == nil {
		cred = credentialFromSecret(sec)
	} else if !errors.Is(err, credentials.ErrNotFound) && resolveErr == nil {
		resolveErr = err
	}

	return cred, resolveErr
}

func (gui *Gui) getRepositorySource() repositorySource {
	gui.registriesState.mu.Lock()
	src := gui.registriesState.repoSource
	gui.registriesState.mu.Unlock()
	if src != nil {
		return src
	}
	return gui.newDefaultRepositorySource()
}

// registryTag is one image tag returned by a tag source (L4.2).
type registryTag struct {
	Name        string
	Digest      string
	LastUpdated time.Time
	FullSize    int64
}

// tagSource abstracts listing tags across different registry kinds.
type tagSource interface {
	ListTags(ctx context.Context, item *registryItem) ([]registryTag, error)
}

// defaultTagSource implements tagSource, selecting the appropriate
// client implementation (generic registry, Docker Hub, or ghcr) based on the registry kind.
type defaultTagSource struct {
	resolveCredential func(ctx context.Context, item *registryItem) (*registry.Credential, error)
	listGeneric       func(ctx context.Context, host, repository string, opts registry.Options) ([]registryTag, error)
	listDockerHub     func(ctx context.Context, opts dockerhub.Options, namespace, repository string) ([]registryTag, error)
	listGHCR          func(ctx context.Context, opts ghcr.Options, owner, pkg string) ([]registryTag, error)
}

func parseDockerHubRepo(repository, defaultNamespace string) (namespace, repo string) {
	if strings.Contains(repository, "/") {
		parts := strings.SplitN(repository, "/", 2)
		return parts[0], parts[1]
	}
	ns := defaultNamespace
	if ns == "" {
		ns = "library"
	}
	return ns, repository
}

func parseGHCRRepo(repository, defaultOwner string) (owner, pkg string) {
	if strings.Contains(repository, "/") {
		parts := strings.SplitN(repository, "/", 2)
		return parts[0], parts[1]
	}
	return defaultOwner, repository
}

func (s *defaultTagSource) ListTags(ctx context.Context, item *registryItem) ([]registryTag, error) {
	if item == nil {
		return nil, errors.New("cannot list tags for nil item")
	}

	var cred *registry.Credential
	if s.resolveCredential != nil {
		var err error
		cred, err = s.resolveCredential(ctx, item)
		if err != nil && !errors.Is(err, credentials.ErrNotFound) {
			// secret-free resolve error
		}
	}

	kind := registryKindForItem(item)
	switch kind {
	case connections.KindDockerHub:
		namespace, repo := parseDockerHubRepo(item.Repository, item.Username)
		if namespace == "" && cred != nil && cred.Username != "" {
			namespace = cred.Username
		}
		if namespace == "" {
			namespace = "library"
		}
		hubOpts := dockerhub.Options{
			Namespace:  namespace,
			Credential: cred,
		}
		if s.listDockerHub != nil {
			return s.listDockerHub(ctx, hubOpts, namespace, repo)
		}
		return defaultListDockerHubTags(ctx, hubOpts, namespace, repo)

	case connections.KindGHCR:
		owner := item.Username
		if owner == "" && cred != nil && cred.Username != "" {
			owner = cred.Username
		}
		owner, pkg := parseGHCRRepo(item.Repository, owner)
		ghcrOpts := ghcr.Options{
			Credential: cred,
			Owner:      owner,
		}
		if s.listGHCR != nil {
			return s.listGHCR(ctx, ghcrOpts, owner, pkg)
		}
		return defaultListGHCRTags(ctx, ghcrOpts, owner, pkg)

	default: // connections.KindRegistry
		opts := registry.Options{
			Credential: cred,
			Insecure:   !item.AutoDetected && item.Connection.Insecure,
		}
		if s.listGeneric != nil {
			return s.listGeneric(ctx, item.Host, item.Repository, opts)
		}
		return defaultListGenericTags(ctx, item.Host, item.Repository, opts)
	}
}

func defaultListGenericTags(ctx context.Context, host, repository string, opts registry.Options) ([]registryTag, error) {
	client, err := registry.NewClient(pingHostForRegistry(host), opts)
	if err != nil {
		return nil, err
	}
	tags, err := client.ListTags(ctx, repository, 0)
	if err != nil {
		return nil, err
	}
	res := make([]registryTag, len(tags))
	for i, t := range tags {
		res[i] = registryTag{
			Name: t,
		}
	}
	return res, nil
}

func defaultListDockerHubTags(ctx context.Context, opts dockerhub.Options, namespace, repository string) ([]registryTag, error) {
	client, err := dockerhub.New(opts)
	if err != nil {
		return nil, err
	}
	tags, err := client.ListTags(ctx, namespace, repository)
	if err != nil {
		return nil, err
	}
	res := make([]registryTag, len(tags))
	for i, t := range tags {
		res[i] = registryTag{
			Name:        t.Name,
			Digest:      t.Digest,
			LastUpdated: t.LastUpdated,
			FullSize:    t.FullSize,
		}
	}
	return res, nil
}

func defaultListGHCRTags(ctx context.Context, opts ghcr.Options, owner, pkg string) ([]registryTag, error) {
	client, err := ghcr.NewClient(opts)
	if err != nil {
		return nil, err
	}
	versions, err := client.ListVersions(ctx, owner, pkg)
	if err != nil {
		return nil, err
	}
	var res []registryTag
	seen := make(map[string]bool)
	for _, v := range versions {
		for _, tag := range v.Tags {
			if seen[tag] {
				continue
			}
			seen[tag] = true
			res = append(res, registryTag{
				Name:   tag,
				Digest: v.Digest,
			})
		}
	}
	return res, nil
}

func (gui *Gui) newDefaultTagSource() tagSource {
	return &defaultTagSource{
		resolveCredential: gui.resolveCredentialForItem,
		listGeneric:       defaultListGenericTags,
		listDockerHub:     defaultListDockerHubTags,
		listGHCR:          defaultListGHCRTags,
	}
}

func (gui *Gui) getTagSource() tagSource {
	gui.registriesState.mu.Lock()
	src := gui.registriesState.tagSource
	gui.registriesState.mu.Unlock()
	if src != nil {
		return src
	}
	return gui.newDefaultTagSource()
}
