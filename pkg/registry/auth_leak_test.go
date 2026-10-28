package registry

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazydocker/pkg/registry/registrytest"
)

// leakCtx bounds every request in this file so a broken transport loop fails
// the test instead of hanging it.
func leakCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// foreignServer is the second httptest server a registry redirect might land
// on (the CDN/S3 shape). It answers 401s naming its OWN auth realm and
// records every Authorization header it ever sees — the observable of the
// R1 leak tests: it must stay empty.
type foreignServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	auths    []string
	tokenHit bool
}

func newForeignServer(t *testing.T) *foreignServer {
	t.Helper()
	f := &foreignServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			f.mu.Lock()
			f.auths = append(f.auths, auth)
			f.mu.Unlock()
		}
		switch r.URL.Path {
		case "/basic-401":
			w.Header().Set("Www-Authenticate", `Basic realm="foreign-realm"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`))
		case "/bearer-401":
			w.Header().Set("Www-Authenticate",
				fmt.Sprintf(`Bearer realm=%q,service="foreign-service"`, f.srv.URL+"/token"))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`))
		case "/token":
			f.mu.Lock()
			f.tokenHit = true
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"foreign-issued-token","expires_in":300}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *foreignServer) sawAuthorizations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.auths))
	copy(out, f.auths)
	return out
}

func (f *foreignServer) tokenEndpointHit() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenHit
}

// TestRedirectNeverCarriesCredentialToForeignHost pins review finding R1:
// http.Client follows redirects through the same transport, and a request
// redirected from the registry to a second host must not carry ANY of this
// registry's credential material — no stored RegistryToken, no basic retry,
// no token fetched from the foreign host's own WWW-Authenticate realm.
func TestRedirectNeverCarriesCredentialToForeignHost(t *testing.T) {
	foreign := newForeignServer(t)

	// The "registry": /v2/ answers 200; /v2/redirect/<x> redirects to the
	// foreign host; /v2/bearer-protected challenges with a realm on the
	// registry itself (to seed a cached token); /token issues those tokens.
	var reg *httptest.Server
	reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case "/v2/redirect/basic", "/v2/redirect/bearer":
			target := foreign.srv.URL + "/basic-401"
			if r.URL.Path == "/v2/redirect/bearer" {
				target = foreign.srv.URL + "/bearer-401"
			}
			http.Redirect(w, r, target, http.StatusFound)
		case "/v2/bearer-protected":
			if r.Header.Get("Authorization") == "Bearer redirect-test-token" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{}"))
				return
			}
			w.Header().Set("Www-Authenticate",
				fmt.Sprintf(`Bearer realm=%q,service="registry"`, reg.URL+"/token"))
			w.WriteHeader(http.StatusUnauthorized)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"redirect-test-token","expires_in":300}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(reg.Close)

	hostPort := reg.URL[len("http://"):]
	redirectTargets := []string{"/v2/redirect/basic", "/v2/redirect/bearer"}

	// Each credential flavour that could leak onto the redirected request.
	variants := map[string]*Credential{
		"basic":          {Username: "alice", Password: "wonderland"},
		"identity":       {IdentityToken: "identity-secret"},
		"registry token": {RegistryToken: "registry-test-token"},
	}
	for name, cred := range variants {
		t.Run(name, func(t *testing.T) {
			c, err := NewClient(hostPort, Options{Credential: cred})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range redirectTargets {
				resp, err := c.do(leakCtx(t), http.MethodGet, path, nil, nil)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				// The foreign 401 is the final response, passed through
				// verbatim — no retry, no challenge handling.
				if resp.StatusCode != http.StatusUnauthorized {
					resp.Body.Close()
					t.Errorf("GET %s: status = %d, want 401 (foreign refusal passed through)", path, resp.StatusCode)
				}
				resp.Body.Close()
			}
		})
	}

	// The sneakiest variant: a bearer token cached from the registry's OWN
	// realm. The old code honoured the foreign 401's realm (a cache miss)
	// and fetched a token from the FOREIGN realm with the credential
	// attached (fetchToken always claimed viaRegistryChallenge).
	t.Run("cached bearer token", func(t *testing.T) {
		c, err := NewClient(hostPort, Options{
			Credential: &Credential{Username: "alice", Password: "wonderland"},
		})
		if err != nil {
			t.Fatal(err)
		}
		// Seed the cache: one full bearer exchange against the registry.
		resp, err := c.do(leakCtx(t), http.MethodGet, "/v2/bearer-protected", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("seeding request: status = %d, want 200", resp.StatusCode)
		}
		for _, path := range redirectTargets {
			resp, err := c.do(leakCtx(t), http.MethodGet, path, nil, nil)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET %s: status = %d, want 401 (foreign refusal passed through)", path, resp.StatusCode)
			}
		}
	})

	if got := foreign.sawAuthorizations(); len(got) > 0 {
		t.Errorf("foreign host received %d Authorization header(s) via redirect: %v", len(got), got)
	}
	if foreign.tokenEndpointHit() {
		t.Error("the foreign host's token realm was contacted — its challenge must never be honoured")
	}
}

// TestInsecureDowngradeNeverAttachesBasicAuth pins review finding R2 for
// the basic retry: with Options.Insecure a non-loopback request downgraded
// to plain http must not get basic auth attached on retry. The non-loopback
// host name is routed to the loopback fixture by a pinned dialer, so the
// test never leaves the machine.
func TestInsecureDowngradeNeverAttachesBasicAuth(t *testing.T) {
	s := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBasic,
		Username: "alice",
		Password: "wonderland",
	})
	defer s.Close()

	c, err := NewClient("reg.example.com"+portOf(t, s.Host()), Options{
		Insecure:   true,
		HTTPClient: &http.Client{Transport: pinnedDialTransport(t, s.Host(), false)},
		Credential: &Credential{Username: "alice", Password: "wonderland"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusUnauthorized {
		t.Errorf("status = %q, want unauthorized (the basic retry must be refused over downgraded http)", res.Status)
	}
	for _, r := range s.Requests() {
		if r.Authorization == "basic" {
			t.Errorf("basic auth was attached over plain http to a non-loopback host: %+v", r)
		}
	}
}

// TestInsecureDowngradeNeverAttachesCredentialDerivedToken pins review
// finding R2 for bearer tokens: a token obtained by exchanging the
// credential at the (allowed: https, loopback) realm is credential material
// and may not be attached to the request after the insecure https->http
// downgrade of a non-loopback registry. The dialer maps only the registry's
// host name onto the loopback fixture; everything else (the realm) dials its
// real loopback address — no packet leaves the machine.
func TestInsecureDowngradeNeverAttachesCredentialDerivedToken(t *testing.T) {
	// The token realm: an https loopback fixture the policy DOES allow
	// credentials to, so any leak happens at the attach step on the
	// downgraded request, not at the realm.
	realmSrv := registrytest.New(registrytest.Config{
		TLS:      &tls.Config{},
		Username: "alice",
		Password: "wonderland",
	})
	defer realmSrv.Close()

	s := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBearer,
		Username: "alice",
		Password: "wonderland",
	})
	defer s.Close()
	s.SetRealmBaseURL(realmSrv.URL())

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, port, err := net.SplitHostPort(addr); err == nil && host == "reg.example.com" {
				addr = net.JoinHostPort("127.0.0.1", port)
			}
			return dialer.DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	c, err := NewClient("reg.example.com"+portOf(t, s.Host()), Options{
		Insecure:   true,
		HTTPClient: &http.Client{Transport: transport},
		Credential: &Credential{Username: "alice", Password: "wonderland"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusUnauthorized {
		t.Errorf("status = %q, want unauthorized (a credential-derived token must not ride downgraded http)", res.Status)
	}
	// The exchange itself ran — the credential reached the realm it is
	// allowed to reach — proving the refusal happened at the attach step.
	toks := realmSrv.TokenRequests()
	if len(toks) != 1 || toks[0].Authorization != "basic" || toks[0].BasicUsername != "alice" {
		t.Errorf("expected exactly one basic-auth token request at the allowed realm, got %+v", toks)
	}
	// ...but the registry's plain-http endpoint never saw any of it.
	for _, r := range s.Requests() {
		if r.Authorization != "" {
			t.Errorf("credential material reached the downgraded plain-http registry: %+v", r)
		}
	}
}
