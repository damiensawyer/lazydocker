// Catalog, tag and manifest access on top of the OCI distribution spec
// endpoints /v2/_catalog, /v2/<name>/tags/list and /v2/<name>/manifests/
// <reference>, with RFC5988 Link-header pagination. Wire types are the
// vendored image-spec v1 ones (D-07).

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// Docker manifest media types (the OCI counterparts are
// v1.MediaTypeImageManifest and v1.MediaTypeImageIndex). Docker schema-1
// manifests are deliberately not supported: they are deprecated, carry
// inline JWS signatures, and every registry serves a v2 type to a client
// that asks for one.
const (
	// MediaTypeDockerManifest is a single Docker v2 image manifest.
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	// MediaTypeDockerManifestList is a Docker v2 manifest list (the Docker
	// spelling of an image index).
	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// acceptManifests is the Accept header manifest requests send: every image
// manifest and index media type this client understands, OCI and Docker v2.
const acceptManifests = "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, " +
	MediaTypeDockerManifest + ", " + MediaTypeDockerManifestList

const (
	// maxPaginationPages bounds a single listing call, so a server that
	// always answers with another Link cannot loop a caller forever (the
	// context remains the caller's own bound).
	maxPaginationPages = 1000
	// maxManifestBytes / maxPageBytes cap response bodies: manifests are
	// a few hundred kilobytes at worst, catalog/tag pages similar.
	maxManifestBytes = 16 << 20
	maxPageBytes     = 16 << 20
)

// ErrDigestMismatch is returned when the registry's Docker-Content-Digest
// header does not match the digest of the manifest body it served — a
// truncated or tampered transfer, never something to render from.
var ErrDigestMismatch = errors.New("registry: Docker-Content-Digest does not match the manifest body")

// ErrUnsupportedMediaType is returned for a manifest media type this client
// cannot parse (e.g. a Docker schema-1 manifest).
var ErrUnsupportedMediaType = errors.New("registry: unsupported manifest media type")

// Repository is one named repository in a registry's catalog. The name is
// the repository's path ("library/ubuntu", "foo/bar/baz").
type Repository struct {
	Name string
}

// Tag is one fetched manifest reference: everything GetTag (or a digest
// fetch) knows about a tag of one repository.
type Tag struct {
	// Repository is the repository the manifest was fetched from.
	Repository string
	// Name is the tag — or, for GetManifestByDigest, the digest string.
	Name string
	// Digest is the digest of the manifest bytes themselves.
	Digest digest.Digest
	// MediaType is the manifest's media type: a single image manifest
	// type or an index type (see IsIndex).
	MediaType string

	// Manifest is the parsed single image manifest; nil for indexes.
	Manifest *v1.Manifest
	// Index is the parsed image index / Docker manifest list; nil for
	// single manifests. Its Manifests descriptors carry each child's
	// digest and Platform; the child manifests themselves are fetched on
	// demand (GetManifestByDigest), not with the index.
	Index *v1.Index
}

// IsIndex reports whether the tag refers to an image index (multi-platform
// manifest list) rather than a single image manifest.
func (t *Tag) IsIndex() bool { return t.Index != nil }

// Platforms lists the platforms of an index's child manifests, in index
// order, skipping children that carry none (e.g. attestation/referrer
// descriptors). It returns nil for a single manifest, whose platform lives
// in its config blob — a later task's fetch.
func (t *Tag) Platforms() []v1.Platform {
	if t.Index == nil {
		return nil
	}
	var out []v1.Platform
	for _, m := range t.Index.Manifests {
		if m.Platform != nil {
			out = append(out, *m.Platform)
		}
	}
	return out
}

// APIErrorItem is one entry of an OCI distribution-spec error body.
type APIErrorItem struct {
	Code    string // OCI error code (NAME_UNKNOWN, MANIFEST_UNKNOWN, ...)
	Message string
	Detail  string
}

// APIError is a structured OCI distribution-spec error response, so callers
// can branch on the code (a 404 NAME_UNKNOWN renders differently from a
// 401 UNAUTHORIZED) instead of parsing strings.
type APIError struct {
	StatusCode int
	Errors     []APIErrorItem
}

func (e *APIError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("registry: api error: status %d", e.StatusCode)
	}
	first := e.Errors[0]
	if len(e.Errors) == 1 {
		return fmt.Sprintf("registry: api error %d (%s): %s", e.StatusCode, first.Code, first.Message)
	}
	return fmt.Sprintf("registry: api error %d (%s): %s (and %d more)", e.StatusCode, first.Code, first.Message, len(e.Errors)-1)
}

// Code returns the OCI error code of the first error item, "" when the body
// carried none.
func (e *APIError) Code() string {
	if len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0].Code
}

