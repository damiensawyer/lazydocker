// Tag details (config blob fetch and decoding into opencontainers/image-spec
// v1.Image, layer listing and total compressed size), co-tagged digest
// discovery (TagsSharingDigest — the blast radius of a delete), and manifest
// deletion by digest (DeleteManifest).

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// MediaTypeDockerImageConfig is the Docker v2 image configuration blob media
// type (the OCI counterpart is v1.MediaTypeImageConfig).
const MediaTypeDockerImageConfig = "application/vnd.docker.container.image.v1+json"

// acceptConfigBlob is the Accept header sent on GET /v2/<name>/blobs/<digest>
// when fetching an image configuration blob.
const acceptConfigBlob = v1.MediaTypeImageConfig + ", " + MediaTypeDockerImageConfig + ", application/octet-stream, application/json"

// maxConfigBlobBytes caps config blob bodies at 16 MiB (matching
// maxManifestBytes).
const maxConfigBlobBytes = 16 << 20

var (
	// ErrDeleteUnsupported is returned when a registry does not support
	// manifest deletion (HTTP 405 Method Not Allowed or an OCI error item
	// with code UNSUPPORTED, such as registry:2 without
	// REGISTRY_STORAGE_DELETE_ENABLED).
	ErrDeleteUnsupported = errors.New("registry: manifest delete is not supported")

	// ErrUnauthorized is matched by errors.Is on a 401 UNAUTHORIZED APIError.
	ErrUnauthorized = errors.New("registry: unauthorized")

	// ErrForbidden is matched by errors.Is on a 403 Forbidden / DENIED APIError.
	ErrForbidden = errors.New("registry: forbidden")

	// ErrNotFound is matched by errors.Is on a 404 Not Found / *_UNKNOWN APIError.
	ErrNotFound = errors.New("registry: not found")

	// ErrPlatformNotFound is returned by GetTagDetails when the caller selects
	// a platform that does not match any child descriptor of an image index.
	ErrPlatformNotFound = errors.New("registry: platform not found in image index")
)

// Is reports whether the APIError matches one of the package's typed sentinel
// errors (ErrDeleteUnsupported, ErrUnauthorized, ErrForbidden, ErrNotFound)
// by HTTP status code or OCI errors[].code.
func (e *APIError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrDeleteUnsupported:
		return e.StatusCode == http.StatusMethodNotAllowed || e.hasCode("UNSUPPORTED")
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized || e.hasCode("UNAUTHORIZED")
	case ErrForbidden:
		return e.StatusCode == http.StatusForbidden || e.hasCode("DENIED")
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound ||
			e.hasCode("NAME_UNKNOWN", "MANIFEST_UNKNOWN", "BLOB_UNKNOWN")
	default:
		return false
	}
}

func (e *APIError) hasCode(codes ...string) bool {
	for _, item := range e.Errors {
		for _, code := range codes {
			if strings.EqualFold(item.Code, code) {
				return true
			}
		}
	}
	return false
}

// Layer describes one compressed filesystem layer from an image manifest.
type Layer struct {
	MediaType string
	Digest    digest.Digest
	Size      int64
}

// TagDetails holds the inspected details of a tag: its manifest identity,
// available platforms, the selected platform, the decoded OCI image config
// (*v1.Image) plus convenient projections of its fields, the manifest's
// layers, and the total compressed size across layers.
type TagDetails struct {
	Repository string
	Name       string
	// Digest is the top-level manifest (or index) digest of the tag.
	Digest digest.Digest
	// ManifestDigest is the digest of the resolved image manifest (equal to
	// Digest for single-image tags; the chosen child manifest's digest when a
	// platform is selected from an index; empty when IsIndex is true and no
	// platform was selected).
	ManifestDigest digest.Digest
	MediaType      string
	IsIndex        bool

	// Platforms lists the available platforms: for an index manifest, the
	// platforms of its child manifests; for a single-image manifest, the
	// platform reported by its config blob (when present).
	Platforms []v1.Platform

	// Platform is the resolved platform of the inspected image manifest (nil
	// when the tag is an index and platform was nil).
	Platform *v1.Platform

	// ConfigDigest is the digest of the config blob (empty when no config blob
	// was fetched, i.e. an index called with a nil platform).
	ConfigDigest digest.Digest

	// Image is the decoded OCI image configuration blob (nil when the tag is
	// an index and platform was nil).
	Image *v1.Image

	// Projections from Image (all empty/zero when Image is nil):
	Env        []string
	Entrypoint []string
	Cmd        []string
	WorkingDir string
	User       string
	Labels     map[string]string
	History    []v1.History
	RootFS     v1.RootFS

	// Layers are the layer descriptors from the resolved image manifest, in
	// manifest order, and TotalSize is the sum of their compressed sizes in
	// bytes.
	Layers    []Layer
	TotalSize int64
}

