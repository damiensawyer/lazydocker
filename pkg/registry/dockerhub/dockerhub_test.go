package dockerhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazydocker/pkg/registry"
)

// hubHit is one request the fake Hub received.
type hubHit struct {
	Method string
	Path   string // path plus query
	Auth   string // Authorization header value, "" when absent
}

// fakeHub is a scripted Docker Hub: it records every request (method, path,
// Authorization header) and delegates responses to a test-supplied handler.
// It never leaves the process.
type fakeHub struct {
	server *httptest.Server

	mu   sync.Mutex
	hits []hubHit
}

// newFakeHub starts a Hub whose every response comes from respond. The
// server is closed on test cleanup.
func newFakeHub(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *fakeHub {
	t.Helper()
	h := &fakeHub{}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.hits = append(h.hits, hubHit{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
		})
		h.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		respond(w, r)
	}))
	t.Cleanup(h.server.Close)
	return h
}

func (h *fakeHub) url() string { return h.server.URL }

// all returns every recorded hit.
func (h *fakeHub) all() []hubHit {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]hubHit, len(h.hits))
	copy(out, h.hits)
	return out
}

// withPath returns the hits whose path matches exactly.
func (h *fakeHub) withPath(path string) []hubHit {
	var out []hubHit
	for _, hit := range h.all() {
		if hit.Path == path {
			out = append(out, hit)
		}
	}
	return out
}

// logins counts POST /v2/users/login hits.
func (h *fakeHub) logins() int { return len(h.withPath("/v2/users/login")) }

// count is the total number of requests received.
func (h *fakeHub) count() int { return len(h.all()) }

// newTestClient builds a Client for the fake Hub origin.
func newTestClient(t *testing.T, baseURL string, cred *registry.Credential) *Client {
	t.Helper()
	c, err := New(Options{BaseURL: baseURL, Credential: cred})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func mustEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func octocatCred() *registry.Credential {
	return &registry.Credential{Username: "octocat", Password: "hunter2"}
}

func TestNewBaseURLValidation(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string // expected origin, "" means expect an error
	}{
		{name: "empty defaults to the public Hub", baseURL: "", want: "https://hub.docker.com"},
		{name: "https origin", baseURL: "https://hub.example.com", want: "https://hub.example.com"},
		{name: "http origin (tests, local mirrors)", baseURL: "http://127.0.0.1:5000", want: "http://127.0.0.1:5000"},
		{name: "path is dropped, origin kept", baseURL: "https://hub.example.com/v2/", want: "https://hub.example.com"},
		{name: "unsupported scheme", baseURL: "ftp://hub.example.com"},
		{name: "no host", baseURL: "https:///v2/"},
		{name: "unparseable", baseURL: "://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(Options{BaseURL: tt.baseURL})
			if tt.want == "" {
				if err == nil {
					t.Fatalf("expected an error, got client for %s", c.base)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q): %v", tt.baseURL, err)
			}
			mustEqual(t, "base origin", c.base.String(), tt.want)
		})
	}
}

func TestLoginThenListRepositories(t *testing.T) {
	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/":
			fmt.Fprint(w, `{"results":[`+
				`{"name":"one","namespace":"octocat","repository_type":"image","is_private":false},`+
				`{"name":"two","namespace":"octocat","repository_type":"image","is_private":true},`+
				`{"name":"plug","namespace":"octocat","repository_type":"plugin"},`+
				`{"name":"","namespace":"octocat","repository_type":"image"}`+
				`],"next":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"not found"}`)
		}
	})

	repos, err := newTestClient(t, hub.url(), octocatCred()).ListRepositories(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}

	want := []Repository{
		{Name: "one", Namespace: "octocat", Visibility: VisibilityPublic},
		{Name: "two", Namespace: "octocat", Visibility: VisibilityPrivate},
	}
	if len(repos) != len(want) {
		t.Fatalf("got %d repos (%v), want %d", len(repos), repos, len(want))
	}
	for i, r := range repos {
		if r != want[i] {
			t.Errorf("repo[%d]: got %+v, want %+v", i, r, want[i])
		}
	}

	// The login request carries the password in its JSON body only — never
	// an Authorization header.
	for _, hit := range hub.withPath("/v2/users/login") {
		mustEqual(t, "login Authorization", hit.Auth, "")
	}
	// Every repository-list request carries the JWT as a bearer token.
	listHits := hub.withPath("/v2/repositories/octocat/")
	if len(listHits) != 1 {
		t.Fatalf("got %d list requests, want 1", len(listHits))
	}
	mustEqual(t, "list Authorization", listHits[0].Auth, "Bearer jwt-abc-123")
}

