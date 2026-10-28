// Package registrytest provides an in-process fake OCI registry for tests:
// an httptest.Server speaking enough of the distribution spec (/v2/ ping,
// basic auth, bearer-token realm with per-scope tokens, /v2/_catalog,
// /v2/<name>/tags/list and /v2/<name>/manifests/<ref>) for pkg/registry
// tests to run with zero network and zero docker daemon. Behaviour is
// driven by Config (data, not code), and every request is recorded (scheme
// only — Authorization values are never stored).
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
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
)

// AuthMode selects how the fake registry authenticates requests.
type AuthMode string

const (
	// AuthNone serves everything anonymously (default).
	AuthNone AuthMode = "none"
	// AuthBasic requires HTTP basic auth on /v2/ endpoints.
	AuthBasic AuthMode = "basic"
	// AuthBearer requires a bearer token on /v2/ endpoints; tokens are
	// issued by this server's /token endpoint (or a realm override),
	// bound to the scopes the token request asked for.
	AuthBearer AuthMode = "bearer"
)

// Config drives the fake registry. It is data, not code.
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

	// Repositories is the catalog the server serves: its tags and their
	// manifests. The catalog lists the repository names sorted (as
	// docker's registry does).
	Repositories []Repository

	// CatalogPageSize, when > 0, forces /v2/_catalog pages of at most this
	// many entries (with a Link rel="next" while more remain), whatever n
	// the client asked for — server-forced pagination. Zero lets the
	// client's n govern, serving everything at once when it sends no n
	// (docker registry:2's default).
	CatalogPageSize int

	// TagsPageSize does the same for /v2/<name>/tags/list.
	TagsPageSize int
}

// Repository is one repository in the fake registry.
type Repository struct {
	Name string
	// Tags maps tag name to the manifest served at
	// /v2/<name>/manifests/<tag>. Every manifest is also served by digest.
	Tags map[string]Manifest
	// Manifests holds additional untagged manifests (such as the platform
	// children of a multi-arch index) served by digest at
	// /v2/<name>/manifests/<digest> without appearing in /v2/<name>/tags/list.
	Manifests []Manifest
}

// Manifest is what the server serves for a tag (or digest) reference.
type Manifest struct {
	// MediaType is served as Content-Type: an image manifest or index
	// media type (OCI or Docker v2).
	MediaType string
	// Body is served verbatim.
	Body []byte
	// ContentDigest overrides the Docker-Content-Digest header (default:
	// the digest of Body) — for client digest-mismatch handling.
	ContentDigest string
}

// Request is a recorded incoming request. Authorization is reduced to its
// scheme ("basic" or "bearer", "" when absent) and, for basic, the decoded
// username; passwords and token values are never recorded.
type Request struct {
	Method        string
	Path          string
	RawQuery      string
	Accept        string
	Authorization string // "", "basic" or "bearer"
	BasicUsername string // decoded username, only for basic auth
}

// issuedToken is a token this server's realm handed out, with the scopes it
// was bound to.
type issuedToken struct {
	username string
	scopes   map[string]bool
}

// Server is the fake registry. All methods are safe for concurrent use.
type Server struct {
	srv *httptest.Server
	cfg Config

	byName map[string]Repository

	mu         sync.Mutex
	recorded   []Request
	issued     map[string]issuedToken
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
		byName: map[string]Repository{},
		issued: map[string]issuedToken{},
	}
	for _, repo := range cfg.Repositories {
		s.byName[repo.Name] = repo
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
// /v2/ tree (ping, catalog, tags, manifests), the /token realm, and a
// catch-all returning OCI-shaped errors.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", s.handleV2)
	mux.HandleFunc("/token", s.handleToken)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "registrytest: unknown endpoint "+r.URL.Path)
	})
	return s.recordMiddleware(mux)
}

// handleV2 routes the /v2/ tree: the ping, the catalog, tags lists and
// manifest fetches, everything else 404.
func (s *Server) handleV2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOCIError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "registrytest: method not allowed")
		return
	}

	path := r.URL.Path
	switch {
	case path == "/v2/" || path == "/v2":
		if !s.authorize(w, r, "") {
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))

	case path == "/v2/_catalog":
		if !s.authorize(w, r, "registry:catalog:*") {
			return
		}
		s.handleCatalog(w, r)

	default:
		rest := strings.TrimPrefix(path, "/v2/")
		if name, ok := strings.CutSuffix(rest, "/tags/list"); ok && name != "" {
			if !s.authorize(w, r, repositoryScope(name)) {
				return
			}
			s.handleTags(w, r, name)
			return
		}
		if name, ref, ok := strings.Cut(rest, "/manifests/"); ok && name != "" && ref != "" {
			if !s.authorize(w, r, repositoryScope(name)) {
				return
			}
			s.handleManifest(w, r, name, ref)
			return
		}
		writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "registrytest: unknown endpoint "+path)
	}
}

// repositoryScope is the pull scope a repository endpoint demands and names
// in its Bearer challenges.
func repositoryScope(name string) string {
	return "repository:" + name + ":pull"
}

// authorize enforces Config.Auth for one /v2/ request needing the given
// scope ("" for the ping, which needs none). It writes the refusal itself;
// false means the handler must return.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, scope string) bool {
	switch s.cfg.Auth {
	case AuthBasic:
		user, pass, ok := r.BasicAuth()
		if !ok || !s.validBasic(user, pass) {
			w.Header().Set("Www-Authenticate", `Basic realm="registrytest"`)
			writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return false
		}
	case AuthBearer:
		if !s.validBearer(r.Header.Get("Authorization"), scope) {
			challenge := fmt.Sprintf(`Bearer realm=%q,service=%q`, s.RealmURL(), s.Host())
			if scope != "" {
				challenge += fmt.Sprintf(`,scope=%q`, scope)
			}
			w.Header().Set("Www-Authenticate", challenge)
			writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return false
		}
	}
	return true
}

