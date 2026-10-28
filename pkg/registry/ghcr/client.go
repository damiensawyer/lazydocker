// Package ghcr implements the GitHub Container Registry (ghcr.io) client layer:
// GitHub REST API operations for container packages (user vs organization
// detection, paginated package and version listing, version/tag blast-radius
// resolution, version deletion, and whole-package deletion) plus a helper that
// builds a pkg/registry OCI client for ghcr.io using the GitHub token.
//
// Security invariants:
//   - The GitHub token is sent as "Authorization: Bearer <token>" only to the
//     configured API host, and never over plain HTTP to a non-loopback host.
//   - Pagination Link headers and HTTP redirects are validated to stay on the
//     configured API origin before any request is dispatched.
//   - The token is never logged, formatted, marshalled to JSON, or included in
//     error messages.
package ghcr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/jesseduffield/lazydocker/pkg/registry"
)

const (
	// DefaultAPIBaseURL is the default GitHub REST API base URL.
	DefaultAPIBaseURL = "https://api.github.com"

	// RegistryHost is the OCI distribution host for GitHub Container Registry.
	RegistryHost = "ghcr.io"

	// defaultTokenUsername is the fallback basic-auth username used when
	// exchanging a GitHub token at the ghcr.io OCI token realm if the caller
	// did not supply a username.
	defaultTokenUsername = "token"

	apiVersionHeader = "2022-11-28"
	acceptHeader     = "application/vnd.github+json"
	userAgentHeader  = "lazydocker"
	perPageDefault   = "100"
)

var (
	// ErrUnauthorized is returned when the GitHub API responds with 401 Unauthorized.
	ErrUnauthorized = errors.New("ghcr: unauthorized")

	// ErrForbidden is returned when the GitHub API responds with 403 Forbidden.
	ErrForbidden = errors.New("ghcr: forbidden")

	// ErrNotFound is returned when the requested owner, package, version, or tag is not found (404).
	ErrNotFound = errors.New("ghcr: not found")

	// ErrForeignHost is returned when a pagination Link header or redirect points
	// outside the configured GitHub API origin.
	ErrForeignHost = errors.New("ghcr: foreign host refused")

	// ErrInsecureTokenRefused is returned when a token would be transmitted over
	// plain HTTP to a non-loopback host.
	ErrInsecureTokenRefused = errors.New("ghcr: refusing to send token over plain http to non-loopback host")
)

// Visibility describes whether a GitHub container package is public or private.
type Visibility string

const (
	// VisibilityPublic indicates a publicly readable container package.
	VisibilityPublic Visibility = "PUBLIC"
	// VisibilityPrivate indicates a private or internal container package.
	VisibilityPrivate Visibility = "PRIVATE"
)

func normalizeVisibility(v string) Visibility {
	if strings.EqualFold(strings.TrimSpace(v), "public") {
		return VisibilityPublic
	}
	return VisibilityPrivate
}

// OwnerType identifies whether a GitHub account is a User or an Organization.
type OwnerType string

const (
	// OwnerTypeUser represents a personal GitHub user account.
	OwnerTypeUser OwnerType = "User"
	// OwnerTypeOrg represents a GitHub organization account.
	OwnerTypeOrg OwnerType = "Organization"
)

// Package represents a container package hosted on ghcr.io.
type Package struct {
	// ID is the GitHub package identifier when reported by the API.
	ID int64
	// Name is the full ghcr.io repository path "<owner>/<package>".
	Name string
	// PackageName is the bare package name within the owner namespace.
	PackageName string
	// Owner is the GitHub user or organization login.
	Owner string
	// Visibility is VisibilityPublic ("PUBLIC") or VisibilityPrivate ("PRIVATE").
	Visibility Visibility
}

// Version represents a single container package version on GitHub Packages.
// Each version corresponds to a manifest digest ("name" in the GitHub API)
// and carries zero or more container tags in metadata.container.tags.
type Version struct {
	ID     int64
	Digest string
	Tags   []string
}