// apiErrorFrom builds an *APIError from a non-2xx response body. A body
// that is not OCI-shaped JSON still yields an *APIError (with the status
// code) rather than a parse error: the status is the actionable fact.
func apiErrorFrom(status int, body []byte) error {
	parsed := struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Detail  any    `json:"detail"`
		} `json:"errors"`
	}{}
	_ = json.Unmarshal(body, &parsed)
	apiErr := &APIError{StatusCode: status}
	for _, e := range parsed.Errors {
		item := APIErrorItem{Code: e.Code, Message: e.Message}
		if e.Detail != nil {
			item.Detail = fmt.Sprint(e.Detail)
		}
		apiErr.Errors = append(apiErr.Errors, item)
	}
	return apiErr
}

// repositoryNameRe approximates the distribution repository-name grammar:
// lowercase path components of [a-z0-9] separated by ., _, __ or runs of -,
// joined by /. It exists to keep malformed names out of URL paths, not to
// be a reference parser.
var repositoryNameRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*$`)

// tagNameRe is the distribution tag grammar.
var tagNameRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

func validRepositoryName(name string) error {
	if name == "" || len(name) > 255 || !repositoryNameRe.MatchString(name) {
		return fmt.Errorf("registry: invalid repository name %q", name)
	}
	return nil
}

func validTagName(name string) error {
	if !tagNameRe.MatchString(name) {
		return fmt.Errorf("registry: invalid tag name %q", name)
	}
	return nil
}

// ListRepositories returns the registry's whole repository catalog,
// following RFC5988 Link rel="next" headers page by page. pageSize, when
// positive, is the n= query parameter requested per page (the registry may
// answer with less); zero asks for the server's own default page size.
func (c *Client) ListRepositories(ctx context.Context, pageSize int) ([]Repository, error) {
	names, err := c.listPaginated(ctx, "/v2/_catalog", pageSize, decodeCatalogPage)
	if err != nil {
		return nil, err
	}
	repos := make([]Repository, len(names))
	for i, name := range names {
		repos[i] = Repository{Name: name}
	}
	return repos, nil
}

// ListTags returns every tag of repository, following Link rel="next"
// headers page by page. pageSize has the same meaning as in
// ListRepositories.
func (c *Client) ListTags(ctx context.Context, repository string, pageSize int) ([]string, error) {
	if err := validRepositoryName(repository); err != nil {
		return nil, err
	}
	return c.listPaginated(ctx, "/v2/"+repository+"/tags/list", pageSize, decodeTagsPage)
}

// GetTag fetches the manifest reference named by tag in repository and
// returns it as a *Tag: its digest, its media type, and either the parsed
// single image manifest or the parsed index with its children's platforms.
// Index children are NOT fetched here — call GetManifestByDigest for the
// child a platform pick selects.
func (c *Client) GetTag(ctx context.Context, repository, tag string) (*Tag, error) {
	if err := validTagName(tag); err != nil {
		return nil, err
	}
	return c.fetchManifest(ctx, repository, tag)
}

// GetManifestByDigest fetches a manifest (or a nested index) by digest —
// the on-demand child fetch for index manifests. The returned Tag's Name
// is the digest string.
func (c *Client) GetManifestByDigest(ctx context.Context, repository string, dgst digest.Digest) (*Tag, error) {
	if err := dgst.Validate(); err != nil {
		return nil, fmt.Errorf("registry: invalid digest %q: %w", dgst, err)
	}
	return c.fetchManifest(ctx, repository, dgst.String())
}

// fetchManifest performs the manifest GET and builds the Tag.
func (c *Client) fetchManifest(ctx context.Context, repository, reference string) (*Tag, error) {
	if err := validRepositoryName(repository); err != nil {
		return nil, err
	}

	resp, err := c.doURL(ctx, http.MethodGet,
		c.urlFor("/v2/"+repository+"/manifests/"+reference),
		http.Header{"Accept": {acceptManifests}})
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp, maxManifestBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiErrorFrom(resp.StatusCode, body)
	}

	mediaType := manifestMediaType(resp.Header.Get("Content-Type"), body)

	// The manifest's digest is computed from the bytes actually received;
	// a registry that names a different digest in
	// Docker-Content-Digest is telling us the body is not what it claims.
	bodyDigest := digest.FromBytes(body)
	if header := resp.Header.Get("Docker-Content-Digest"); header != "" {
		headerDigest, err := digest.Parse(header)
		if err != nil {
			return nil, fmt.Errorf("registry: malformed Docker-Content-Digest %q", header)
		}
		if headerDigest != bodyDigest {
			return nil, fmt.Errorf("%w: header says %s, body is %s", ErrDigestMismatch, headerDigest, bodyDigest)
		}
	}

	tag := &Tag{Repository: repository, Name: reference, Digest: bodyDigest, MediaType: mediaType}
	switch {
	case isIndexMediaType(mediaType):
		var index v1.Index
		if err := json.Unmarshal(body, &index); err != nil {
			return nil, fmt.Errorf("registry: decode image index: %w", err)
		}
		tag.Index = &index
	case isManifestMediaType(mediaType):
		var manifest v1.Manifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return nil, fmt.Errorf("registry: decode image manifest: %w", err)
		}
		tag.Manifest = &manifest
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, mediaType)
	}
	return tag, nil
}

func isIndexMediaType(mediaType string) bool {
	return mediaType == v1.MediaTypeImageIndex || mediaType == MediaTypeDockerManifestList
}

func isManifestMediaType(mediaType string) bool {
	return mediaType == v1.MediaTypeImageManifest || mediaType == MediaTypeDockerManifest
}

// manifestMediaType decides the manifest's media type: the Content-Type
// header when it names a type this client knows, else the body's own
// mediaType field (registries that answer application/json), else whatever
// Content-Type said — which the caller reports as unsupported.
func manifestMediaType(contentType string, body []byte) string {
	contentType = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	if isManifestMediaType(contentType) || isIndexMediaType(contentType) {
		return contentType
	}
	probe := struct {
		MediaType string `json:"mediaType"`
	}{}
	if json.Unmarshal(body, &probe) == nil && probe.MediaType != "" {
		if isManifestMediaType(probe.MediaType) || isIndexMediaType(probe.MediaType) {
			return probe.MediaType
		}
	}
	return contentType
}

// readBody drains and returns resp's body, capped at limit bytes.
func readBody(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("registry: read response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, errors.New("registry: response body exceeds size limit")
	}
	return body, nil
}

// listPaginated walks a paginated string listing (catalog or tags): GET
// start, decode a page, follow Link rel="next" until it stops appearing.
// A next link is resolved against the URL of the page carrying it, so both
// relative ("/v2/_catalog?last=x") and absolute links work; a repeated link
// ends the walk (a server stuck on one page is done, not a source of
// endless duplicates).
func (c *Client) listPaginated(ctx context.Context, path string, pageSize int, decode func([]byte) ([]string, error)) ([]string, error) {
	next, err := c.pageURL(path, pageSize)
	if err != nil {
		return nil, err
	}

	var out []string
	seen := map[string]bool{}
	for page := 0; page < maxPaginationPages; page++ {
		if seen[next.String()] {
			return out, nil
		}
		seen[next.String()] = true

		resp, err := c.doURL(ctx, http.MethodGet, next.String(), nil)
		if err != nil {
			return nil, err
		}
		body, err := readBody(resp, maxPageBytes)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, apiErrorFrom(resp.StatusCode, body)
		}
		items, err := decode(body)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)

		link := linkNext(resp.Header.Values("Link"))
		if link == nil {
			return out, nil
		}
		resolved := next.ResolveReference(link)
		if link.User != nil || !sameHostPort(c.host, resolved) || !strings.HasPrefix(resolved.Path, "/v2/") {
			return nil, fmt.Errorf("registry: pagination link %q leaves registry %s", link, c.host)
		}
		resolved.Scheme = c.scheme
		resolved.Host = c.host
		next = resolved
	}
	return nil, fmt.Errorf("registry: %s pagination exceeded %d pages", path, maxPaginationPages)
}

// pageURL builds the absolute URL of the first page of a listing.
func (c *Client) pageURL(path string, pageSize int) (*url.URL, error) {
	u, err := url.Parse(c.urlFor(path))
	if err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	if pageSize > 0 {
		q := u.Query()
		q.Set("n", strconv.Itoa(pageSize))
		u.RawQuery = q.Encode()
	}
	return u, nil
}

func decodeCatalogPage(body []byte) ([]string, error) {
	page := struct {
		Repositories []string `json:"repositories"`
	}{}
	// A null list decodes to nil, which is a valid empty page.
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("registry: decode catalog page: %w", err)
	}
	return page.Repositories, nil
}

func decodeTagsPage(body []byte) ([]string, error) {
	page := struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}{}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("registry: decode tags page: %w", err)
	}
	return page.Tags, nil
}

// linkNext extracts the rel="next" target from Link header values
// (RFC5988), the pointer registries use for catalog and tag pagination.
// The returned URL is unresolved; the caller resolves it against the URL of
// the page that carried it.
func linkNext(headerValues []string) *url.URL {
	for _, header := range headerValues {
		for _, link := range splitChallengeList(header) {
			target, params, ok := strings.Cut(strings.TrimSpace(link), ";")
			if !ok {
				continue
			}
			target = strings.TrimSpace(target)
			target = strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
			for _, param := range strings.Split(params, ";") {
				key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(key), "rel") {
					continue
				}
				value = strings.Trim(strings.TrimSpace(value), `"'`)
				if !strings.EqualFold(value, "next") {
					continue
				}
				if u, err := url.Parse(target); err == nil && u.String() != "" {
					return u
				}
			}
		}
	}
	return nil
}
