// Package dockerhub is the Docker Hub API client (hub.docker.com, not the
// OCI registry protocol): repository listing with pagination and visibility,
// tag listing, credential validation, and tag/repository deletes — ported
// from crtui's internal/infra/dockerhubclient onto stdlib net/http (D-01).
// Docker Hub exposes no OCI catalog endpoint, so this client IS the
// repository listing for Docker Hub.
//
// Auth model: a credential (username + password or personal access token) is
// exchanged once at POST /v2/users/login for a JWT, which is then sent as
// "Authorization: Bearer <jwt>" — only ever to the Hub base origin, never to
// a pagination target on another host, and never over a different scheme.
// Without a credential the client is anonymous and lists only what the
// namespace exposes publicly. The password and the JWT are never logged,
// never embedded in error strings, and never written anywhere.
//
// It is a pure library: no UI, no config files, no docker daemon, and every
// test runs against an in-process httptest server.
package dockerhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jesseduffield/lazydocker/pkg/registry"
)

// DefaultBaseURL is the public Docker Hub API origin.
const DefaultBaseURL = "https://hub.docker.com"

// Docker Hub caps anonymous pagination at an offset of roughly 100 results;
// authenticated paging goes further. Page sizes follow crtui's values.
const (
	pageSizeAuthenticated = "100"
	pageSizeAnonymous     = "25"

	// anonymousPaginationMarker appears in the Hub's 403 body when an
	// anonymous listing pages past the offset cap; listing then stops
	// gracefully with what it already has (crtui behaviour).
	anonymousPaginationMarker = "pagination offset too large"

	// maxPages is a hard stop against a Hub (or fixture) looping its next
	// URLs forever.
	maxPages = 1000

	// maxDecodeBytes caps how much of a response body is decoded.
	maxDecodeBytes = 8 << 20
)

// Options configures a Client. The zero value is a sane anonymous client for
// the public Hub.
type Options struct {
	// BaseURL overrides the Hub API origin (default DefaultBaseURL). Tests
	// point this at an httptest server. Must be an absolute http(s) URL;
	// its origin (scheme + host) is the only origin the JWT is ever sent
	// to and the only origin pagination may follow.
	BaseURL string

	// Namespace is the default namespace Validate checks; when empty,
	// Validate falls back to the credential's username.
	Namespace string

	// HTTPClient, when set, supplies the base transport and timeout. The
	// JWT guard wrapper is always applied on top, so a redirect away from
	// the Hub origin cannot carry the Authorization header even though
	// net/http copies it when following redirects.
	HTTPClient *http.Client

	// Credential is the Hub login material. Password (or IdentityToken)
	// may be a personal access token — the login endpoint accepts both.
	// It is sent only to the Hub base origin, over its scheme. Never
	// logged.
	Credential *registry.Credential
}

func redactSet(set bool) string {
	if !set {
		return "unset"
	}
	return "set"
}

// String implements fmt.Stringer with credential material redacted.
func (o Options) String() string {
	return fmt.Sprintf("dockerhub.Options{BaseURL:%s, Namespace:%s, Credential:%s}",
		o.BaseURL, o.Namespace, redactSet(o.Credential != nil))
}

// GoString implements fmt.GoStringer with credential material redacted.
func (o Options) GoString() string {
	return o.String()
}

// MarshalJSON redacts credential material from JSON dumps.
func (o Options) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		BaseURL       string `json:"BaseURL"`
		Namespace     string `json:"Namespace"`
		CredentialSet bool   `json:"CredentialSet"`
	}{
		BaseURL:       o.BaseURL,
		Namespace:     o.Namespace,
		CredentialSet: o.Credential != nil,
	})
}

// Client talks to one Hub API origin. It is safe for concurrent use; the
// login JWT is cached under a mutex and refreshed on a 401.
type Client struct {
	base      *url.URL
	namespace string
	cred      *registry.Credential

	httpClient *http.Client

	mu  sync.Mutex
	jwt string
}

// String implements fmt.Stringer with the credential and cached JWT redacted.
func (c *Client) String() string {
	if c == nil {
		return "<nil>"
	}
	base := ""
	if c.base != nil {
		base = c.base.String()
	}
	c.mu.Lock()
	hasJWT := c.jwt != ""
	c.mu.Unlock()
	return fmt.Sprintf("dockerhub.Client{BaseURL:%s, Namespace:%s, Credential:%s, JWT:%s}",
		base, c.namespace, redactSet(c.cred != nil), redactSet(hasJWT))
}

// GoString implements fmt.GoStringer with the credential and cached JWT redacted.
func (c *Client) GoString() string {
	return c.String()
}