// APIError represents a non-2xx HTTP response from the GitHub API.
// It unwraps to ErrUnauthorized, ErrForbidden, or ErrNotFound for 401/403/404.
type APIError struct {
	StatusCode int
	Status     string
	Message    string
	Err        error
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("ghcr: github api returned %d (%s): %s", e.StatusCode, http.StatusText(e.StatusCode), e.Message)
	}
	if e.Status != "" {
		return fmt.Sprintf("ghcr: github api returned %s", e.Status)
	}
	return fmt.Sprintf("ghcr: github api returned %d", e.StatusCode)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// Options configures a GitHub Packages API Client.
type Options struct {
	// BaseURL overrides the GitHub REST API base URL (default
	// https://api.github.com). The token is only ever sent to this origin.
	BaseURL string

	// Token is the GitHub personal access token (or fine-grained token) sent
	// as "Authorization: Bearer <token>" to the API host. If empty and
	// Credential is set, the token is extracted from Credential.
	Token string

	// Credential optionally supplies the GitHub username (Owner fallback) and
	// token (Password, IdentityToken, or RegistryToken).
	Credential *registry.Credential

	// Owner is an optional default GitHub user or organization login used when
	// an empty owner argument is passed to client methods.
	Owner string

	// HTTPClient optionally supplies the underlying HTTP transport and timeout.
	HTTPClient *http.Client
}

func redactFlag(set bool) string {
	if !set {
		return "unset"
	}
	return "set"
}

// String implements fmt.Stringer with secret fields redacted.
func (o Options) String() string {
	hasToken := o.Token != "" || (o.Credential != nil && extractToken(o.Credential) != "")
	return fmt.Sprintf("ghcr.Options{BaseURL:%s, Owner:%s, Token:%s}", o.BaseURL, o.Owner, redactFlag(hasToken))
}

// GoString implements fmt.GoStringer with secret fields redacted.
func (o Options) GoString() string {
	return o.String()
}

// MarshalJSON redacts token material from JSON serialization.
func (o Options) MarshalJSON() ([]byte, error) {
	hasToken := o.Token != "" || (o.Credential != nil && extractToken(o.Credential) != "")
	return json.Marshal(struct {
		BaseURL  string `json:"BaseURL"`
		Owner    string `json:"Owner"`
		TokenSet bool   `json:"TokenSet"`
	}{
		BaseURL:  o.BaseURL,
		Owner:    o.Owner,
		TokenSet: hasToken,
	})
}

// Client talks to the GitHub REST API for container package management.
// It is safe for concurrent use.
type Client struct {
	baseURL    *url.URL
	token      string
	owner      string
	httpClient *http.Client

	mu         sync.Mutex
	ownerTypes map[string]OwnerType
}

