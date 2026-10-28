package gui

import (
	"time"

	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
)

// The Registries side panel is ONE SideListPanel over a sum type (D-08),
// because SideListPanel[T] is generic over a single T: the list shows
// registry connections at the top level and, once the L4 browsing tasks
// land, the repositories of the selected registry and the tags of the
// selected repository, with a drill stack in gui state deciding which level
// is rendered. This file defines that sum type. Only the registry kind is
// populated so far (L3.1); the repository/tag kinds get their fields when
// the drill-down tasks need them.

// registryItemKind discriminates the variant of a registryItem.
type registryItemKind int

const (
	// registryKindRegistry: the top level — a registry connection row.
	registryKindRegistry registryItemKind = iota
	// registryKindRepository: a repository of the drilled-into registry (L4).
	registryKindRepository
	// registryKindTag: a tag of the drilled-into repository (L4).
	registryKindTag
)

// registryItemStatus is a row's reachability verdict. It extends the
// registry package's Status (online/offline/unauthorized — what a Ping
// reports) with a "checking" state covering the window between a refresh
// kicking off and the ping landing.
type registryItemStatus string

const (
	registryItemStatusChecking     registryItemStatus = "checking"
	registryItemStatusOnline       registryItemStatus = registryItemStatus(registry.StatusOnline)
	registryItemStatusOffline      registryItemStatus = registryItemStatus(registry.StatusOffline)
	registryItemStatusUnauthorized registryItemStatus = registryItemStatus(registry.StatusUnauthorized)
)

// registryItem is one row of the Registries side panel.
//
// It deliberately holds no secret material: usernames are shown by `docker
// login` too and are not treated as secrets, but passwords and tokens never
// leave the credentials resolver / registry client boundary (see
// credentialFromSecret, which copies them field by field into the client's
// redacting Credential type and is the only place they pass through).
type registryItem struct {
	Kind registryItemKind

	// Registry-kind fields (the only populated kind in L3.1).

	// Host is the canonical registry host ("docker.io", "ghcr.io",
	// "localhost:5000"); already normalized by credentials.Discover.
	Host string

	// Username comes from the auth source; empty when it lives behind a
	// credential helper and is only resolved lazily for a ping.
	Username string

	// SourceFile is the auth file the entry was discovered in.
	SourceFile string

	// AutoDetected marks rows that were read from the user's docker/podman
	// config rather than saved as connections (saved connections land with
	// L2.2/L3.2; until then every row is auto-detected).
	AutoDetected bool

	// Status is the row's ping verdict; checking until one lands.
	Status registryItemStatus

	// Duration is the ping round-trip; meaningful once Status is final.
	Duration time.Duration

	// Err, when set, is a secret-free error message from the registry or
	// credentials packages (both already sanitize), shown in the summary.
	Err string
}

// credentialFromSecret maps a resolved credentials.Secret onto the registry
// client's Credential, field by field and nothing more: it is the single
// seam between the credential layer and the HTTP layer, so there is exactly
// one place to audit that no field is dropped, transformed, or logged
// (registry.Credential redacts itself in String/GoString/MarshalJSON).
func credentialFromSecret(sec credentials.Secret) *registry.Credential {
	return &registry.Credential{
		Username:      sec.Username,
		Password:      sec.Password,
		IdentityToken: sec.IdentityToken,
		RegistryToken: sec.RegistryToken,
	}
}