// MarshalJSON redacts the credential and cached JWT from JSON dumps.
func (c *Client) MarshalJSON() ([]byte, error) {
	base := ""
	if c.base != nil {
		base = c.base.String()
	}
	c.mu.Lock()
	hasJWT := c.jwt != ""
	c.mu.Unlock()
	return json.Marshal(struct {
		BaseURL       string `json:"BaseURL"`
		Namespace     string `json:"Namespace"`
		CredentialSet bool   `json:"CredentialSet"`
		JWTSet        bool   `json:"JWTSet"`
	}{
		BaseURL:       base,
		Namespace:     c.namespace,
		CredentialSet: c.cred != nil,
		JWTSet:        hasJWT,
	})
}

// New builds a Hub client.
func New(opts Options) (*Client, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("dockerhub: bad base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("dockerhub: base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("dockerhub: base URL has no host: %q", base)
	}
	if u.User != nil {
		return nil, fmt.Errorf("dockerhub: base URL must not contain userinfo")
	}
	if opts.Credential != nil && u.Scheme == "http" && !isLoopbackHost(u.Host) {
		return nil, ErrInsecureHTTP
	}
	// Only the origin matters (JWT scoping, pagination checks); drop any
	// path so first-page URLs build by concatenation.
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	baseTransport := http.DefaultTransport
	var timeout time.Duration
	if opts.HTTPClient != nil {
		timeout = opts.HTTPClient.Timeout
		if opts.HTTPClient.Transport != nil {
			baseTransport = opts.HTTPClient.Transport
		}
	}

	return &Client{
		base:      u,
		namespace: opts.Namespace,
		cred:      opts.Credential,
		httpClient: &http.Client{
			// The guard is the credential's last line of defence: even a
			// redirect followed inside http.Client cannot move the
			// Authorization header or a login POST body off the Hub origin.
			Transport: &authGuardTransport{
				base:          baseTransport,
				allowedScheme: u.Scheme,
				allowedHost:   u.Host,
			},
			Timeout: timeout,
		},
	}, nil
}

// isLoopbackHost reports whether hostPort ("host" or "host:port", possibly
// "[::1]:5000") is a loopback IP or "localhost".
func isLoopbackHost(hostPort string) bool {
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	} else {
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// authGuardTransport strips the Authorization header from any GET/HEAD request
// whose origin (scheme + host) is not the Hub base origin, and refuses any
// non-GET/HEAD request or request with a body (such as POST /v2/users/login)
// that was redirected off the Hub origin. Requests are cloned shallowly; the
// original is never mutated (RoundTrip must not).
type authGuardTransport struct {
	base          http.RoundTripper
	allowedScheme string
	allowedHost   string
}

func (t *authGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	onOrigin := strings.EqualFold(req.URL.Scheme, t.allowedScheme) &&
		strings.EqualFold(req.URL.Host, t.allowedHost)
	if !onOrigin {
		if req.Body != nil || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
			return nil, fmt.Errorf("%w: %s://%s (Hub origin is %s://%s)",
				ErrForeignNext, req.URL.Scheme, req.URL.Host, t.allowedScheme, t.allowedHost)
		}
		if req.Header.Get("Authorization") != "" {
			guarded := req.Clone(req.Context())
			guarded.Header.Del("Authorization")
			return t.base.RoundTrip(guarded)
		}
	}
	return t.base.RoundTrip(req)
}

// apiURL builds the absolute URL for an API path (which must start with "/"
// and already be path-escaped), optionally with a page_size query.
func (c *Client) apiURL(path, pageSize string) string {
	full := c.base.String() + path
	if pageSize != "" {
		full += "?page_size=" + pageSize
	}
	return full
}

// followURL validates a pagination "next" URL before any request is made to
// it: it must sit on the Hub base origin (same scheme AND host), or be a path
// relative to it. Network-path references ("//evil.host/...") and foreign
// origins are refused with ErrForeignNext — before the request, so the JWT
// cannot leave the Hub origin via a pagination hop.
// A nil URL (and nil error) means "no more pages".
func (c *Client) followURL(next string) (*url.URL, error) {
	if next == "" {
		return nil, nil
	}
	u, err := url.Parse(next)
	if err != nil {
		return nil, fmt.Errorf("dockerhub: unparseable pagination URL: %w", err)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: pagination URL contains userinfo", ErrForeignNext)
	}
	if !u.IsAbs() {
		u = c.base.ResolveReference(u)
	}
	if !sameOrigin(c.base, u) {
		return nil, fmt.Errorf("%w: %s://%s (Hub origin is %s://%s)",
			ErrForeignNext, u.Scheme, u.Host, c.base.Scheme, c.base.Host)
	}
	return u, nil
}

