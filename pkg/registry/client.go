// Package registry is the remote-registry client layer: transport, ping and
// the OCI distribution auth flows, ported from crtui's registryclient onto
// stdlib net/http. It is a pure library: no UI, no config files, no docker
// daemon, and every test runs against in-process httptest servers
// (registrytest).
package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Status is the reachability verdict of a Ping.
type Status string

const (
	// StatusOnline: the registry answered 2xx on /v2/ (after auth flows).
	StatusOnline Status = "online"
	// StatusUnauthorized: the registry answered 401 even after the auth
	// flows (missing or rejected credential).
	StatusUnauthorized Status = "unauthorized"
	// StatusOffline: the registry could not be reached at all, or answered
	// with a status that is neither 2xx nor 401 (DNS failure, refused
	// connection, 5xx, ...).
	StatusOffline Status = "offline"
)

// PingResult is the outcome of a Ping call.
type PingResult struct {
	Status   Status
	Duration time.Duration
}

// Options configures a Client. The zero value is a sane anonymous client for
// a normal https registry.
type Options struct {
	// Insecure marks the registry as insecure, docker-style: self-signed
	// TLS certificates are accepted and plain http is tried when https is
	// not reachable. Loopback hosts get the http fallback without this
	// flag; it exists for non-loopback plain-http registries.
	Insecure bool

	// CertsDir overrides the per-host CA directory (default
	// /etc/docker/certs.d). When <CertsDir>/<host>/ca.crt exists it is
	// added to the root pool for that client. Ignored when HTTPClient is
	// provided with its own transport.
	CertsDir string

	// HTTPClient, when set, supplies the base transport (and timeout/cookie
	// jar) the registry client runs on top of. The auth and scheme-fallback
	// wrappers always wrap whatever is configured here; the caller owns the
	// transport's TLS setup.
	HTTPClient *http.Client

	// Credential is the auth material for this registry. It is only ever
	// sent to the registry itself, to a token realm the registry named in
	// its own WWW-Authenticate challenge, and never over plain http to a
	// non-loopback host. Never logged.
	Credential *Credential
}

// Client talks to one registry host. It is safe for concurrent use; bearer
// tokens are cached per scope under a mutex.
type Client struct {
	host       string // host[:port], no scheme
	scheme     string // scheme requests start on
	credential *Credential
	tokens     *tokenCache
	httpClient *http.Client
	auth       *authTransport
}

// NewClient builds a client for host. Accepted forms: "registry.io",
// "registry.io:5000", "https://registry.io", "http://localhost:5000".
func NewClient(host string, opts Options) (*Client, error) {
	hostPort, scheme, explicit, err := normalizeHost(host)
	if err != nil {
		return nil, err
	}

	c := &Client{
		host:       hostPort,
		scheme:     scheme,
		credential: opts.Credential,
		tokens:     newTokenCache(),
	}

	c.auth = &authTransport{
		cred:   opts.Credential,
		policy: credentialPolicy{registryHost: hostPort},
		tokens: c.tokens,
	}

	base, timeout, jar, err := newBaseTransport(hostPort, opts)
	if err != nil {
		return nil, err
	}
	c.auth.base = base

	// https is tried first and may fall back to http for loopback hosts and
	// insecure registries, like the docker daemon; an explicit scheme is
	// pinned and never fallen back from.
	fallback := !explicit && (opts.Insecure || isLoopbackHost(hostPort))

	c.httpClient = &http.Client{
		Transport: &schemeFallbackTransport{base: c.auth, enabled: fallback},
		Timeout:   timeout,
		Jar:       jar,
	}
	return c, nil
}

// Host returns the registry address this client was built for (host[:port]).
func (c *Client) Host() string { return c.host }

// urlFor builds the absolute URL for an API path such as "/v2/".
func (c *Client) urlFor(path string) string {
	return c.scheme + "://" + c.host + path
}

// Ping probes GET /v2/ and reports Online (2xx), Unauthorized (401 even
// after the auth flows) or Offline (unreachable, or a status that is neither
// 2xx nor 401), plus how long the probe took. An error is returned only for
// client misuse (bad path, cancelled context); transport failures are
// reported as StatusOffline, not as errors, because "offline" is a verdict
// the caller wants to render, not a failure to handle.
func (c *Client) Ping(ctx context.Context) (PingResult, error) {
	start := time.Now()

	resp, err := c.do(ctx, http.MethodGet, "/v2/", nil, nil)
	took := time.Since(start)

	result := PingResult{Status: StatusOffline, Duration: took}
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Transport-level failure (connection refused, DNS, TLS): offline.
		return result, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		result.Status = StatusOnline
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		result.Status = StatusUnauthorized
	default:
		result.Status = StatusOffline
	}
	return result, nil
}

// do performs an authenticated registry request and returns the raw
// response. It is the workhorse the endpoint helpers (ping, catalog,
// tags, manifests, blobs, deletes) build on: URL assembly, auth flows and
// scheme fallback all happen here. query may be nil.
func (c *Client) do(ctx context.Context, method, path string, query map[string]string, header http.Header) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("registry: path %q must start with '/'", path)
	}
	full := c.urlFor(path)
	if len(query) > 0 {
		v := url.Values{}
		for k, val := range query {
			v.Set(k, val)
		}
		full += "?" + v.Encode()
	}
	return c.doURL(ctx, method, full, header)
}

// doURL performs an authenticated registry request against an absolute
// URL — the form pagination Link headers hand back. The default Accept
// header is application/json unless header supplies its own (manifest
// requests negotiate their media types through it).
func (c *Client) doURL(ctx context.Context, method, rawURL string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("registry: new request: %w", err)
	}
	if header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	return c.httpClient.Do(req)
}