func TestTokenIsCachedAcrossCalls(t *testing.T) {
	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/", "/v2/repositories/octocat/app/tags/":
			fmt.Fprint(w, `{"results":[],"next":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	c := newTestClient(t, hub.url(), octocatCred())

	for i := 0; i < 2; i++ {
		if _, err := c.ListRepositories(context.Background(), "octocat"); err != nil {
			t.Fatalf("ListRepositories #%d: %v", i+1, err)
		}
		if _, err := c.ListTags(context.Background(), "octocat", "app"); err != nil {
			t.Fatalf("ListTags #%d: %v", i+1, err)
		}
	}
	mustEqual(t, "logins", hub.logins(), 1)
}

func TestListRepositoriesPaginates(t *testing.T) {
	var hub *fakeHub
	hub = newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"results":[{"name":"three","namespace":"octocat","repository_type":"image"}],"next":null}`)
				return
			}
			next := `"` + hub.url() + `/v2/repositories/octocat/?page=2&page_size=100"`
			fmt.Fprint(w, `{"results":[{"name":"one","namespace":"octocat","repository_type":"image"},`+
				`{"name":"two","namespace":"octocat","repository_type":"image"}],"next":`+next+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	repos, err := newTestClient(t, hub.url(), octocatCred()).ListRepositories(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	var names []string
	for _, r := range repos {
		names = append(names, r.Name)
	}
	if fmt.Sprint(names) != "[one two three]" {
		t.Errorf("got repos %v, want [one two three]", names)
	}
	mustEqual(t, "list requests", len(hub.withPath("/v2/repositories/octocat/")), 2)
}

func TestListRepositoriesAnonymous(t *testing.T) {
	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/repositories/octocat/" {
			fmt.Fprint(w, `{"results":[{"name":"one","namespace":"octocat","repository_type":"image"}],"next":null}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	repos, err := newTestClient(t, hub.url(), nil).ListRepositories(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "one" || repos[0].Visibility != VisibilityPublic {
		t.Fatalf("got %+v, want one public repo", repos)
	}
	mustEqual(t, "total requests (no login expected)", hub.count(), 1)
	for _, hit := range hub.all() {
		mustEqual(t, "anonymous Authorization on "+hit.Path, hit.Auth, "")
	}
}

func TestListRepositoriesForeignNextIsRefused(t *testing.T) {
	var foreignHits int
	var mu sync.Mutex
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		foreignHits++
		mu.Unlock()
		fmt.Fprint(w, `{"results":[],"next":null}`)
	}))
	t.Cleanup(foreign.Close)

	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/":
			next := `"` + foreign.URL + `/v2/repositories/octocat/?page=2"`
			fmt.Fprint(w, `{"results":[{"name":"one","namespace":"octocat","repository_type":"image"}],"next":`+next+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	repos, err := newTestClient(t, hub.url(), octocatCred()).ListRepositories(context.Background(), "octocat")
	if !errors.Is(err, ErrForeignNext) {
		t.Fatalf("ListRepositories: err = %v, want ErrForeignNext", err)
	}
	if repos != nil {
		t.Errorf("repos = %v, want nil", repos)
	}
	mu.Lock()
	defer mu.Unlock()
	mustEqual(t, "requests to the foreign host", foreignHits, 0)
}

func TestListRepositoriesNextSchemeMismatchIsRefused(t *testing.T) {
	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/repositories/octocat/":
			// Same host, but https while the (test) base origin is http.
			next := `"https://` + r.Host + `/v2/repositories/octocat/?page=2"`
			fmt.Fprint(w, `{"results":[{"name":"one","namespace":"octocat","repository_type":"image"}],"next":`+next+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, err := newTestClient(t, hub.url(), nil).ListRepositories(context.Background(), "octocat")
	if !errors.Is(err, ErrForeignNext) {
		t.Fatalf("ListRepositories: err = %v, want ErrForeignNext", err)
	}
	mustEqual(t, "list requests (no second page fetched)", len(hub.withPath("/v2/repositories/octocat/")), 1)
}

func TestListRepositoriesAnonymousPaginationCapStopsGracefully(t *testing.T) {
	var hub *fakeHub
	hub = newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/repositories/octocat/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"pagination offset too large for anonymous requests; sign in to page further"}`)
			return
		}
		next := `"` + hub.url() + `/v2/repositories/octocat/?page=2&page_size=25"`
		fmt.Fprint(w, `{"results":[{"name":"one","namespace":"octocat","repository_type":"image"}],"next":`+next+`}`)
	})

	repos, err := newTestClient(t, hub.url(), nil).ListRepositories(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("ListRepositories: %v (the anonymous cap must not be an error)", err)
	}
	if len(repos) != 1 || repos[0].Name != "one" {
		t.Fatalf("got %+v, want the first page's single repo", repos)
	}
}