// pageSizeFor reports the Hub page size to request: larger pages are allowed
// once authenticated; anonymous listings use the small page the Hub allows.
func (c *Client) pageSizeFor() string {
	if c.cred != nil {
		return pageSizeAuthenticated
	}
	return pageSizeAnonymous
}

// secret returns the credential's login secret: the password, or the
// identity/registry token when only that is set (a PAT may arrive in any of
// those fields).
func (c *Client) secret() string {
	if c.cred == nil {
		return ""
	}
	if c.cred.Password != "" {
		return c.cred.Password
	}
	if c.cred.IdentityToken != "" {
		return c.cred.IdentityToken
	}
	return c.cred.RegistryToken
}

// token returns the cached Hub JWT, logging in lazily on first use (and
// after invalidation). Anonymous clients return "" without any request.
func (c *Client) token(ctx context.Context) (string, error) {
	if c.cred == nil {
		return "", nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwt != "" {
		return c.jwt, nil
	}
	if c.cred.Username == "" || c.secret() == "" {
		return "", fmt.Errorf("dockerhub: credential has no username + password/PAT to log in with")
	}

	jwt, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	c.jwt = jwt
	return c.jwt, nil
}

// invalidateToken drops the cached JWT so the next request re-logins. Used
// after a 401: Hub JWTs expire.
func (c *Client) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jwt = ""
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

// login performs POST /v2/users/login and returns the JWT. The request body
// carries the password — it must never appear in an error. The response body
// of a success carries the JWT — it must never appear in an error either;
// failures map to the typed status errors with only the Hub's message field.
func (c *Client) login(ctx context.Context) (string, error) {
	payload, err := json.Marshal(loginRequest{Username: c.cred.Username, Password: c.secret()})
	if err != nil {
		return "", fmt.Errorf("dockerhub: encode login request: %w", err)
	}
	// c.mu is held by token(); login itself performs no locking.

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("/v2/users/login", ""), bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("dockerhub: new login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dockerhub: login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", mapStatusError(http.MethodPost, req.URL, resp)
	}

	var out loginResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxDecodeBytes))
	if err := dec.Decode(&out); err != nil {
		return "", fmt.Errorf("dockerhub: decode login response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("dockerhub: login succeeded but returned no token")
	}
	return out.Token, nil
}

// send performs one authenticated Hub API request. The JWT — when the client
// has one and the target is the Hub origin — goes on as a Bearer header; the
// transport guard enforces the same rule again for redirects. The caller
// owns closing the response body.
func (c *Client) send(ctx context.Context, method, urlStr string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, reader)
	if err != nil {
		return nil, fmt.Errorf("dockerhub: new request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	// Belt and braces: followURL has already vetted the origin, but the
	// JWT only ever goes to the Hub base origin, checked here too.
	if token != "" && sameOrigin(c.base, req.URL) {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return c.httpClient.Do(req)
}

// call performs one Hub API request and, on success (2xx), decodes the JSON
// body into out when out is not nil. A 401 with a credential invalidates the
// cached JWT, logs in again and retries once — Hub JWTs expire mid-session.
func (c *Client) call(ctx context.Context, method, urlStr string, body []byte, out any) error {
	resp, err := c.send(ctx, method, urlStr, body)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized && c.cred != nil {
		resp.Body.Close()
		c.invalidateToken()
		if resp, err = c.send(ctx, method, urlStr, body); err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// mapStatusError consumes a bounded body for the message field.
		return mapStatusError(method, resp.Request.URL, resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDecodeBytes))
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxDecodeBytes))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("dockerhub: decode response: %w", err)
	}
	return nil
}

// listAll pages a Hub list endpoint from firstURL, decoding pages of T and
// handing each to collect until there is no next page. Anonymous listings
// that hit the Hub's pagination cap stop gracefully with what they have
// (crtui behaviour); for an authenticated client the same 403 is an error.
func listAll[T any](ctx context.Context, c *Client, firstURL string, collect func(page hubPage[T]) error) error {
	next := firstURL
	for pages := 0; next != ""; pages++ {
		if pages >= maxPages {
			return fmt.Errorf("docker hub: pagination exceeded %d pages", maxPages)
		}

		var page hubPage[T]
		if err := c.call(ctx, http.MethodGet, next, nil, &page); err != nil {
			if c.cred == nil &&
				strings.Contains(statusMessage(err), anonymousPaginationMarker) {
				return nil
			}
			return err
		}
		if err := collect(page); err != nil {
			return err
		}

		rawNext := ""
		if page.Next != nil {
			rawNext = *page.Next
		}
		u, err := c.followURL(rawNext)
		if err != nil {
			return err
		}
		if u == nil {
			return nil
		}
		next = u.String()
	}
	return nil
}