// NewClient constructs a GitHub Packages API client from opts.
func NewClient(opts Options) (*Client, error) {
	rawBase := strings.TrimSpace(opts.BaseURL)
	if rawBase == "" {
		rawBase = DefaultAPIBaseURL
	}
	parsedBase, err := url.Parse(rawBase)
	if err != nil {
		return nil, fmt.Errorf("ghcr: invalid base url: %w", err)
	}
	if parsedBase.Scheme != "https" && parsedBase.Scheme != "http" {
		return nil, fmt.Errorf("ghcr: unsupported base url scheme %q", parsedBase.Scheme)
	}
	if parsedBase.Host == "" {
		return nil, errors.New("ghcr: base url must include a host")
	}
	if parsedBase.User != nil {
		return nil, errors.New("ghcr: base url must not include userinfo")
	}
	parsedBase.Path = strings.TrimRight(parsedBase.Path, "/")
	parsedBase.RawQuery = ""
	parsedBase.Fragment = ""

	token := opts.Token
	if token == "" && opts.Credential != nil {
		token = extractToken(opts.Credential)
	}
	owner := strings.TrimSpace(opts.Owner)
	if owner == "" && opts.Credential != nil {
		owner = strings.TrimSpace(opts.Credential.Username)
	}

	if token != "" && parsedBase.Scheme == "http" && !isLoopbackHost(parsedBase.Host) {
		return nil, ErrInsecureTokenRefused
	}

	c := &Client{
		baseURL:    parsedBase,
		token:      token,
		owner:      owner,
		ownerTypes: make(map[string]OwnerType),
	}

	var baseTransport http.RoundTripper = http.DefaultTransport
	var timeout = http.Client{}.Timeout
	var jar http.CookieJar
	if opts.HTTPClient != nil {
		if opts.HTTPClient.Transport != nil {
			baseTransport = opts.HTTPClient.Transport
		}
		timeout = opts.HTTPClient.Timeout
		jar = opts.HTTPClient.Jar
	}

	c.httpClient = &http.Client{
		Transport: baseTransport,
		Timeout:   timeout,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("ghcr: stopped after 10 redirects")
			}
			if !sameOrigin(c.baseURL, req.URL) {
				req.Header.Del("Authorization")
				return fmt.Errorf("%w: redirect to %q", ErrForeignHost, req.URL.Host)
			}
			return nil
		},
	}

	return c, nil
}

// String implements fmt.Stringer with the token redacted.
func (c *Client) String() string {
	if c == nil {
		return "<nil>"
	}
	base := ""
	if c.baseURL != nil {
		base = c.baseURL.String()
	}
	return fmt.Sprintf("ghcr.Client{BaseURL:%s, Owner:%s, Token:%s}", base, c.owner, redactFlag(c.token != ""))
}

// GoString implements fmt.GoStringer with the token redacted.
func (c *Client) GoString() string {
	return c.String()
}

// MarshalJSON redacts the token from JSON dumps.
func (c *Client) MarshalJSON() ([]byte, error) {
	base := ""
	if c.baseURL != nil {
		base = c.baseURL.String()
	}
	return json.Marshal(struct {
		BaseURL  string `json:"BaseURL"`
		Owner    string `json:"Owner"`
		TokenSet bool   `json:"TokenSet"`
	}{
		BaseURL:  base,
		Owner:    c.owner,
		TokenSet: c.token != "",
	})
}

// NewRegistryClient returns a *registry.Client configured for "ghcr.io" using
// the GitHub token in cred as the basic-auth password for the ghcr.io OCI
// bearer token flow.
func NewRegistryClient(cred *registry.Credential) (*registry.Client, error) {
	return NewRegistryClientWithOptions(RegistryHost, cred, registry.Options{})
}

// NewRegistryClientWithOptions returns a *registry.Client for the given ghcr
// registry host (defaulting to "ghcr.io" when empty) with cred normalized so
// that the GitHub token is sent as the basic-auth password during the OCI
// bearer token exchange.
func NewRegistryClientWithOptions(host string, cred *registry.Credential, opts registry.Options) (*registry.Client, error) {
	if strings.TrimSpace(host) == "" {
		host = RegistryHost
	}
	opts.Credential = normalizeRegistryCredential(cred)
	return registry.NewClient(host, opts)
}

func normalizeRegistryCredential(cred *registry.Credential) *registry.Credential {
	if cred == nil {
		return nil
	}
	token := extractToken(cred)
	if token == "" {
		if cred.Username == "" {
			return nil
		}
		cp := *cred
		return &cp
	}
	username := strings.TrimSpace(cred.Username)
	if username == "" {
		username = defaultTokenUsername
	}
	return &registry.Credential{
		Username: username,
		Password: token,
	}
}

func extractToken(cred *registry.Credential) string {
	if cred == nil {
		return ""
	}
	switch {
	case cred.Password != "":
		return cred.Password
	case cred.IdentityToken != "":
		return cred.IdentityToken
	case cred.RegistryToken != "":
		return cred.RegistryToken
	default:
		return ""
	}
}