// GetTagDetails fetches the details of tag in repository.
//
//   - For a single-image manifest, it fetches and verifies the config blob at
//     /v2/<repo>/blobs/<digest>, decodes it into a vendored v1.Image, and
//     extracts layers and total compressed size from the manifest.
//   - For an image index (or Docker manifest list) with platform == nil, it
//     returns the index's platform list (Platforms) with Image == nil and no
//     config blob fetch.
//   - For an image index with a non-nil platform, it selects the matching
//     child descriptor, fetches that child manifest by digest, and fetches and
//     decodes its config blob and layers.
func (c *Client) GetTagDetails(ctx context.Context, repository, tag string, platform *v1.Platform) (*TagDetails, error) {
	t, err := c.GetTag(ctx, repository, tag)
	if err != nil {
		return nil, err
	}
	return c.resolveTagDetails(ctx, t, platform)
}

func (c *Client) resolveTagDetails(ctx context.Context, t *Tag, platform *v1.Platform) (*TagDetails, error) {
	details := &TagDetails{
		Repository: t.Repository,
		Name:       t.Name,
		Digest:     t.Digest,
		MediaType:  t.MediaType,
		IsIndex:    t.IsIndex(),
	}

	var manifest *v1.Manifest
	if t.IsIndex() {
		details.Platforms = t.Platforms()
		if platform == nil {
			return details, nil
		}
		desc, ok := selectIndexDescriptor(t.Index.Manifests, *platform)
		if !ok {
			return nil, fmt.Errorf("%w: %s/%s", ErrPlatformNotFound, platform.OS, platform.Architecture)
		}
		childTag, err := c.GetManifestByDigest(ctx, t.Repository, desc.Digest)
		if err != nil {
			return nil, err
		}
		if childTag.Manifest == nil {
			return nil, fmt.Errorf("%w: index child %s is not an image manifest (%s)",
				ErrUnsupportedMediaType, desc.Digest, childTag.MediaType)
		}
		manifest = childTag.Manifest
		details.ManifestDigest = childTag.Digest
		if desc.Platform != nil {
			p := *desc.Platform
			details.Platform = &p
		}
	} else {
		if t.Manifest == nil {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, t.MediaType)
		}
		manifest = t.Manifest
		details.ManifestDigest = t.Digest
	}

	img, err := c.fetchConfigBlob(ctx, t.Repository, manifest.Config.Digest)
	if err != nil {
		return nil, err
	}

	details.ConfigDigest = manifest.Config.Digest
	details.Image = img
	details.Env = img.Config.Env
	details.Entrypoint = img.Config.Entrypoint
	details.Cmd = img.Config.Cmd
	details.WorkingDir = img.Config.WorkingDir
	details.User = img.Config.User
	details.Labels = img.Config.Labels
	details.History = img.History
	details.RootFS = img.RootFS

	if details.Platform == nil && (img.OS != "" || img.Architecture != "") {
		p := img.Platform
		details.Platform = &p
	}
	if !details.IsIndex && details.Platform != nil {
		details.Platforms = []v1.Platform{*details.Platform}
	}

	layers := make([]Layer, len(manifest.Layers))
	var totalSize int64
	for i, l := range manifest.Layers {
		layers[i] = Layer{
			MediaType: l.MediaType,
			Digest:    l.Digest,
			Size:      l.Size,
		}
		if l.Size > 0 {
			totalSize += l.Size
		}
	}
	details.Layers = layers
	details.TotalSize = totalSize

	return details, nil
}

// selectIndexDescriptor finds the child manifest descriptor matching want.
// Pass 1 looks for an exact match on OS, Architecture, Variant and OSVersion;
// when want omits Variant and OSVersion, pass 2 falls back to matching OS and
// Architecture alone (e.g. matching linux/arm64 against a linux/arm64/v8 entry).
func selectIndexDescriptor(manifests []v1.Descriptor, want v1.Platform) (v1.Descriptor, bool) {
	for _, m := range manifests {
		if m.Platform == nil {
			continue
		}
		if strings.EqualFold(m.Platform.OS, want.OS) &&
			strings.EqualFold(m.Platform.Architecture, want.Architecture) &&
			strings.EqualFold(m.Platform.Variant, want.Variant) &&
			m.Platform.OSVersion == want.OSVersion {
			return m, true
		}
	}
	if want.Variant == "" && want.OSVersion == "" {
		for _, m := range manifests {
			if m.Platform == nil {
				continue
			}
			if strings.EqualFold(m.Platform.OS, want.OS) &&
				strings.EqualFold(m.Platform.Architecture, want.Architecture) {
				return m, true
			}
		}
	}
	return v1.Descriptor{}, false
}