// catalogNames returns the configured repository names, sorted.
func (s *Server) catalogNames() []string {
	names := make([]string, 0, len(s.byName))
	for name := range s.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// handleCatalog serves GET /v2/_catalog, honoring the client's n and the
// server's forced page size, resuming after last, and emitting a Link
// rel="next" while entries remain.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	s.serveNamePage(w, r.URL.Query(), s.catalogNames(), "/v2/_catalog", func(names []string) any {
		return map[string]any{"repositories": names}
	}, s.cfg.CatalogPageSize)
}

// handleTags serves GET /v2/<name>/tags/list for a known repository.
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request, name string) {
	repo, ok := s.byName[name]
	if !ok {
		writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN",
			fmt.Sprintf("repository name not known to registry: %s", name))
		return
	}
	tags := make([]string, 0, len(repo.Tags))
	for tag := range repo.Tags {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	path := "/v2/" + name + "/tags/list"
	s.serveNamePage(w, r.URL.Query(), tags, path, func(page []string) any {
		return map[string]any{"name": name, "tags": page}
	}, s.cfg.TagsPageSize)
}

// serveNamePage slices names into a page and renders it, walking the
// n / last / forced-page-size semantics shared by catalog and tags.
func (s *Server) serveNamePage(w http.ResponseWriter, q url.Values, names []string, path string, render func([]string) any, forcedSize int) {
	pageSize := 0
	if n := q.Get("n"); n != "" {
		if parsed, err := strconv.Atoi(n); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}
	if forcedSize > 0 && (pageSize == 0 || pageSize > forcedSize) {
		pageSize = forcedSize
	}

	// Resume strictly after "last" in the sorted order.
	start := 0
	if last := q.Get("last"); last != "" {
		for i, name := range names {
			if name <= last {
				start = i + 1
			} else {
				break
			}
		}
	}
	end := len(names)
	if pageSize > 0 && start+pageSize < end {
		end = start + pageSize
	}
	page := names[start:end]

	if end < len(names) && len(page) > 0 {
		next := url.Values{}
		next.Set("last", page[len(page)-1])
		if q.Get("n") != "" {
			next.Set("n", q.Get("n"))
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s?%s>; rel="next"`, path, next.Encode()))
	}

	body, err := json.Marshal(render(page))
	if err != nil {
		http.Error(w, "registrytest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func isHead(r *http.Request) bool { return r.Method == http.MethodHead }

// handleManifest serves GET /v2/<name>/manifests/<ref> for a known
// repository, where ref is a tag or a digest.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request, name, ref string) {
	repo, ok := s.byName[name]
	if !ok {
		writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN",
			fmt.Sprintf("repository name not known to registry: %s", name))
		return
	}

	var manifest *Manifest
	if _, err := digest.Parse(ref); err == nil {
		for _, tag := range sortedTagNames(repo.Tags) {
			m := repo.Tags[tag]
			if contentDigest(m) == ref {
				manifest = &m
				break
			}
		}
		if manifest == nil {
			for i := range repo.Manifests {
				m := repo.Manifests[i]
				if contentDigest(m) == ref {
					manifest = &m
					break
				}
			}
		}
	} else if m, isTag := repo.Tags[ref]; isTag {
		manifest = &m
	}
	if manifest == nil {
		writeOCIError(w, http.StatusNotFound, "MANIFEST_UNKNOWN",
			fmt.Sprintf("manifest unknown to registry: %s/%s", name, ref))
		return
	}

	w.Header().Set("Content-Type", manifest.MediaType)
	w.Header().Set("Docker-Content-Digest", contentDigest(*manifest))
	w.WriteHeader(http.StatusOK)
	if isHead(r) {
		return
	}
	_, _ = w.Write(manifest.Body)
}

// contentDigest is a manifest's advertised digest: the override when set,
// else the digest of its body.
func contentDigest(m Manifest) string {
	if m.ContentDigest != "" {
		return m.ContentDigest
	}
	return digest.FromBytes(m.Body).String()
}

func sortedTagNames(tags map[string]Manifest) []string {
	names := make([]string, 0, len(tags))
	for name := range tags {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// handleToken is the bearer realm: it issues scope-bound tokens for valid
// basic auth, 401s for invalid basic auth, and issues anonymous tokens when
// no credential is presented. The requested scopes (comma- or
// space-separated, as token servers accept both) ride with the token and
// gate which /v2/ endpoints accept it.
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

	scopes := map[string]bool{}
	for _, scope := range strings.FieldsFunc(r.URL.Query().Get("scope"), func(ru rune) bool {
		return ru == ',' || ru == ' '
	}) {
		if scope != "" {
			scopes[scope] = true
		}
	}

	token := "registrytest-token/anonymous"
	if username != "" {
		token = "registrytest-token/" + username
	}

	s.mu.Lock()
	// Every grant is its own token (real token servers never hand the same
	// string for two different scopes); the issue counter keeps them
	// distinct so one grant cannot overwrite another's scopes.
	s.issueCount++
	token = fmt.Sprintf("%s/%d", token, s.issueCount)
	s.issued[token] = issuedToken{username: username, scopes: scopes}
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
// issued AND that the token was bound to the scope the endpoint demands
// (any scope satisfies the scope-less ping).
func (s *Server) validBearer(header, requiredScope string) bool {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	issued, known := s.issued[token]
	if !known {
		return false
	}
	return requiredScope == "" || issued.scopes[requiredScope]
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
		Accept:   r.Header.Get("Accept"),
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
