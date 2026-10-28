// Package registrytest provides an in-process fake OCI registry for tests:
// an httptest.Server speaking enough of the distribution spec (/v2/ ping,
// basic auth, bearer-token realm) for pkg/registry tests to run with zero
// network and zero docker daemon. Later tasks extend it with catalog, tags,
// manifests and blobs; behaviour is driven by Config, and every request is
// recorded (scheme only — Authorization values are never stored).
package registrytest

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// AuthMode selects how the fake registry authenticates requests.
type AuthMode string

const (
	// AuthNone serves everything anonymously (default).
	AuthNone AuthMode = "none"
	// AuthBasic requires HTTP basic auth on /v2/ endpoints.
	AuthBasic AuthMode = "basic"
	// AuthBearer requires a bearer token on /v2/ endpoints; tokens are
	// issued by this server's /token endpoint (or a realm override).
	AuthBearer AuthMode = "bearer"
)

// Config drives the fake registry. It is data, not code: later phases add
// fields (repositories, tags, manifests, blobs) rather than new servers.
type Config struct {
	// Auth selects the authentication mode (default AuthNone).
	Auth AuthMode

	// Username and Password are the credentials AuthBasic enforces and the
	// ones the /token endpoint of AuthBearer accepts (basic auth on the
	// token request). Empty Username means no credential is ever valid and
	// only anonymous tokens are issued.
	Username string
	Password string

	// RealmBaseURL overrides the realm advertised in Bearer challenges
	// (default: this server's own base URL, i.e. realm "<URL>/token").
	// Point it at a second server (or any URL the client can route to) to
	// exercise cross-host realm behaviour.
	RealmBaseURL string

	// TokenTTL is the expires_in (seconds) advertised with issued tokens.
	// Zero means the default of 300.
	TokenTTL time.Duration

	// TLS, when set, starts the server with TLS using exactly this config
	// (certificates included), so tests can exercise custom CA behaviour.
	TLS *tls.Config
}

// Request is a recorded incoming request. Authorization is reduced to its
// scheme ("basic" or "bearer", "" when absent) and, for basic, the decoded
// username; passwords and token values are never recorded.
type Request struct {
	Method        string
	Path          string
	RawQuery      string
	Authorization string // "", "basic" or "bearer"
	BasicUsername string // decoded username, only for basic auth
}

// Server is the fake registry. All methods are safe for concurrent use.
type Server struct {
	srv *httptest.Server
	cfg Config

	mu         sync.Mutex
	recorded   []Request
	issued     map[string]string // token -> username it identifies ("" = anonymous)
	issueCount int
}

// New starts a fake registry with the given configuration.
func New(cfg Config) *Server {
	if cfg.Auth == "" {
		cfg.Auth = AuthNone
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = 300 * time.Second
	}
	s := &Server{
		cfg:    cfg,
		issued: map[string]string{},
	}
	s.srv = httptest.NewUnstartedServer(s.handler())
	// Clients are expected to probe https against the plain-http port (the
	// loopback fallback) and vice versa; keep that expected noise out of
	// test output.
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if cfg.TLS != nil {
		s.srv.TLS = cfg.TLS
		s.srv.StartTLS()
	} else {
		s.srv.Start()
	}
	return s
}

// Close shuts the server down.
func (s *Server) Close() { s.srv.Close() }

// URL returns the base URL of the fake registry (scheme://host:port).
func (s *Server) URL() string { return s.srv.URL }

// Host returns the host:port to hand to registry.NewClient.
func (s *Server) Host() string {
	return strings.TrimPrefix(strings.TrimPrefix(s.srv.URL, "https://"), "http://")
}

// RealmURL returns the token realm advertised in Bearer challenges.
func (s *Server) RealmURL() string {
	s.mu.Lock()
	base := s.cfg.RealmBaseURL
	s.mu.Unlock()
	if base == "" {
		base = s.srv.URL
	}
	return strings.TrimSuffix(base, "/") + "/token"
}