type githubUserResponse struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubPackageResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	PackageType string `json:"package_type"`
	Visibility  string `json:"visibility"`
}

type githubVersionResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Metadata struct {
		PackageType string `json:"package_type"`
		Container   struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

type githubErrorResponse struct {
	Message string `json:"message"`
}

// DetectOwnerType queries GET /users/<owner> and returns whether the account
// is a User or an Organization based on the response's "type" field.
func (c *Client) DetectOwnerType(ctx context.Context, owner string) (OwnerType, error) {
	resolvedOwner, err := c.resolveOwner(owner)
	if err != nil {
		return "", err
	}

	cacheKey := strings.ToLower(resolvedOwner)
	c.mu.Lock()
	if cached, ok := c.ownerTypes[cacheKey]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	endpoint := c.endpointURL(fmt.Sprintf("/users/%s", url.PathEscape(resolvedOwner)), nil)
	var user githubUserResponse
	if _, err := c.getJSON(ctx, endpoint, &user); err != nil {
		return "", err
	}

	var ownerType OwnerType
	switch {
	case strings.EqualFold(user.Type, string(OwnerTypeOrg)) || strings.EqualFold(user.Type, "org"):
		ownerType = OwnerTypeOrg
	case strings.EqualFold(user.Type, string(OwnerTypeUser)) || user.Type == "":
		ownerType = OwnerTypeUser
	default:
		return "", fmt.Errorf("ghcr: unexpected owner type %q for %q", user.Type, resolvedOwner)
	}

	c.mu.Lock()
	c.ownerTypes[cacheKey] = ownerType
	c.mu.Unlock()

	return ownerType, nil
}

func (c *Client) ownerBasePath(ctx context.Context, owner string) (string, string, error) {
	resolvedOwner, err := c.resolveOwner(owner)
	if err != nil {
		return "", "", err
	}
	ownerType, err := c.DetectOwnerType(ctx, resolvedOwner)
	if err != nil {
		return "", "", err
	}
	escaped := url.PathEscape(resolvedOwner)
	if ownerType == OwnerTypeOrg {
		return "/orgs/" + escaped, resolvedOwner, nil
	}
	return "/users/" + escaped, resolvedOwner, nil
}

// ListPackages lists all container packages under owner (detecting whether
// owner is a user or an organization via GET /users/<owner>), following
// GitHub Link pagination headers and normalizing visibility to PUBLIC/PRIVATE.
func (c *Client) ListPackages(ctx context.Context, owner string) ([]Package, error) {
	prefix, resolvedOwner, err := c.ownerBasePath(ctx, owner)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("package_type", "container")
	q.Set("per_page", perPageDefault)
	nextURL := c.endpointURL(prefix+"/packages", q)

	packages := make([]Package, 0)
	for nextURL != "" {
		var page []githubPackageResponse
		next, err := c.getJSON(ctx, nextURL, &page)
		if err != nil {
			return nil, err
		}
		for _, p := range page {
			if p.PackageType != "" && !strings.EqualFold(p.PackageType, "container") {
				continue
			}
			name := strings.TrimSpace(p.Name)
			if name == "" {
				continue
			}
			bareName := trimOwnerPrefix(resolvedOwner, name)
			packages = append(packages, Package{
				ID:          p.ID,
				Name:        resolvedOwner + "/" + bareName,
				PackageName: bareName,
				Owner:       resolvedOwner,
				Visibility:  normalizeVisibility(p.Visibility),
			})
		}
		nextURL = next
	}

	return packages, nil
}

// ListContainerPackages is an alias for ListPackages.
func (c *Client) ListContainerPackages(ctx context.Context, owner string) ([]Package, error) {
	return c.ListPackages(ctx, owner)
}

// Validate checks that owner exists and that the client's token can list
// container packages for that owner (fetching a single 1-item page).
func (c *Client) Validate(ctx context.Context, owner string) error {
	prefix, _, err := c.ownerBasePath(ctx, owner)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("package_type", "container")
	q.Set("per_page", "1")
	endpoint := c.endpointURL(prefix+"/packages", q)

	var page []githubPackageResponse
	_, err = c.getJSON(ctx, endpoint, &page)
	return err
}

// ListVersions returns all versions of a container package for owner,
// including each version's ID, manifest digest (GitHub API "name"), and
// container tags (metadata.container.tags). Paginated via Link headers.
func (c *Client) ListVersions(ctx context.Context, owner, pkg string) ([]Version, error) {
	prefix, resolvedOwner, err := c.ownerBasePath(ctx, owner)
	if err != nil {
		return nil, err
	}
	pkgName, err := normalizePackageName(resolvedOwner, pkg)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("per_page", perPageDefault)
	path := fmt.Sprintf("%s/packages/container/%s/versions", prefix, url.PathEscape(pkgName))
	nextURL := c.endpointURL(path, q)

	versions := make([]Version, 0)
	for nextURL != "" {
		var page []githubVersionResponse
		next, err := c.getJSON(ctx, nextURL, &page)
		if err != nil {
			return nil, err
		}
		for _, v := range page {
			tags := make([]string, len(v.Metadata.Container.Tags))
			copy(tags, v.Metadata.Container.Tags)
			versions = append(versions, Version{
				ID:     v.ID,
				Digest: v.Name,
				Tags:   tags,
			})
		}
		nextURL = next
	}

	return versions, nil
}

// ResolveVersionForTag finds the package version whose tag list contains tag.
// If no version carries tag, an error wrapping ErrNotFound is returned.
func (c *Client) ResolveVersionForTag(ctx context.Context, owner, pkg, tag string) (Version, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return Version{}, errors.New("ghcr: empty tag")
	}

	versions, err := c.ListVersions(ctx, owner, pkg)
	if err != nil {
		return Version{}, err
	}

	for _, v := range versions {
		for _, t := range v.Tags {
			if t == tag {
				tagsCopy := make([]string, len(v.Tags))
				copy(tagsCopy, v.Tags)
				return Version{
					ID:     v.ID,
					Digest: v.Digest,
					Tags:   tagsCopy,
				}, nil
			}
		}
	}

	return Version{}, fmt.Errorf("ghcr: tag %q not found in package %q: %w", tag, pkg, ErrNotFound)
}

