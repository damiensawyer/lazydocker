package registry

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/lazydocker/pkg/registry/registrytest"
)

// pingCtx gives every ping a hard deadline so a broken fallback loop fails
// the test instead of hanging it.
func pingCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustPing(t *testing.T, c *Client) PingResult {
	t.Helper()
	res, err := c.Ping(pingCtx(t))
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	return res
}

func TestNewClientNormalizeHost(t *testing.T) {
	tests := []struct {
		in           string
		wantHost     string
		wantScheme   string
		wantExplicit bool
		wantErr      bool
	}{
		{in: "registry.io", wantHost: "registry.io", wantScheme: "https"},
		{in: "registry.io:5000", wantHost: "registry.io:5000", wantScheme: "https"},
		{in: "https://registry.io", wantHost: "registry.io", wantScheme: "https", wantExplicit: true},
		{in: "http://localhost:5000", wantHost: "localhost:5000", wantScheme: "http", wantExplicit: true},
		{in: "[::1]:5000", wantHost: "[::1]:5000", wantScheme: "https"},
		{in: "  registry.io  ", wantHost: "registry.io", wantScheme: "https"},
		{in: "", wantErr: true},
		{in: "https://registry.io/v2/", wantErr: true},
		{in: "https://user:pass@registry.io", wantErr: true},
		{in: "a:b:c", wantErr: true},
	}
	for _, tt := range tests {
		host, scheme, explicit, err := normalizeHost(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("normalizeHost(%q) expected error, got %q %q", tt.in, host, scheme)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeHost(%q): %v", tt.in, err)
			continue
		}
		if host != tt.wantHost || scheme != tt.wantScheme || explicit != tt.wantExplicit {
			t.Errorf("normalizeHost(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, host, scheme, explicit, tt.wantHost, tt.wantScheme, tt.wantExplicit)
		}
	}
}

func TestPingAnonymous(t *testing.T) {
	s := registrytest.New(registrytest.Config{})
	defer s.Close()

	c, err := NewClient(s.Host(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusOnline {
		t.Errorf("status = %q, want online", res.Status)
	}
	if res.Duration <= 0 {
		t.Error("duration must be positive")
	}
}

func TestPingOfflineUnreachable(t *testing.T) {
	// Port 1 on loopback: connection refused immediately, both schemes.
	c, err := NewClient("127.0.0.1:1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusOffline {
		t.Errorf("status = %q, want offline", res.Status)
	}
	if res.Duration <= 0 {
		t.Error("duration must be positive even when offline")
	}
}

func TestPingBasicAuth(t *testing.T) {
	cfg := registrytest.Config{Auth: registrytest.AuthBasic, Username: "alice", Password: "wonderland"}

	t.Run("no credential is unauthorized after one try", func(t *testing.T) {
		s := registrytest.New(cfg)
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		res := mustPing(t, c)
		if res.Status != StatusUnauthorized {
			t.Errorf("status = %q, want unauthorized", res.Status)
		}
		// Without material there is nothing to retry with.
		if got := len(s.Requests()); got != 1 {
			t.Errorf("expected exactly 1 request without credential, got %d", got)
		}
	})

	t.Run("good credential retries once with basic auth", func(t *testing.T) {
		s := registrytest.New(cfg)
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{Credential: &Credential{Username: "alice", Password: "wonderland"}})
		res := mustPing(t, c)
		if res.Status != StatusOnline {
			t.Errorf("status = %q, want online", res.Status)
		}
		reqs := s.Requests()
		if len(reqs) != 2 || reqs[1].Authorization != "basic" || reqs[1].BasicUsername != "alice" {
			t.Errorf("expected retry with basic auth for alice, got %+v", reqs)
		}
	})

	t.Run("bad credential is unauthorized", func(t *testing.T) {
		s := registrytest.New(cfg)
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{Credential: &Credential{Username: "alice", Password: "nope"}})
		res := mustPing(t, c)
		if res.Status != StatusUnauthorized {
			t.Errorf("status = %q, want unauthorized", res.Status)
		}
	})
}

func TestPingBearerAuth(t *testing.T) {
	cfg := registrytest.Config{Auth: registrytest.AuthBearer, Username: "alice", Password: "wonderland"}

	t.Run("anonymous token", func(t *testing.T) {
		s := registrytest.New(cfg)
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		res := mustPing(t, c)
		if res.Status != StatusOnline {
			t.Errorf("status = %q, want online", res.Status)
		}
		toks := s.TokenRequests()
		if len(toks) != 1 || toks[0].Authorization != "" {
			t.Errorf("expected one anonymous token request, got %+v", toks)
		}
	})

	t.Run("good credential reaches the realm as basic auth", func(t *testing.T) {
		s := registrytest.New(cfg)
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{Credential: &Credential{Username: "alice", Password: "wonderland"}})
		res := mustPing(t, c)
		if res.Status != StatusOnline {
			t.Errorf("status = %q, want online", res.Status)
		}
		toks := s.TokenRequests()
		if len(toks) != 1 || toks[0].Authorization != "basic" || toks[0].BasicUsername != "alice" {
			t.Errorf("expected token request with alice's basic auth, got %+v", toks)
		}
	})

	t.Run("bad credential is unauthorized", func(t *testing.T) {
		s := registrytest.New(cfg)
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{Credential: &Credential{Username: "alice", Password: "nope"}})
		res := mustPing(t, c)
		if res.Status != StatusUnauthorized {
			t.Errorf("status = %q, want unauthorized", res.Status)
		}
		// The realm was asked (with the bad credential) and refused.
		toks := s.TokenRequests()
		if len(toks) != 1 || toks[0].Authorization != "basic" {
			t.Errorf("expected one basic-auth token request, got %+v", toks)
		}
	})

	t.Run("tokens are cached per client", func(t *testing.T) {
		s := registrytest.New(cfg)
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{Credential: &Credential{Username: "alice", Password: "wonderland"}})
		for i := 0; i < 3; i++ {
			if res := mustPing(t, c); res.Status != StatusOnline {
				t.Fatalf("ping %d: status = %q, want online", i, res.Status)
			}
		}
		if got := s.TokensIssued(); got != 1 {
			t.Errorf("tokens issued = %d, want 1 (cached after first ping)", got)
		}
		if got := len(s.TokenRequests()); got != 1 {
			t.Errorf("token requests = %d, want 1", got)
		}
	})
}

