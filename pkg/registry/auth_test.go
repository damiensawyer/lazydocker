package registry

import (
	"net/url"
	"testing"
	"time"
)

func TestParseChallenges(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		want    []challenge
	}{
		{
			name:    "bearer with realm, service and scope",
			headers: []string{`Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:foo/bar:pull"`},
			want: []challenge{
				{scheme: "bearer", params: map[string]string{
					"realm":   "https://auth.example.com/token",
					"service": "registry.example.com",
					"scope":   "repository:foo/bar:pull",
				}},
			},
		},
		{
			name:    "unquoted values",
			headers: []string{`Bearer realm=https://auth.example.com/token,service=registry.example.com`},
			want: []challenge{
				{scheme: "bearer", params: map[string]string{
					"realm":   "https://auth.example.com/token",
					"service": "registry.example.com",
				}},
			},
		},
		{
			name:    "quoted comma inside a value",
			headers: []string{`Bearer realm="https://auth.example.com/token?a=1,b=2",scope="repository:a/b:pull,push"`},
			want: []challenge{
				{scheme: "bearer", params: map[string]string{
					"realm": "https://auth.example.com/token?a=1,b=2",
					"scope": "repository:a/b:pull,push",
				}},
			},
		},
		{
			name:    "multiple challenges on one header",
			headers: []string{`Basic realm="x",Bearer realm="https://auth.example.com/token"`},
			want: []challenge{
				{scheme: "basic", params: map[string]string{"realm": "x"}},
				{scheme: "bearer", params: map[string]string{"realm": "https://auth.example.com/token"}},
			},
		},
		{
			name:    "multiple header values",
			headers: []string{`Basic realm="x"`, `Bearer realm="https://auth.example.com/token"`},
			want: []challenge{
				{scheme: "basic", params: map[string]string{"realm": "x"}},
				{scheme: "bearer", params: map[string]string{"realm": "https://auth.example.com/token"}},
			},
		},
		{
			name:    "bare basic scheme",
			headers: []string{`Basic`},
			want:    []challenge{{scheme: "basic", params: map[string]string{}}},
		},
		{
			name:    "case-insensitive scheme, lower-cased keys",
			headers: []string{`BEARER Realm="https://auth.example.com/token"`},
			want: []challenge{
				{scheme: "bearer", params: map[string]string{"realm": "https://auth.example.com/token"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseChallenges(tt.headers)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d challenges, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i].scheme != tt.want[i].scheme {
					t.Errorf("challenge %d scheme = %q, want %q", i, got[i].scheme, tt.want[i].scheme)
				}
				for k, v := range tt.want[i].params {
					if got[i].params[k] != v {
						t.Errorf("challenge %d param %q = %q, want %q", i, k, got[i].params[k], v)
					}
				}
			}
		})
	}
}

func TestBearerChallengePreferred(t *testing.T) {
	cs := parseChallenges([]string{`Basic realm="x",Bearer realm="https://auth.example.com/token"`})
	c, ok := bearerChallenge(cs)
	if !ok || c.params["realm"] != "https://auth.example.com/token" {
		t.Fatalf("bearer challenge not found/preferred: %+v", c)
	}
	if !basicChallenge(cs) {
		t.Error("basic challenge should also be recognized")
	}
	if _, ok := bearerChallenge(parseChallenges([]string{`Basic realm="x"`})); ok {
		t.Error("no bearer challenge expected")
	}
}

// TestCredentialPolicyCrossHostRealm pins refusal #1: a credential may not be
// sent to a token realm on a different host than the registry, unless that
// realm was named in the registry's own WWW-Authenticate challenge (the
// Docker Hub pattern: registry-1.docker.io -> auth.docker.io).
func TestCredentialPolicyCrossHostRealm(t *testing.T) {
	p := credentialPolicy{registryHost: "registry.example.com:5000"}

	realm, err := url.Parse("https://tokens.example.com/token")
	if err != nil {
		t.Fatal(err)
	}

	// REFUSAL: different host, not via the registry's own challenge.
	if err := p.maySendCredentialTo(realm, false); err == nil {
		t.Error("credential must be refused for a cross-host realm not named by the registry's own challenge")
	}

	// Allowed: different host, but named in the registry's own challenge.
	if err := p.maySendCredentialTo(realm, true); err != nil {
		t.Errorf("hub-style cross-host realm from own challenge must be allowed: %v", err)
	}

	// Allowed: same host (any port spelling that resolves to it).
	for _, same := range []string{
		"https://registry.example.com:5000/token",
		"https://registry.example.com:5000/",
	} {
		u, err := url.Parse(same)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.maySendCredentialTo(u, false); err != nil {
			t.Errorf("same-host target %s must be allowed: %v", same, err)
		}
	}
}

