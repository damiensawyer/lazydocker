package dockerhub

import "time"

// Visibility of a Hub repository, mapped from the API's is_private flag.
type Visibility string

const (
	// VisibilityPublic: the repository is listable anonymously.
	VisibilityPublic Visibility = "PUBLIC"
	// VisibilityPrivate: only visible to the namespace's accounts.
	VisibilityPrivate Visibility = "PRIVATE"
)

// Repository is one image repository on Docker Hub. Docker Hub exposes no
// OCI catalog endpoint, so this — not /v2/_catalog — is how a namespace's
// repositories are listed.
type Repository struct {
	// Name is the repository name without its namespace ("nginx", not
	// "library/nginx").
	Name string
	// Namespace is the namespace (user or org) the repository belongs to.
	Namespace string
	// Visibility is Public unless the Hub reports the repository private.
	Visibility Visibility
}

// Tag is one tag of a Hub repository as reported by the Hub tags endpoint.
// Hub tags carry no platform/layer detail; that still comes from the OCI
// registry client (pkg/registry) once a tag is picked.
type Tag struct {
	// Name is the tag string, e.g. "1.25".
	Name string
	// Digest of the manifest the tag currently points at.
	Digest string
	// LastUpdated is when the tag was last pushed.
	LastUpdated time.Time
	// FullSize is the compressed size of the image in bytes.
	FullSize int64
}

// hubRepository is the wire form of one entry in a
// GET /v2/repositories/<ns>/ page.
type hubRepository struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	RepositoryType string `json:"repository_type"`
	IsPrivate      bool   `json:"is_private"`
}

// hubTag is the wire form of one entry in a
// GET /v2/repositories/<ns>/<repo>/tags page. Many more fields exist
// (images, tiers, ...); only the ones the model carries are decoded.
type hubTag struct {
	Name        string    `json:"name"`
	Digest      string    `json:"digest"`
	LastUpdated time.Time `json:"last_updated"`
	FullSize    int64     `json:"full_size"`
}

// hubPage is the common envelope of Hub list endpoints: a page of results
// plus the absolute URL of the next page (absent on the last one).
type hubPage[T any] struct {
	Results []T     `json:"results"`
	Next    *string `json:"next"`
}