func TestPingRegistryTokenShortcut(t *testing.T) {
	s := registrytest.New(registrytest.Config{Auth: registrytest.AuthBearer, Username: "alice", Password: "wonderland"})
	defer s.Close()

	// A ready registry token is attached directly; the realm is never
	// contacted. This token was never issued, so the registry refuses it.
	c, _ := NewClient(s.Host(), Options{Credential: &Credential{RegistryToken: "registrytest-token/never-issued"}})
	res := mustPing(t, c)
	if res.Status != StatusUnauthorized {
		t.Errorf("status = %q, want unauthorized", res.Status)
	}
	if got := s.TokensIssued(); got != 0 {
		t.Errorf("tokens issued = %d, want 0 (realm must not be contacted)", got)
	}
	if got := len(s.TokenRequests()); got != 0 {
		t.Errorf("token requests = %d, want 0", got)
	}
}

func TestPingIdentityToken(t *testing.T) {
	s := registrytest.New(registrytest.Config{Auth: registrytest.AuthBearer, Username: "alice", Password: "wonderland"})
	defer s.Close()

	c, _ := NewClient(s.Host(), Options{Credential: &Credential{IdentityToken: "identity-secret"}})
	res := mustPing(t, c)
	if res.Status != StatusOnline {
		t.Errorf("status = %q, want online", res.Status)
	}
	toks := s.TokenRequests()
	if len(toks) != 1 || toks[0].Authorization != "bearer" {
		t.Errorf("expected one token request carrying the identity token as bearer, got %+v", toks)
	}
	// The identity token itself is never recorded.
	for _, r := range s.Requests() {
		if strings.Contains(fmt.Sprint(r), "identity-secret") {
			t.Error("identity token leaked into recorded requests")
		}
	}
}