// ResolveTagDeletion resolves the package version whose tags contain tag and
// returns its version ID along with the full slice of tags attached to that
// version (the blast radius of deleting the version).
func (c *Client) ResolveTagDeletion(ctx context.Context, owner, pkg, tag string) (versionID int64, tags []string, err error) {
	v, err := c.ResolveVersionForTag(ctx, owner, pkg, tag)
	if err != nil {
		return 0, nil, err
	}
	return v.ID, v.Tags, nil
}

// DeleteVersion deletes a specific container package version by its ID via
// DELETE /{users|orgs}/<owner>/packages/container/<pkg>/versions/<id>.
func (c *Client) DeleteVersion(ctx context.Context, owner, pkg string, versionID int64) error {
	prefix, resolvedOwner, err := c.ownerBasePath(ctx, owner)
	if err != nil {
		return err
	}
	pkgName, err := normalizePackageName(resolvedOwner, pkg)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("%s/packages/container/%s/versions/%s",
		prefix, url.PathEscape(pkgName), strconv.FormatInt(versionID, 10))
	return c.deleteURL(ctx, c.endpointURL(path, nil))
}

// DeleteTag resolves the package version carrying tag, deletes that version,
// and returns the full list of tags that belonged to the deleted version (the
// blast radius).
func (c *Client) DeleteTag(ctx context.Context, owner, pkg, tag string) ([]string, error) {
	versionID, tags, err := c.ResolveTagDeletion(ctx, owner, pkg, tag)
	if err != nil {
		return nil, err
	}
	if err := c.DeleteVersion(ctx, owner, pkg, versionID); err != nil {
		return nil, err
	}
	return tags, nil
}

