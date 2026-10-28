package registry

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultCertsDir is docker's per-host CA directory, mirrored here so a
// registry added to /etc/docker/certs.d works for lazydocker too.
const DefaultCertsDir = "/etc/docker/certs.d"

// normalizeHost turns the user-facing registry address into a bare
// "host[:port]" plus the initial scheme. Accepted inputs: "registry.io",
// "registry.io:5000", "https://registry.io", "http://localhost:5000",
// "[::1]:5000". An explicit scheme pins it; otherwise the client starts on
// https and (for loopback or insecure registries) may fall back to http like
// the docker daemon does.
func normalizeHost(host string) (hostPort string, initialScheme string, explicit bool, err error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", false, fmt.Errorf("registry: empty host")
	}

	scheme := ""
	if u, perr := url.Parse(host); perr == nil && (u.Scheme == "http" || u.Scheme == "https") {
		scheme = u.Scheme
		host = u.Host
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return "", "", false, fmt.Errorf("registry: host %q must not contain a path, query or userinfo", host)
		}
	}
	if host == "" {
		return "", "", false, fmt.Errorf("registry: empty host")
	}

	// Validate the host part parses as host[:port]. A lone colon-less name is
	// fine; anything else malformed ("a:b:c") is rejected here rather than
	// deep inside a request later.
	if _, _, serr := net.SplitHostPort(host); serr != nil {
		if strings.Contains(host, ":") {
			return "", "", false, fmt.Errorf("registry: invalid host %q", host)
		}
	}

	if scheme != "" {
		return host, scheme, true, nil
	}
	return host, "https", false, nil
}

// isLoopbackHost reports whether host ("host" or "host:port", possibly
// "[::1]:5000") is a loopback address or "localhost", the hosts docker
// treats as http-capable by default.
func isLoopbackHost(hostPort string) bool {
	host, _ := splitHostPort(hostPort)
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loadCACert reads <certsDir>/<host>/ca.crt, when present, into pool. A nil
// pool means "system roots only". A missing directory or file is not an error
// (most hosts have none); an unreadable or malformed ca.crt is, because
// silently skipping a CA the user pointed at produces confusing TLS failures
// later.
func loadCACert(certsDir, host string) (*x509.CertPool, error) {
	if certsDir == "" || host == "" {
		return nil, nil
	}
	caPath := filepath.Join(certsDir, host, "ca.crt")
	pem, err := os.ReadFile(caPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("registry: %s contains no valid certificate", caPath)
	}
	return pool, nil
}

// schemeFallbackTransport retries a failed https request over plain http,
// once, mirroring the docker daemon's behaviour for loopback and
// insecure-registries: try https, and when the TLS endpoint is not there,
// speak http. Only enabled for loopback hosts or Options.Insecure.
type schemeFallbackTransport struct {
	base    http.RoundTripper
	enabled bool
}

func (t *schemeFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !t.enabled {
		return t.base.RoundTrip(req)
	}

	resp, err := t.base.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	// Do not "fall back" on a cancelled or deadlined request; the caller
	// gave up, retrying over http cannot help.
	if req.Context().Err() != nil {
		return nil, err
	}
	// A body can only be replayed when GetBody is set; ping and token
	// requests are GETs (nil body), and later POSTs must set GetBody to
	// benefit from the fallback.
	if req.Body != nil && req.GetBody == nil {
		return nil, err
	}

	httpReq := req.Clone(req.Context())
	if req.Body != nil {
		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			return nil, err
		}
		httpReq.Body = body
	}
	httpReq.URL.Scheme = "http"
	return t.base.RoundTrip(httpReq)
}

// newBaseTransport returns the base round tripper and client settings for a
// client. When Options.HTTPClient is provided its transport is used verbatim
// (the caller owns its TLS configuration; certs.d is not injected into it);
// otherwise a stdlib transport is built with the certs.d CA pool and the
// insecure-registries TLS relaxation.
func newBaseTransport(host string, opts Options) (base http.RoundTripper, timeout time.Duration, jar http.CookieJar, err error) {
	if opts.HTTPClient != nil {
		base = http.DefaultTransport
		if opts.HTTPClient.Transport != nil {
			base = opts.HTTPClient.Transport
		}
		return base, opts.HTTPClient.Timeout, opts.HTTPClient.Jar, nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.Insecure {
		// Insecure-registries semantics: self-signed certificates are
		// accepted. Plain http itself is handled by the fallback transport.
		tlsCfg.InsecureSkipVerify = true
	}
	pool, err := loadCACert(certsDirOrDefault(opts.CertsDir), host)
	if err != nil {
		return nil, 0, nil, err
	}
	tlsCfg.RootCAs = pool

	return &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: tlsCfg,
	}, 0, nil, nil
}

func certsDirOrDefault(dir string) string {
	if dir == "" {
		return DefaultCertsDir
	}
	return dir
}