func TestListRepositoriesAuthenticatedPaginationCapIsAnError(t *testing.T) {
	var hub *fakeHub
	hub = newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/":
			if r.URL.Query().Get("page") == "2" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"message":"pagination offset too large for anonymous requests"}`)
				return
			}
			next := `"` + hub.url() + `/v2/repositories/octocat/?page=2"`
			fmt.Fprint(w, `{"results":[],"next":`+next+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// The graceful stop is anonymous-only; an authenticated client seeing
	// the same 403 reports it.
	_, err := newTestClient(t, hub.url(), octocatCred()).ListRepositories(context.Background(), "octocat")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListRepositories: err = %v, want ErrForbidden", err)
	}
}

func TestListRepositoriesStatusErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "401 maps to ErrUnauthorized", status: 401, body: `{"message":"authentication required"}`, wantErr: ErrUnauthorized},
		{name: "403 maps to ErrForbidden", status: 403, body: `{"message":"denied"}`, wantErr: ErrForbidden},
		{name: "404 maps to ErrNotFound", status: 404, body: `{"message":"namespace not found"}`, wantErr: ErrNotFound},
		{name: "500 is a plain status error", status: 500, body: `{"message":"boom"}`, wantErr: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/repositories/octocat/" {
					w.WriteHeader(tt.status)
					fmt.Fprint(w, tt.body)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			})
			_, err := newTestClient(t, hub.url(), nil).ListRepositories(context.Background(), "octocat")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			var se *StatusError
			if !errors.As(err, &se) || se.StatusCode != tt.status {
				t.Fatalf("err = %v, want *StatusError(%d)", err, tt.status)
			}
			if !strings.Contains(err.Error(), "GET") || !strings.Contains(err.Error(), "/v2/repositories/octocat/") {
				t.Errorf("error %q should name the method and path", err)
			}
		})
	}
}

func TestListTags(t *testing.T) {
	var hub *fakeHub
	hub = newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/app/tags/":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"results":[{"name":"1.24","digest":"sha256:def456","last_updated":"2026-09-01T08:00:00Z","full_size":2048}],"next":null}`)
				return
			}
			next := `"` + hub.url() + `/v2/repositories/octocat/app/tags/?page=2"`
			fmt.Fprint(w, `{"results":[`+
				`{"name":"1.25","digest":"sha256:abc123","last_updated":"2026-10-28T12:34:56.789Z","full_size":4096},`+
				`{"name":"latest","digest":"sha256:abc123","last_updated":"2026-10-28T12:34:56.789Z","full_size":4096}`+
				`],"next":`+next+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	tags, err := newTestClient(t, hub.url(), octocatCred()).ListTags(context.Background(), "octocat", "app")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	wantUpdated := time.Date(2026, 10, 28, 12, 34, 56, 789000000, time.UTC)
	want := []Tag{
		{Name: "1.25", Digest: "sha256:abc123", LastUpdated: wantUpdated, FullSize: 4096},
		{Name: "latest", Digest: "sha256:abc123", LastUpdated: wantUpdated, FullSize: 4096},
		{Name: "1.24", Digest: "sha256:def456", LastUpdated: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), FullSize: 2048},
	}
	if len(tags) != len(want) {
		t.Fatalf("got %d tags (%+v), want %d", len(tags), tags, len(want))
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Errorf("tag[%d]: got %+v, want %+v", i, tags[i], want[i])
		}
	}

	hits := hub.withPath("/v2/repositories/octocat/app/tags/")
	if len(hits) != 2 {
		t.Fatalf("got %d tag-list requests, want 2", len(hits))
	}
	mustEqual(t, "first page Authorization", hits[0].Auth, "Bearer jwt-abc-123")
	mustEqual(t, "second page Authorization", hits[1].Auth, "Bearer jwt-abc-123")
}