// SetRealmBaseURL re-points the realm advertised in Bearer challenges — for
// tests that need the realm host to differ from the registry host but only
// know the server's port after it started.
func (s *Server) SetRealmBaseURL(base string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.RealmBaseURL = base
}

// Requests returns a copy of every recorded request, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.recorded))
	copy(out, s.recorded)
	return out
}

// TokenRequests returns the recorded requests to the token endpoint.
func (s *Server) TokenRequests() []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == "/token" {
			out = append(out, r)
		}
	}
	return out
}

// TokensIssued returns how many tokens the realm has issued (anonymous ones
// included) — the observable for client token-cache tests.
func (s *Server) TokensIssued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueCount
}

// handler builds the full mux: recording middleware around the auth-aware
// /v2/ ping, the /token realm, and a catch-all returning OCI-shaped errors
// that later phases replace with real endpoints.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", s.handleV2Root)
	mux.HandleFunc("/token", s.handleToken)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "registrytest: unknown endpoint "+r.URL.Path)
	})
	return s.recordMiddleware(mux)
}

// handleV2Root answers GET /v2/ (and any not-yet-implemented /v2/... path
// with 404, the extension point for catalog/tags/manifests/blobs).
func (s *Server) handleV2Root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v2/" && r.URL.Path != "/v2" {
		writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "registrytest: unknown endpoint "+r.URL.Path)
		return
	}

	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")

	switch s.cfg.Auth {
	case AuthBasic:
		user, pass, ok := r.BasicAuth()
		if !ok || !s.validBasic(user, pass) {
			w.Header().Set("Www-Authenticate", `Basic realm="registrytest"`)
			writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
	case AuthBearer:
		if !s.validBearer(r.Header.Get("Authorization")) {
			w.Header().Set("Www-Authenticate",
				fmt.Sprintf(`Bearer realm=%q,service=%q`, s.RealmURL(), s.Host()))
			writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

// handleToken is the bearer realm: it issues tokens for valid basic auth,
// 401s for invalid basic auth, and issues anonymous tokens when no
// credential is presented.
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	username := ""
	if user, pass, ok := r.BasicAuth(); ok {
		if !s.validBasic(user, pass) {
			w.Header().Set("Www-Authenticate", `Basic realm="registrytest-token"`)
			writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
			return
		}
		username = user
	}

	s.mu.Lock()
	token := "registrytest-token/anonymous"
	if username != "" {
		token = "registrytest-token/" + username
	}
	s.issued[token] = username
	s.issueCount++
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      token,
		"expires_in": int(s.cfg.TokenTTL / time.Second),
	})
}

// validBasic compares in constant time; it runs inside the test process but
// keeping the habit costs nothing.
func (s *Server) validBasic(user, pass string) bool {
	if s.cfg.Username == "" {
		return false
	}
	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Username)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Password)) == 1
	return okUser && okPass
}

// validBearer checks the Authorization header names a token this server
// issued (bearer tokens are accepted in the form "Bearer <token>").
func (s *Server) validBearer(header string) bool {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, issued := s.issued[token]
	return issued
}

// recordMiddleware records every request before it reaches the mux.
func (s *Server) recordMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		next.ServeHTTP(w, r)
	})
}

// record appends the scheme-only view of a request. Never record header
// values: no token, no password, nothing that could leak into a test dump.
func (s *Server) record(r *http.Request) {
	rec := Request{
		Method:   r.Method,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
	}
	if header := r.Header.Get("Authorization"); header != "" {
		switch {
		case strings.HasPrefix(header, "Basic "):
			rec.Authorization = "basic"
			if user, _, ok := r.BasicAuth(); ok {
				rec.BasicUsername = user
			}
		case strings.HasPrefix(header, "Bearer "):
			rec.Authorization = "bearer"
		default:
			rec.Authorization = "other"
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, rec)
}

// writeOCIError renders an OCI distribution-spec error body.
func writeOCIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": code, "message": message, "detail": ""}},
	})
}