// fetchConfigBlob fetches /v2/<repo>/blobs/<dgst> (following redirects via the
// client's HTTPClient, whose authTransport never attaches credentials to a
// foreign redirect host), caps the response body at maxConfigBlobBytes,
// verifies the body's digest against dgst, and decodes it as a v1.Image.
func (c *Client) fetchConfigBlob(ctx context.Context, repository string, dgst digest.Digest) (*v1.Image, error) {
	if err := validRepositoryName(repository); err != nil {
		return nil, err
	}
	if err := dgst.Validate(); err != nil {
		return nil, fmt.Errorf("registry: invalid config digest %q: %w", dgst, err)
	}

	resp, err := c.doURL(ctx, http.MethodGet,
		c.urlFor("/v2/"+repository+"/blobs/"+dgst.String()),
		http.Header{"Accept": {acceptConfigBlob}})
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp, maxConfigBlobBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiErrorFrom(resp.StatusCode, body)
	}

	bodyDigest := dgst.Algorithm().FromBytes(body)
	if bodyDigest != dgst {
		return nil, fmt.Errorf("%w: config blob expected %s, body is %s", ErrDigestMismatch, dgst, bodyDigest)
	}
	if header := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest")); header != "" {
		headerDigest, err := digest.Parse(header)
		if err != nil {
			return nil, fmt.Errorf("registry: malformed Docker-Content-Digest %q", header)
		}
		if headerDigest != dgst {
			return nil, fmt.Errorf("%w: header says %s, expected %s", ErrDigestMismatch, headerDigest, dgst)
		}
	}

	var img v1.Image
	if err := json.Unmarshal(body, &img); err != nil {
		return nil, fmt.Errorf("registry: decode image config: %w", err)
	}
	return &img, nil
}

// TagsSharingDigest returns every tag of repository whose manifest digest
// equals dgst. It reuses ListTags and sends a HEAD request for each tag's
// manifest with the standard manifest Accept header to inspect
// Docker-Content-Digest without downloading manifest bodies. This reports the
// blast radius before a manifest delete.
func (c *Client) TagsSharingDigest(ctx context.Context, repository string, dgst digest.Digest) ([]string, error) {
	if err := validRepositoryName(repository); err != nil {
		return nil, err
	}
	if err := dgst.Validate(); err != nil {
		return nil, fmt.Errorf("registry: invalid digest %q: %w", dgst, err)
	}

	tags, err := c.ListTags(ctx, repository, 0)
	if err != nil {
		return nil, err
	}

	var sharing []string
	for _, tag := range tags {
		if err := validTagName(tag); err != nil {
			continue
		}
		tagDigest, ok, err := c.headManifestDigest(ctx, repository, tag)
		if err != nil {
			return nil, err
		}
		if ok && tagDigest == dgst {
			sharing = append(sharing, tag)
		}
	}
	return sharing, nil
}

func (c *Client) headManifestDigest(ctx context.Context, repository, tag string) (digest.Digest, bool, error) {
	resp, err := c.doURL(ctx, http.MethodHead,
		c.urlFor("/v2/"+repository+"/manifests/"+tag),
		http.Header{"Accept": {acceptManifests}})
	if err != nil {
		return "", false, err
	}
	body, err := readBody(resp, maxManifestBytes)
	if err != nil {
		return "", false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, apiErrorFrom(resp.StatusCode, body)
	}

	header := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	if header == "" {
		return "", false, nil
	}
	parsed, err := digest.Parse(header)
	if err != nil {
		return "", false, fmt.Errorf("registry: malformed Docker-Content-Digest %q", header)
	}
	return parsed, true, nil
}

// DeleteManifest deletes a manifest from repository by digest
// (DELETE /v2/<repo>/manifests/<digest>). The OCI distribution spec forbids
// deleting by tag name; a non-digest reference is rejected client-side before
// any network request is made.
//
// 202 Accepted and 200 OK (and any 2xx) succeed. A 405 Method Not Allowed or
// an OCI UNSUPPORTED error code returns an *APIError matching
// errors.Is(err, ErrDeleteUnsupported). 401, 403 and 404 return *APIError
// values matching ErrUnauthorized, ErrForbidden and ErrNotFound.
func (c *Client) DeleteManifest(ctx context.Context, repository string, dgst digest.Digest) error {
	if err := validRepositoryName(repository); err != nil {
		return err
	}
	if err := dgst.Validate(); err != nil {
		return fmt.Errorf("registry: invalid manifest digest %q: %w", dgst, err)
	}

	resp, err := c.doURL(ctx, http.MethodDelete,
		c.urlFor("/v2/"+repository+"/manifests/"+dgst.String()),
		nil)
	if err != nil {
		return err
	}
	body, err := readBody(resp, maxManifestBytes)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return apiErrorFrom(resp.StatusCode, body)
}