// DeletePackage deletes the entire container package via
// DELETE /{users|orgs}/<owner>/packages/container/<pkg>.
func (c *Client) DeletePackage(ctx context.Context, owner, pkg string) error {
	prefix, resolvedOwner, err := c.ownerBasePath(ctx, owner)
	if err != nil {
		return err
	}
	pkgName, err := normalizePackageName(resolvedOwner, pkg)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("%s/packages/container/%s", prefix, url.PathEscape(pkgName))
	return c.deleteURL(ctx, c.endpointURL(path, nil))
}

func (c *Client) resolveOwner(owner string) (string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = c.owner
	}
	if owner == "" {
		return "", errors.New("ghcr: owner is required")
	}
	if strings.ContainsAny(owner, "/\\?#") {
		return "", fmt.Errorf("ghcr: invalid owner %q", owner)
	}
	return owner, nil
}

func normalizePackageName(owner, pkg string) (string, error) {
	pkg = strings.TrimSpace(pkg)
	pkg = trimOwnerPrefix(owner, pkg)
	pkg = strings.TrimPrefix(pkg, "/")
	if pkg == "" {
		return "", errors.New("ghcr: package name is required")
	}
	return pkg, nil
}

func trimOwnerPrefix(owner, pkg string) string {
	if owner == "" {
		return pkg
	}
	prefix := owner + "/"
	if len(pkg) > len(prefix) && strings.EqualFold(pkg[:len(prefix)], prefix) {
		return pkg[len(prefix):]
	}
	return pkg
}

