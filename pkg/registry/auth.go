package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// challenge is one parsed WWW-Authenticate challenge from a 401 response.
type challenge struct {
	// scheme is "bearer" or "basic" (lower-cased).
	scheme string
	// params holds the k=v pairs (realm, service, scope, error...), keys
	// lower-cased, values unquoted.
	params map[string]string
}

// parseChallenges parses every WWW-Authenticate header value of a response.
// A value may carry several comma-separated challenges whose parameters may
// themselves contain quoted commas, so splitting is quote-aware and a
// segment whose first word carries '=' is a parameter of the previous
// challenge rather than a new one (RFC 7235 shape).
func parseChallenges(headerValues []string) []challenge {
	var out []challenge
	for _, header := range headerValues {
		for _, segment := range splitChallengeList(header) {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}
			firstWord := segment
			if idx := strings.IndexAny(segment, " \t"); idx >= 0 {
				firstWord = segment[:idx]
			}
			if len(out) > 0 && strings.Contains(firstWord, "=") {
				addChallengeParam(out[len(out)-1], segment)
				continue
			}
			scheme, rest, _ := strings.Cut(segment, " ")
			c := challenge{scheme: strings.ToLower(strings.TrimSpace(scheme)), params: map[string]string{}}
			addChallengeParam(c, rest)
			out = append(out, c)
		}
	}
	return out
}

// addChallengeParam parses one `key=value` (value optionally quoted) into c.
func addChallengeParam(c challenge, s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	key, value, ok := strings.Cut(s, "=")
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	c.params[strings.ToLower(strings.TrimSpace(key))] = value
}

// splitChallengeList splits on commas that are not inside double quotes.
func splitChallengeList(s string) []string {
	var parts []string
	var current strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && inQuotes:
			current.WriteRune(r)
			escaped = true
		case r == '"':
			current.WriteRune(r)
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	parts = append(parts, current.String())
	return parts
}

// bearerChallenge returns the first Bearer challenge, if any. Bearer is
// preferred over Basic when a response offers both (the OCI token flow
// subsumes basic auth by carrying it to the realm).
func bearerChallenge(challenges []challenge) (challenge, bool) {
	for _, c := range challenges {
		if c.scheme == "bearer" {
			return c, true
		}
	}
	return challenge{}, false
}

func basicChallenge(challenges []challenge) bool {
	for _, c := range challenges {
		if c.scheme == "basic" {
			return true
		}
	}
	return false
}

// credentialPolicy decides whether credential material may be attached to a
// request for the given target URL. It is the security kernel of the bearer
// flow: the two rules it refuses on are the plan's hard requirements.
//
//  1. Credentials go to the registry's own token realm only. A realm on a
//     different host than the registry receives credentials only when that
//     realm was named in the registry's own WWW-Authenticate challenge
//     (that is how Docker Hub works: registry-1.docker.io challenges with
//     realm=auth.docker.io). A realm from anywhere else never sees them.
//  2. Credentials never travel over plain http to a non-loopback host, no
//     matter who named the realm.
type credentialPolicy struct {
	registryHost string // host or host:port of the registry the credential belongs to
}

// errCredentialRefused is returned (internally) when policy forbids sending
// the credential to target. It never carries credential material.
var errCredentialRefused = errors.New("registry: credential refused for target")

func (p credentialPolicy) maySendCredentialTo(target *url.URL, viaRegistryChallenge bool) error {
	if target == nil {
		return errCredentialRefused
	}

	// Rule 2: no credentials over plain http except to loopback.
	if target.Scheme == "http" && !isLoopbackHost(target.Host) {
		return errCredentialRefused
	}
	if target.Scheme != "https" && target.Scheme != "http" {
		return errCredentialRefused
	}

	// Rule 1: same host as the registry, or a realm the registry itself
	// named in a challenge.
	if viaRegistryChallenge || sameHostPort(p.registryHost, target) {
		return nil
	}
	return errCredentialRefused
}

// sameHostPort compares a "host[:port]" registry address against a URL,
// filling in default ports by scheme so "example.com" matches
// "https://example.com/" but not "https://example.com:5000/".
func sameHostPort(registryHost string, target *url.URL) bool {
	rh, rp := splitHostPort(registryHost)
	th, tp := splitHostPort(target.Host)
	if !strings.EqualFold(rh, th) {
		return false
	}
	if rp == "" {
		rp = defaultPort(target.Scheme)
	}
	if tp == "" {
		tp = defaultPort(target.Scheme)
	}
	return rp == tp
}

