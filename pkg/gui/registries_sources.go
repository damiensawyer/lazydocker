package gui

import (
	"context"
	"errors"

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