func (c *Client) endpointURL(escapedPath string, query url.Values) string {
	if !strings.HasPrefix(escapedPath, "/") {
		escapedPath = "/" + escapedPath
	}
	fullEscaped := c.baseURL.EscapedPath() + escapedPath
	u := *c.baseURL
	if rel, err := url.Parse(fullEscaped); err == nil {
		u.Path = rel.Path
		u.RawPath = rel.RawPath
	} else {
		u.Path = fullEscaped
		u.RawPath = ""
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func (c *Client) getJSON(ctx context.Context, rawURL string, dst any) (nextURL string, err error) {
	resp, err := c.do(ctx, http.MethodGet, rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", c.parseAPIError(resp)
	}

	if dst != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(dst); err != nil {
			return "", fmt.Errorf("ghcr: decode response: %w", c.redactErr(err))
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	}

	nextRaw := parseNextLink(resp.Header.Values("Link"))
	if nextRaw == "" {
		return "", nil
	}

	validated, err := c.validateNextURL(nextRaw)
	if err != nil {
		return "", err
	}
	return validated, nil
}

func (c *Client) deleteURL(ctx context.Context, rawURL string) error {
	resp, err := c.do(ctx, http.MethodDelete, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.parseAPIError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

func (c *Client) do(ctx context.Context, method, rawURL string) (*http.Response, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ghcr: invalid request url: %w", c.redactErr(err))
	}

	if !sameOrigin(c.baseURL, target) {
		return nil, fmt.Errorf("%w: %q", ErrForeignHost, target.Host)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ghcr: build request: %w", c.redactErr(err))
	}

	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("X-GitHub-Api-Version", apiVersionHeader)
	req.Header.Set("User-Agent", userAgentHeader)

	if c.token != "" {
		if target.Scheme == "http" && !isLoopbackHost(target.Host) {
			return nil, ErrInsecureTokenRefused
		}
		if sameOrigin(c.baseURL, req.URL) {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.redactErr(err)
	}
	return resp, nil
}

func (c *Client) validateNextURL(nextRaw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(nextRaw))
	if err != nil {
		return "", fmt.Errorf("ghcr: invalid pagination link: %w", c.redactErr(err))
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: pagination link contains userinfo", ErrForeignHost)
	}

	// Resolve relative Link references against the configured API base URL.
	if parsed.Scheme == "" && parsed.Host == "" {
		resolved := *c.baseURL
		if strings.HasPrefix(parsed.Path, "/") {
			resolved.Path = parsed.Path
			resolved.RawPath = parsed.RawPath
		} else {
			resolved.Path = c.baseURL.Path + "/" + parsed.Path
		}
		resolved.RawQuery = parsed.RawQuery
		parsed = &resolved
	}

	if !sameOrigin(c.baseURL, parsed) {
		return "", fmt.Errorf("%w: pagination link points to %q", ErrForeignHost, parsed.Host)
	}
	return parsed.String(), nil
}

func (c *Client) parseAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var ghErr githubErrorResponse
	msg := ""
	if err := json.Unmarshal(body, &ghErr); err == nil && ghErr.Message != "" {
		msg = ghErr.Message
	} else {
		msg = strings.TrimSpace(string(body))
	}
	msg = c.redactString(msg)

	var sentinel error
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		sentinel = ErrUnauthorized
	case http.StatusForbidden:
		sentinel = ErrForbidden
	case http.StatusNotFound:
		sentinel = ErrNotFound
	}

	return &APIError{
		StatusCode: resp.StatusCode,
		Status:     c.redactString(resp.Status),
		Message:    msg,
		Err:        sentinel,
	}
}

func (c *Client) redactString(s string) string {
	if c == nil || c.token == "" || s == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[REDACTED]")
}

func (c *Client) redactErr(err error) error {
	if err == nil || c == nil || c.token == "" {
		return err
	}
	if !strings.Contains(err.Error(), c.token) {
		return err
	}
	if errors.Is(err, ErrForeignHost) {
		return fmt.Errorf("%w: %s", ErrForeignHost, c.redactString(err.Error()))
	}
	return errors.New(c.redactString(err.Error()))
}

// parseNextLink extracts the URL with rel="next" from one or more HTTP Link
// header values.
func parseNextLink(headers []string) string {
	for _, header := range headers {
		for _, part := range strings.Split(header, ",") {
			part = strings.TrimSpace(part)
			if !hasRelNext(part) {
				continue
			}
			_, rest, ok := strings.Cut(part, "<")
			if !ok {
				continue
			}
			target, _, ok := strings.Cut(rest, ">")
			if !ok {
				continue
			}
			return strings.TrimSpace(target)
		}
	}
	return ""
}

func hasRelNext(linkEntry string) bool {
	_, params, ok := strings.Cut(linkEntry, ">")
	if !ok {
		return false
	}
	for _, param := range strings.Split(params, ";") {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		k, v, ok := strings.Cut(param, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "rel") {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		for _, rel := range strings.Fields(v) {
			if strings.EqualFold(rel, "next") {
				return true
			}
		}
	}
	return false
}

// sameOrigin checks that target shares the exact scheme, host, port, and base
// path prefix of base.
func sameOrigin(base, target *url.URL) bool {
	if base == nil || target == nil || target.User != nil {
		return false
	}
	if !strings.EqualFold(base.Scheme, target.Scheme) {
		return false
	}
	bh, bp := splitHostPort(base.Host, base.Scheme)
	th, tp := splitHostPort(target.Host, target.Scheme)
	if !strings.EqualFold(bh, th) || bp != tp {
		return false
	}
	if base.Path != "" && base.Path != "/" {
		if target.Path != base.Path && !strings.HasPrefix(target.Path, base.Path+"/") {
			return false
		}
	}
	return true
}

func splitHostPort(hostPort, scheme string) (string, string) {
	if h, p, err := net.SplitHostPort(hostPort); err == nil {
		return h, p
	}
	if strings.EqualFold(scheme, "http") {
		return hostPort, "80"
	}
	return hostPort, "443"
}

func isLoopbackHost(hostPort string) bool {
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