func defaultPort(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

// splitHostPort splits "host:port" / "[v6]:port" / "host" without erroring.
func splitHostPort(hostPort string) (string, string) {
	if h, p, err := net.SplitHostPort(hostPort); err == nil {
		return h, p
	}
	return hostPort, ""
}

// tokenResponse is the JSON body of a token (OAuth2 www-authenticate) grant.
type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// tokenCache caches bearer tokens per challenge (realm+service+scope) with
// expiry, so a session does not re-authenticate on every request.
type tokenCache struct {
	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

func newTokenCache() *tokenCache {
	return &tokenCache{tokens: map[string]cachedToken{}}
}

func (c *tokenCache) key(realm, service, scope string) string {
	return realm + "\x00" + service + "\x00" + scope
}

func (c *tokenCache) get(realm, service, scope string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.tokens[c.key(realm, service, scope)]
	if !ok {
		return "", false
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		delete(c.tokens, c.key(realm, service, scope))
		return "", false
	}
	return entry.token, true
}

func (c *tokenCache) set(realm, service, scope, token string, ttl time.Duration) {
	var expiresAt time.Time
	if ttl > 0 {
		// Refresh a little early: a token that expires the moment it is
		// issued is worse than one extra round trip.
		expiresAt = time.Now().Add(ttl - tokenRefreshSlack(ttl))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[c.key(realm, service, scope)] = cachedToken{token: token, expiresAt: expiresAt}
}

func (c *tokenCache) invalidate(realm, service, scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, c.key(realm, service, scope))
}

func tokenRefreshSlack(ttl time.Duration) time.Duration {
	slack := ttl / 10
	if slack > 10*time.Second {
		slack = 10 * time.Second
	}
	return slack
}

// authTransport is an http.RoundTripper implementing the OCI distribution
// auth flow on top of a base transport:
//
//   - a stored RegistryToken is attached to every request;
//   - on 401 with a Basic challenge, the request is retried once with basic
//     auth (when material exists);
//   - on 401 with a Bearer challenge, a token is fetched from the challenged
//     realm (per-scope cache, credential attached only when the credential
//     policy allows that realm) and the request is retried once.
//
// A 401 after a fresh token is final and returned to the caller.
type authTransport struct {
	base   http.RoundTripper
	cred   *Credential
	policy credentialPolicy
	tokens *tokenCache
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.base == nil {
		return nil, errors.New("registry: auth transport has no base transport")
	}

	// A ready registry token short-circuits every flow.
	if t.cred.hasRegistryToken() {
		return t.base.RoundTrip(withBearer(req, t.cred.RegistryToken))
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	challenges := parseChallenges(resp.Header.Values("Www-Authenticate"))
	if len(challenges) == 0 {
		return resp, nil
	}

	if c, ok := bearerChallenge(challenges); ok {
		return t.roundTripBearer(req, resp, c)
	}
	if basicChallenge(challenges) && t.cred.hasBasicAuth() {
		resp.Body.Close()
		return t.base.RoundTrip(withBasicAuth(req, t.cred.Username, t.cred.Password))
	}
	return resp, nil
}

// roundTripBearer exchanges the 401 for a bearer token and retries once.
func (t *authTransport) roundTripBearer(req *http.Request, unauthorized *http.Response, c challenge) (*http.Response, error) {
	realm := c.params["realm"]
	if realm == "" {
		return unauthorized, nil
	}
	if !strings.Contains(realm, "://") {
		if req.URL != nil && req.URL.Scheme != "" {
			realm = req.URL.Scheme + "://" + realm
		} else {
			realm = "https://" + realm
		}
	}
	realmURL, err := url.Parse(realm)
	if err != nil {
		// A realm we cannot parse can never receive credentials; surface
		// the malformed challenge instead of an unrelated failure later.
		unauthorized.Body.Close()
		return nil, fmt.Errorf("registry: parse token realm: %w", err)
	}

	service := c.params["service"]
	scope := c.params["scope"]

	token, ok := t.tokens.get(realm, service, scope)
	if ok {
		resp, err := t.base.RoundTrip(withBearer(req, token))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized {
			return resp, nil
		}
		// A cached token may have been revoked server-side; drop it and do
		// one fresh exchange below.
		resp.Body.Close()
		t.tokens.invalidate(realm, service, scope)
	}

	token, ttl, err := t.fetchToken(req.Context(), realmURL, service, scope)
	if err != nil {
		// Some registries (e.g. Quay) refuse anonymous or unscoped token
		// requests. Return the original 401 so callers can tell an
		// unauthorized request from an unreachable registry.
		return unauthorized, nil
	}

	t.tokens.set(realm, service, scope, token, ttl)
	unauthorized.Body.Close()
	return t.base.RoundTrip(withBearer(req, token))
}

// fetchToken performs the token request against realmURL. The credential is
// attached only when the credential policy allows the realm; otherwise the
// request proceeds anonymously (better an unauthorized ping than a leaked
// credential). The realm is only ever reached via a registry's own challenge,
// which is what makes the cross-host rule meaningful.
func (t *authTransport) fetchToken(ctx context.Context, realmURL *url.URL, service, scope string) (string, time.Duration, error) {
	q := realmURL.Query()
	if service != "" {
		q.Set("service", service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	tokenURL := *realmURL
	tokenURL.RawQuery = q.Encode()

	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", 0, fmt.Errorf("registry: new token request: %w", err)
	}
	tokenReq.Header.Set("Accept", "application/json")

	// Attach credential material only when the policy allows this realm;
	// a refusal downgrades the request to anonymous rather than leaking.
	if t.policy.maySendCredentialTo(realmURL, true) == nil {
		switch {
		case t.cred.hasIdentityToken():
			tokenReq.Header.Set("Authorization", "Bearer "+t.cred.IdentityToken)
		case t.cred.hasBasicAuth():
			tokenReq.SetBasicAuth(t.cred.Username, t.cred.Password)
		}
	}

	resp, err := t.base.RoundTrip(tokenReq)
	if err != nil {
		return "", 0, fmt.Errorf("registry: token endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// The body is the registry's own error text, never ours to leak; it
		// cannot contain the credential because it was not sent unless the
		// policy allowed it.
		return "", 0, fmt.Errorf("registry: token endpoint returned %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return "", 0, fmt.Errorf("registry: decode token response: %w", err)
	}
	token := parsed.Token
	if token == "" {
		token = parsed.AccessToken
	}
	if token == "" {
		return "", 0, errors.New("registry: token endpoint returned an empty token")
	}
	return token, time.Duration(parsed.ExpiresIn) * time.Second, nil
}

func withBearer(req *http.Request, token string) *http.Request {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	cloned.Header.Set("Authorization", "Bearer "+token)
	return cloned
}

func withBasicAuth(req *http.Request, username, password string) *http.Request {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	cloned.SetBasicAuth(username, password)
	return cloned
}