// TestCredentialPolicyPlainHTTP pins refusal #2: credentials never travel
// over plain http to a non-loopback host — even when the registry itself
// named the realm.
func TestCredentialPolicyPlainHTTP(t *testing.T) {
	p := credentialPolicy{registryHost: "registry.example.com"}

	for _, raw := range []string{
		"http://tokens.example.com/token",
		"http://registry.example.com/token",
		"http://registry.example.com:5000/token",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.maySendCredentialTo(u, true); err == nil {
			t.Errorf("credential must be refused over plain http to non-loopback %s", raw)
		}
	}

	// Loopback plain http stays allowed: that is how localhost registries
	// and their token realms legitimately work. Each target is judged for
	// the registry on the same host.
	for _, raw := range []string{
		"http://localhost/token",
		"http://localhost:5000/token",
		"http://127.0.0.1:5000/token",
		"http://[::1]:5000/token",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		p := credentialPolicy{registryHost: u.Host}
		if err := p.maySendCredentialTo(u, false); err != nil {
			t.Errorf("loopback plain-http target %s must be allowed: %v", raw, err)
		}
	}

	// Non-http(s) schemes and nil never receive credentials.
	if err := p.maySendCredentialTo(nil, true); err == nil {
		t.Error("nil target must be refused")
	}
	if u, _ := url.Parse("ftp://tokens.example.com/token"); p.maySendCredentialTo(u, true) == nil {
		t.Error("non-http(s) target must be refused")
	}
}

func TestSameHostPortDefaultPorts(t *testing.T) {
	tests := []struct {
		registry string
		target   string
		want     bool
	}{
		{"example.com", "https://example.com/", true},
		{"example.com", "https://example.com:443/", true},
		{"example.com:443", "https://example.com/", true},
		{"example.com", "https://example.com:5000/", false},
		{"example.com:5000", "http://example.com:5000/", true},
		{"example.com", "https://other.example.com/", false},
		{"localhost", "http://localhost:80/", true},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.target)
		if err != nil {
			t.Fatal(err)
		}
		if got := sameHostPort(tt.registry, u); got != tt.want {
			t.Errorf("sameHostPort(%q, %s) = %v, want %v", tt.registry, tt.target, got, tt.want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	yes := []string{"localhost", "LOCALHOST", "localhost.", "localhost:5000", "127.0.0.1", "127.0.0.1:5000", "127.255.0.1:5000", "[::1]:5000", "::1"}
	no := []string{"example.com", "example.com:5000", "10.0.0.1", "172.17.0.1:5000", "localhost.evil.com", ""}
	for _, h := range yes {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range no {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}

func TestTokenCache(t *testing.T) {
	tc := newTokenCache()

	if _, ok := tc.get("r", "s", "sc"); ok {
		t.Fatal("empty cache must miss")
	}

	tc.set("r", "s", "sc", "tok1", time.Minute, false)
	if entry, ok := tc.get("r", "s", "sc"); !ok || entry.token != "tok1" {
		t.Fatalf("get after set = %q, %v, want tok1", entry.token, ok)
	}
	if entry, _ := tc.get("r", "s", "sc"); entry.derived {
		t.Error("token cached as derived without credential exchange")
	}

	// Different scope is a different cache entry.
	if _, ok := tc.get("r", "s", "other-scope"); ok {
		t.Fatal("different scope must miss")
	}

	tc.invalidate("r", "s", "sc")
	if _, ok := tc.get("r", "s", "sc"); ok {
		t.Fatal("invalidated entry must miss")
	}

	// The derived flag rides with the entry and decides where a token may
	// later be attached.
	tc.set("r", "s", "sc", "tok2", time.Minute, true)
	if entry, ok := tc.get("r", "s", "sc"); !ok || !entry.derived {
		t.Fatalf("get after derived set = %+v, %v, want derived", entry, ok)
	}
}

func TestTokenCacheExpiry(t *testing.T) {
	tc := newTokenCache()
	// A short ttl minus its refresh slack expires almost immediately.
	tc.set("r", "s", "sc", "tok", 20*time.Millisecond, false)
	if _, ok := tc.get("r", "s", "sc"); !ok {
		t.Fatal("fresh short-ttl token must still be valid")
	}
	time.Sleep(30 * time.Millisecond)
	if _, ok := tc.get("r", "s", "sc"); ok {
		t.Fatal("expired token must miss")
	}

	tc.set("r", "s", "sc", "tok", time.Hour, false)
	if _, ok := tc.get("r", "s", "sc"); !ok {
		t.Fatal("long-ttl token must be valid")
	}
}