func TestValidate(t *testing.T) {
	t.Run("credential and namespace", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/users/login":
				fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
			case "/v2/repositories/octocat/":
				fmt.Fprint(w, `{"results":[],"next":null}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		c := newTestClient(t, hub.url(), octocatCred())
		c.namespace = "octocat"
		if err := c.Validate(context.Background()); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		mustEqual(t, "logins", hub.logins(), 1)
	})

	t.Run("namespace defaults to the credential username", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/users/login":
				fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
			case "/v2/repositories/octocat/":
				fmt.Fprint(w, `{"results":[],"next":null}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		if err := newTestClient(t, hub.url(), octocatCred()).Validate(context.Background()); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if len(hub.withPath("/v2/repositories/octocat/")) != 1 {
			t.Error("expected one namespace probe request")
		}
	})

	t.Run("rejected login is ErrUnauthorized", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"incorrect username or password"}`)
		})
		err := newTestClient(t, hub.url(), octocatCred()).Validate(context.Background())
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Validate: err = %v, want ErrUnauthorized", err)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error %q leaks the password", err)
		}
	})

	t.Run("anonymous namespace probe", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/repositories/octocat/" {
				fmt.Fprint(w, `{"results":[],"next":null}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})
		c := newTestClient(t, hub.url(), nil)
		c.namespace = "octocat"
		if err := c.Validate(context.Background()); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("anonymous unknown namespace is ErrNotFound", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"namespace not found"}`)
		})
		c := newTestClient(t, hub.url(), nil)
		c.namespace = "ghost"
		err := c.Validate(context.Background())
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Validate: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("anonymous with no namespace is a misuse error, not a request", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {})
		err := newTestClient(t, hub.url(), nil).Validate(context.Background())
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		mustEqual(t, "requests made", hub.count(), 0)
	})
}

func TestDeleteTag(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/users/login":
				fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
			case "/v2/repositories/octocat/app/tags/v1.0/":
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		if err := newTestClient(t, hub.url(), octocatCred()).DeleteTag(context.Background(), "octocat", "app", "v1.0"); err != nil {
			t.Fatalf("DeleteTag: %v", err)
		}
		hits := hub.withPath("/v2/repositories/octocat/app/tags/v1.0/")
		if len(hits) != 1 || hits[0].Method != "DELETE" {
			t.Fatalf("got hits %+v, want one DELETE", hits)
		}
		mustEqual(t, "Authorization", hits[0].Auth, "Bearer jwt-abc-123")
	})

	t.Run("status mapping", func(t *testing.T) {
		tests := []struct {
			name    string
			status  int
			wantErr error
		}{
			{name: "401", status: 401, wantErr: ErrUnauthorized},
			{name: "403", status: 403, wantErr: ErrForbidden},
			{name: "404", status: 404, wantErr: ErrNotFound},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v2/users/login":
						fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
					case "/v2/repositories/octocat/app/tags/v1.0/":
						w.WriteHeader(tt.status)
						fmt.Fprint(w, `{"message":"nope"}`)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				})
				err := newTestClient(t, hub.url(), octocatCred()).DeleteTag(context.Background(), "octocat", "app", "v1.0")
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DeleteTag: err = %v, want %v", err, tt.wantErr)
				}
			})
		}
	})

	t.Run("anonymous is refused before any request", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {})
		err := newTestClient(t, hub.url(), nil).DeleteTag(context.Background(), "octocat", "app", "v1.0")
		if !errors.Is(err, ErrLoginRequired) {
			t.Fatalf("DeleteTag: err = %v, want ErrLoginRequired", err)
		}
		mustEqual(t, "requests made", hub.count(), 0)
	})

	t.Run("empty arguments are a misuse error", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {})
		c := newTestClient(t, hub.url(), octocatCred())
		if err := c.DeleteTag(context.Background(), "octocat", "app", ""); err == nil {
			t.Error("expected an error for an empty tag")
		}
		if err := c.DeleteTag(context.Background(), "", "app", "v1.0"); err == nil {
			t.Error("expected an error for an empty namespace")
		}
		mustEqual(t, "requests made (no login either)", hub.count(), 0)
	})
}

func TestDeleteTagRetriesOnceAfterExpiredJWT(t *testing.T) {
	var hub *fakeHub
	hub = newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/app/tags/v1.0/":
			if hub.logins() < 2 {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"authentication token expired"}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := newTestClient(t, hub.url(), octocatCred()).DeleteTag(context.Background(), "octocat", "app", "v1.0"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	mustEqual(t, "logins (token expired, then refreshed)", hub.logins(), 2)
	mustEqual(t, "delete attempts", len(hub.withPath("/v2/repositories/octocat/app/tags/v1.0/")), 2)
}

func TestDeleteRepository(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/users/login":
				fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
			case "/v2/repositories/octocat/app/":
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		if err := newTestClient(t, hub.url(), octocatCred()).DeleteRepository(context.Background(), "octocat", "app"); err != nil {
			t.Fatalf("DeleteRepository: %v", err)
		}
		hits := hub.withPath("/v2/repositories/octocat/app/")
		if len(hits) != 1 || hits[0].Method != "DELETE" {
			t.Fatalf("got hits %+v, want one DELETE", hits)
		}
		mustEqual(t, "Authorization", hits[0].Auth, "Bearer jwt-abc-123")
	})

	t.Run("anonymous is refused before any request", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {})
		err := newTestClient(t, hub.url(), nil).DeleteRepository(context.Background(), "octocat", "app")
		if !errors.Is(err, ErrLoginRequired) {
			t.Fatalf("DeleteRepository: err = %v, want ErrLoginRequired", err)
		}
		mustEqual(t, "requests made", hub.count(), 0)
	})
}

func TestRedirectAwayFromHubStripsAuthorization(t *testing.T) {
	var mu sync.Mutex
	var foreignAuth string
	var foreignHits int
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		foreignHits++
		foreignAuth = r.Header.Get("Authorization")
		mu.Unlock()
		fmt.Fprint(w, `{"results":[],"next":null}`)
	}))
	t.Cleanup(foreign.Close)

	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/":
			http.Redirect(w, r, foreign.URL+"/v2/repositories/octocat/", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// A redirect off the Hub origin may be followed for public data, but it
	// must not carry the JWT (guard transport, on top of net/http's own
	// cross-host stripping).
	if _, err := newTestClient(t, hub.url(), octocatCred()).ListRepositories(context.Background(), "octocat"); err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	mustEqual(t, "foreign hits", foreignHits, 1)
	mustEqual(t, "Authorization on the foreign host", foreignAuth, "")
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	const password = "hunter2"
	const token = "jwt-super-secret"

	t.Run("rejected login", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"incorrect username or password"}`)
		})
		_, err := newTestClient(t, hub.url(), &registry.Credential{Username: "octocat", Password: password}).
			ListRepositories(context.Background(), "octocat")
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), password) {
			t.Errorf("error %q contains the password", err)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("error %q contains a token", err)
		}
	})

	t.Run("expired token after successful login", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v2/users/login":
				fmt.Fprint(w, `{"token":"`+token+`"}`)
			case "/v2/repositories/octocat/":
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"authentication token expired"}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		_, err := newTestClient(t, hub.url(), &registry.Credential{Username: "octocat", Password: password}).
			ListRepositories(context.Background(), "octocat")
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrUnauthorized", err)
		}
		if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), token) {
			t.Errorf("error %q leaks credential material", err)
		}
	})

	t.Run("credential without a secret is refused without a request", func(t *testing.T) {
		hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {})
		_, err := newTestClient(t, hub.url(), &registry.Credential{Username: "octocat"}).
			ListRepositories(context.Background(), "octocat")
		if err == nil {
			t.Fatal("expected an error")
		}
		mustEqual(t, "requests made", hub.count(), 0)
	})
}