// TestPingLoopbackHTTPFallback pins the docker behaviour: a loopback
// registry is tried over https first and falls back to plain http.
func TestPingLoopbackHTTPFallback(t *testing.T) {
	s := registrytest.New(registrytest.Config{}) // plain http server
	defer s.Close()

	c, err := NewClient(s.Host(), Options{}) // no scheme: https first
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusOnline {
		t.Errorf("status = %q, want online (https must fall back to http on loopback)", res.Status)
	}
}

// TestPingInsecureNonLoopbackFallback: Options.Insecure extends the http
// fallback to non-loopback hosts. Routing uses a dialer pinned to loopback
// so the test never leaves the machine.
func TestPingInsecureNonLoopbackFallback(t *testing.T) {
	s := registrytest.New(registrytest.Config{})
	defer s.Close()

	c, err := NewClient("reg.example.com"+portOf(t, s.Host()), Options{
		Insecure:   true,
		HTTPClient: &http.Client{Transport: pinnedDialTransport(t, s.Host(), true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusOnline {
		t.Errorf("status = %q, want online (insecure registry must fall back to http)", res.Status)
	}
}

// TestPingExplicitHTTPSPinned: an explicit https:// scheme never falls back
// to http, even on loopback.
func TestPingExplicitHTTPSPinned(t *testing.T) {
	s := registrytest.New(registrytest.Config{}) // plain http server
	defer s.Close()

	c, err := NewClient("https://"+s.Host(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusOffline {
		t.Errorf("status = %q, want offline (explicit https must not fall back to http)", res.Status)
	}
}

// TestPingCustomCA exercises the certs.d flow: a per-host CA under
// <CertsDir>/<host>/ca.crt is trusted for a registry with a private CA.
func TestPingCustomCA(t *testing.T) {
	ca, leaf, key := generateCAAndLeaf(t)
	cert, err := tls.X509KeyPair(leaf, key)
	if err != nil {
		t.Fatal(err)
	}

	s := registrytest.New(registrytest.Config{TLS: &tls.Config{Certificates: []tls.Certificate{cert}}})
	defer s.Close()

	t.Run("with ca.crt in certs dir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, s.Host()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, s.Host(), "ca.crt"), ca, 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := NewClient(s.Host(), Options{CertsDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if res := mustPing(t, c); res.Status != StatusOnline {
			t.Errorf("status = %q, want online via custom CA", res.Status)
		}
	})

	t.Run("without the CA the registry is unreachable over https", func(t *testing.T) {
		c, err := NewClient(s.Host(), Options{CertsDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if res := mustPing(t, c); res.Status != StatusOffline {
			t.Errorf("status = %q, want offline (unknown CA must fail, http fallback must fail against TLS)", res.Status)
		}
	})

	t.Run("malformed ca.crt is a construction error", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, s.Host()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, s.Host(), "ca.crt"), []byte("not a certificate"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := NewClient(s.Host(), Options{CertsDir: dir}); err == nil {
			t.Error("NewClient must reject a malformed ca.crt")
		}
	})
}

// TestBearerCrossHostRealmAllowed is the Docker Hub shape: the registry
// challenges with a token realm on a DIFFERENT host than the registry
// (here 127.0.0.1 vs localhost over a trusted CA), and because the realm
// was named in the registry's own challenge the credential may be sent.
func TestBearerCrossHostRealmAllowed(t *testing.T) {
	ca, leaf, key := generateCAAndLeaf(t)
	cert, err := tls.X509KeyPair(leaf, key)
	if err != nil {
		t.Fatal(err)
	}

	s := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBearer,
		Username: "alice",
		Password: "wonderland",
		TLS:      &tls.Config{Certificates: []tls.Certificate{cert}},
	})
	defer s.Close()
	// The realm is localhost:<port> while the registry is contacted as
	// 127.0.0.1:<port> — a different host, named by the registry's own
	// challenge.
	s.SetRealmBaseURL("https://localhost" + portOf(t, s.Host()))

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, s.Host()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, s.Host(), "ca.crt"), ca, 0o644); err != nil {
		t.Fatal(err)
	}

	// Registry host is 127.0.0.1:<port>; the realm is localhost:<port> —
	// a different host, allowed only because the challenge named it.
	c, err := NewClient(s.Host(), Options{
		CertsDir:   dir,
		Credential: &Credential{Username: "alice", Password: "wonderland"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusOnline {
		t.Errorf("status = %q, want online", res.Status)
	}
	toks := s.TokenRequests()
	if len(toks) != 1 || toks[0].Authorization != "basic" || toks[0].BasicUsername != "alice" {
		t.Errorf("credential must be sent to the challenged cross-host realm, got %+v", toks)
	}
}

// TestBearerPlainHTTPRealmRefused pins refusal #2 end to end: a realm on
// plain http at a non-loopback host never receives the credential — the
// token request goes out anonymously instead. The non-loopback host is
// routed to loopback by a pinned dialer, so no packet leaves the machine.
func TestBearerPlainHTTPRealmRefused(t *testing.T) {
	s := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBearer,
		Username: "alice",
		Password: "wonderland",
	})
	defer s.Close()
	s.SetRealmBaseURL("http://auth.example.com" + portOf(t, s.Host()))

	hostPort := s.Host()
	c, err := NewClient("http://reg.example.com"+portOf(t, hostPort), Options{
		Credential: &Credential{Username: "alice", Password: "wonderland"},
		HTTPClient: &http.Client{Transport: pinnedDialTransport(t, hostPort, false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := mustPing(t, c)
	if res.Status != StatusOnline {
		t.Errorf("status = %q, want online via anonymous token", res.Status)
	}
	toks := s.TokenRequests()
	if len(toks) != 1 {
		t.Fatalf("token requests = %d, want 1", len(toks))
	}
	if toks[0].Authorization != "" {
		t.Errorf("credential must NOT be sent to a plain-http non-loopback realm, got authorization %q", toks[0].Authorization)
	}
}

func TestPingCancelledContext(t *testing.T) {
	s := registrytest.New(registrytest.Config{})
	defer s.Close()

	c, _ := NewClient(s.Host(), Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Ping(ctx); err == nil {
		t.Error("ping with cancelled context must return an error")
	}
}

func TestCredentialNeverInErrors(t *testing.T) {
	s := registrytest.New(registrytest.Config{Auth: registrytest.AuthBearer, Username: "alice", Password: "wonderland"})
	defer s.Close()

	cred := &Credential{Username: "alice", Password: "supersecret"}
	c, _ := NewClient(s.Host(), Options{Credential: cred})
	res := mustPing(t, c) // unauthorized: bad password for the realm
	if res.Status != StatusUnauthorized {
		t.Fatalf("status = %q, want unauthorized", res.Status)
	}
	if strings.Contains(fmt.Sprint(res), "supersecret") {
		t.Error("ping result leaked the password")
	}
	if strings.Contains(fmt.Sprint(cred), "supersecret") {
		t.Error("credential rendering leaked the password")
	}
}

// portOf extracts ":<port>" from a host:port string.
func portOf(t *testing.T, hostPort string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("split %q: %v", hostPort, err)
	}
	return ":" + port
}

// pinnedDialTransport returns a transport that dials every address to
// target (a loopback host:port), letting tests use non-loopback host NAMES
// (reg.example.com, auth.example.com) without any network or DNS access.
func pinnedDialTransport(t *testing.T, target string, insecureTLS bool) *http.Transport {
	t.Helper()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", target)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureTLS},
	}
}

// generateCAAndLeaf makes a private CA plus a server certificate valid for
// localhost, 127.0.0.1 and ::1, as PEM blocks.
func generateCAAndLeaf(t *testing.T) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "registrytest-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	return caPEM, certPEM, keyPEM
}