// ListRepositories lists the image repositories of namespace (a user or
// org), following pagination until the end (or the anonymous cap: the
// results fetched so far are returned, not an error). Non-image repository
// types (plugins, ...) and unnamed entries are skipped, and is_private maps
// to Visibility.
func (c *Client) ListRepositories(ctx context.Context, namespace string) ([]Repository, error) {
	if namespace == "" {
		return nil, fmt.Errorf("dockerhub: namespace is required")
	}
	first := c.apiURL("/v2/repositories/"+url.PathEscape(namespace)+"/", c.pageSizeFor())

	repos := []Repository{}
	err := listAll(ctx, c, first, func(page hubPage[hubRepository]) error {
		for _, r := range page.Results {
			if r.Name == "" || (r.RepositoryType != "" && r.RepositoryType != "image") {
				continue
			}
			visibility := VisibilityPublic
			if r.IsPrivate {
				visibility = VisibilityPrivate
			}
			repos = append(repos, Repository{
				Name:       r.Name,
				Namespace:  r.Namespace,
				Visibility: visibility,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return repos, nil
}

// ListTags lists the tags of namespace/repository, newest first (the Hub's
// default ordering), following pagination like ListRepositories.
func (c *Client) ListTags(ctx context.Context, namespace, repository string) ([]Tag, error) {
	if namespace == "" || repository == "" {
		return nil, fmt.Errorf("dockerhub: namespace and repository are required")
	}
	path := "/v2/repositories/" + url.PathEscape(namespace) + "/" + url.PathEscape(repository) + "/tags/"
	first := c.apiURL(path, c.pageSizeFor())

	tags := []Tag{}
	err := listAll(ctx, c, first, func(page hubPage[hubTag]) error {
		for _, t := range page.Results {
			tags = append(tags, Tag{
				Name:        t.Name,
				Digest:      t.Digest,
				LastUpdated: t.LastUpdated,
				FullSize:    t.FullSize,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// Validate confirms the configured credential and namespace work: with a
// credential it logs in (a rejected login fails immediately), then fetches
// the first page of the namespace's repositories (Options.Namespace, else
// the credential's username) to confirm the namespace is visible. An
// anonymous client validates its namespace only.
func (c *Client) Validate(ctx context.Context) error {
	if c.cred != nil {
		if _, err := c.token(ctx); err != nil {
			return err
		}
	} else if c.namespace == "" {
		return fmt.Errorf("dockerhub: cannot validate an anonymous client with no namespace configured")
	}

	ns := c.namespace
	if ns == "" && c.cred != nil {
		ns = c.cred.Username
	}
	if ns == "" {
		// Credential-only validation: login already succeeded.
		return nil
	}
	return c.call(ctx, http.MethodGet, c.apiURL("/v2/repositories/"+url.PathEscape(ns)+"/", "1"), nil, &hubPage[hubRepository]{})
}

// DeleteTag deletes one tag of namespace/repository via the Hub API.
// DELETE /v2/repositories/<ns>/<repo>/tags/<tag>/ — the Hub deletes a tag,
// not the manifest digest (an OCI delete would remove every co-tagged tag).
// Requires login; ErrLoginRequired is returned before any request when no
// credential is configured. 401/403/404 map to the typed sentinels.
func (c *Client) DeleteTag(ctx context.Context, namespace, repository, tag string) error {
	if c.cred == nil {
		return ErrLoginRequired
	}
	if namespace == "" || repository == "" || tag == "" {
		return fmt.Errorf("dockerhub: namespace, repository and tag are required")
	}
	path := "/v2/repositories/" + url.PathEscape(namespace) + "/" + url.PathEscape(repository) +
		"/tags/" + url.PathEscape(tag) + "/"
	return c.call(ctx, http.MethodDelete, c.apiURL(path, ""), nil, nil)
}

// DeleteRepository deletes namespace/repository via the Hub API
// (DELETE /v2/repositories/<ns>/<repo>/), which removes every tag in it.
// Like DeleteTag it requires login and maps 401/403/404 to typed errors.
func (c *Client) DeleteRepository(ctx context.Context, namespace, repository string) error {
	if c.cred == nil {
		return ErrLoginRequired
	}
	if namespace == "" || repository == "" {
		return fmt.Errorf("dockerhub: namespace and repository are required")
	}
	path := "/v2/repositories/" + url.PathEscape(namespace) + "/" + url.PathEscape(repository) + "/"
	return c.call(ctx, http.MethodDelete, c.apiURL(path, ""), nil, nil)
}