func TestPageSizeFollowsAuthMode(t *testing.T) {
	tests := []struct {
		name       string
		cred       *registry.Credential
		wantSize   string
		wantLogins int
	}{
		{name: "authenticated asks for 100 per page", cred: octocatCred(), wantSize: "100", wantLogins: 1},
		{name: "anonymous asks for 25 per page", cred: nil, wantSize: "25", wantLogins: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotQuery string
			hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/users/login":
					fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
				case "/v2/repositories/octocat/":
					gotQuery = r.URL.RawQuery
					fmt.Fprint(w, `{"results":[],"next":null}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			if _, err := newTestClient(t, hub.url(), tt.cred).ListRepositories(context.Background(), "octocat"); err != nil {
				t.Fatalf("ListRepositories: %v", err)
			}
			mustEqual(t, "first-page query", gotQuery, "page_size="+tt.wantSize)
			mustEqual(t, "logins", hub.logins(), tt.wantLogins)
		})
	}
}

func TestListRepositoriesSchemeRelativeForeignNextIsRefused(t *testing.T) {
	var foreignHits int
	var mu sync.Mutex
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		foreignHits++
		mu.Unlock()
		fmt.Fprint(w, `{"results":[],"next":null}`)
	}))
	t.Cleanup(foreign.Close)

	foreignHost := strings.TrimPrefix(foreign.URL, "http://")
	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
		case "/v2/repositories/octocat/":
			// A scheme-relative URL ("//host/path") has u.IsAbs() == false in
			// net/url; followURL must still reject the foreign host.
			next := `"//` + foreignHost + `/v2/repositories/octocat/?page=2"`
			fmt.Fprint(w, `{"results":[{"name":"one","namespace":"octocat","repository_type":"image"}],"next":`+next+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	_, err := newTestClient(t, hub.url(), octocatCred()).ListRepositories(context.Background(), "octocat")
	if !errors.Is(err, ErrForeignNext) {
		t.Fatalf("ListRepositories: err = %v, want ErrForeignNext", err)
	}
	mu.Lock()
	defer mu.Unlock()
	mustEqual(t, "requests to the foreign host", foreignHits, 0)
}

func TestLoginRedirectToForeignHostRefusedWithoutSendingPassword(t *testing.T) {
	var mu sync.Mutex
	var foreignHits int
	var foreignBody string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		foreignHits++
		foreignBody = string(raw)
		mu.Unlock()
		fmt.Fprint(w, `{"token":"evil-jwt"}`)
	}))
	t.Cleanup(foreign.Close)

	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/users/login" {
			// 307 instructs standard HTTP clients to replay the POST body.
			http.Redirect(w, r, foreign.URL+"/v2/users/login", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := newTestClient(t, hub.url(), octocatCred()).ListRepositories(context.Background(), "octocat")
	if !errors.Is(err, ErrForeignNext) {
		t.Fatalf("ListRepositories: err = %v, want ErrForeignNext", err)
	}
	mu.Lock()
	defer mu.Unlock()
	mustEqual(t, "foreign hits (login body must never be replayed off-origin)", foreignHits, 0)
	mustEqual(t, "foreign body", foreignBody, "")
}

func TestNewRefusesPlainHTTPWithCredentialForNonLoopback(t *testing.T) {
	_, err := New(Options{
		BaseURL:    "http://hub.docker.com",
		Credential: octocatCred(),
	})
	if !errors.Is(err, ErrInsecureHTTP) {
		t.Fatalf("New: err = %v, want ErrInsecureHTTP", err)
	}
}

func TestDeleteRepositoryStatusErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr error
	}{
		{name: "401", status: 401, wantErr: ErrUnauthorized},
		{name: "403", status: 403, wantErr: ErrForbidden},
		{name: "404", status: 404, wantErr: ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/users/login":
					fmt.Fprint(w, `{"token":"jwt-abc-123"}`)
				case "/v2/repositories/octocat/app/":
					w.WriteHeader(tt.status)
					fmt.Fprint(w, `{"message":"nope"}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			err := newTestClient(t, hub.url(), octocatCred()).DeleteRepository(context.Background(), "octocat", "app")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DeleteRepository: err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestClientAndOptionsRedaction(t *testing.T) {
	const password = "dckr_pat_super_secret"
	const jwt = "jwt-top-secret-token"

	hub := newFakeHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/users/login":
			fmt.Fprint(w, `{"token":"`+jwt+`"}`)
		case "/v2/repositories/octocat/":
			fmt.Fprint(w, `{"results":[],"next":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	opts := Options{
		BaseURL:    hub.url(),
		Namespace:  "octocat",
		Credential: &registry.Credential{Username: "octocat", IdentityToken: password},
	}
	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	optsJSON, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("json.Marshal(opts): %v", err)
	}
	clientJSON, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal(client): %v", err)
	}

	dumps := []string{
		fmt.Sprintf("%v", opts),
		fmt.Sprintf("%#v", opts),
		string(optsJSON),
		fmt.Sprintf("%v", c),
		fmt.Sprintf("%#v", c),
		string(clientJSON),
	}
	for _, d := range dumps {
		if strings.Contains(d, password) {
			t.Errorf("dump %q leaks password/PAT", d)
		}
		if strings.Contains(d, jwt) {
			t.Errorf("dump %q leaks cached JWT", d)
		}
	}
}
