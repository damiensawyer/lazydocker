package ghcr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/jesseduffield/lazydocker/pkg/registry/registrytest"
	"github.com/stretchr/testify/assert"
)

func TestListPackagesUserAndPagination(t *testing.T) {
	const testToken = "ghp_super_secret_token_value_12345"

	var userCalls int32
	var seenAuthHeaders []string

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuthHeaders = append(seenAuthHeaders, r.Header.Get("Authorization"))
		assert.Equal(t, acceptHeader, r.Header.Get("Accept"))
		assert.Equal(t, apiVersionHeader, r.Header.Get("X-GitHub-Api-Version"))

		switch r.URL.Path {
		case "/users/octocat":
			atomic.AddInt32(&userCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"login":"octocat","type":"User"}`)
		case "/users/octocat/packages":
			assert.Equal(t, "container", r.URL.Query().Get("package_type"))
			w.Header().Set("Content-Type", "application/json")

			switch r.URL.Query().Get("page") {
			case "", "1":
				w.Header().Set("Link", fmt.Sprintf(
					`<%s/users/octocat/packages?package_type=container&per_page=100&page=2>; rel="next", <%s/users/octocat/packages?package_type=container&per_page=100&page=2>; rel="last"`,
					srv.URL, srv.URL,
				))
				_, _ = fmt.Fprint(w, `[
					{"id":101,"name":"alpha","package_type":"container","visibility":"public"},
					{"id":102,"name":"beta","package_type":"container","visibility":"private"},
					{"id":103,"name":"ignored-npm","package_type":"npm","visibility":"public"}
				]`)
			case "2":
				_, _ = fmt.Fprint(w, `[
					{"id":104,"name":"octocat/gamma","package_type":"container","visibility":"internal"}
				]`)
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(Options{
		BaseURL: srv.URL,
		Token:   testToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	pkgs, err := client.ListPackages(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("ListPackages: %v", err)
	}
	assert.Equal(t, []Package{
		{ID: 101, Name: "octocat/alpha", PackageName: "alpha", Owner: "octocat", Visibility: VisibilityPublic},
		{ID: 102, Name: "octocat/beta", PackageName: "beta", Owner: "octocat", Visibility: VisibilityPrivate},
		{ID: 104, Name: "octocat/gamma", PackageName: "gamma", Owner: "octocat", Visibility: VisibilityPrivate},
	}, pkgs)

	if len(seenAuthHeaders) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(seenAuthHeaders))
	}
	for _, h := range seenAuthHeaders {
		assert.Equal(t, "Bearer "+testToken, h)
	}

	// Second call for the same owner reuses the cached OwnerType without
	// hitting GET /users/octocat again.
	if err := client.Validate(context.Background(), "octocat"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&userCalls))
}

func TestListPackagesOrg(t *testing.T) {
	const testToken = "ghp_org_token_999"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))

		switch r.URL.Path {
		case "/users/kubernetes":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"login":"kubernetes","type":"Organization"}`)
		case "/orgs/kubernetes/packages":
			assert.Equal(t, "container", r.URL.Query().Get("package_type"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[
				{"id":201,"name":"apiserver","package_type":"container","visibility":"public"},
				{"id":202,"name":"internal-tool","package_type":"container","visibility":"private"}
			]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(Options{
		BaseURL: srv.URL,
		Credential: &registry.Credential{
			Username: "kubernetes",
			Password: testToken,
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	pkgs, err := client.ListContainerPackages(context.Background(), "")
	if err != nil {
		t.Fatalf("ListContainerPackages: %v", err)
	}
	assert.Equal(t, []Package{
		{ID: 201, Name: "kubernetes/apiserver", PackageName: "apiserver", Owner: "kubernetes", Visibility: VisibilityPublic},
		{ID: 202, Name: "kubernetes/internal-tool", PackageName: "internal-tool", Owner: "kubernetes", Visibility: VisibilityPrivate},
	}, pkgs)
}

func TestForeignNextLinkRefusedWithoutLeakingToken(t *testing.T) {
	const secretToken = "ghp_never_leak_this_token_xyz"

	var foreignHits int32
	var foreignAuthHeader atomic.Value

	foreignSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		foreignAuthHeader.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"id":999,"name":"hijacked","package_type":"container","visibility":"public"}]`)
	}))
	t.Cleanup(foreignSrv.Close)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/octocat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"login":"octocat","type":"User"}`)
		case "/users/octocat/packages":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Link", fmt.Sprintf(`<%s/users/octocat/packages?page=2>; rel="next"`, foreignSrv.URL))
			_, _ = fmt.Fprint(w, `[{"id":1,"name":"safe","package_type":"container","visibility":"public"}]`)
		case "/users/octocat/packages/container/safe/versions":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Link", fmt.Sprintf(`<%s/users/octocat/packages/container/safe/versions?page=2>; rel="next"`, foreignSrv.URL))
			_, _ = fmt.Fprint(w, `[{"id":10,"name":"sha256:aaa","metadata":{"package_type":"container","container":{"tags":["v1"]}}}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(apiSrv.Close)

	client, err := NewClient(Options{
		BaseURL: apiSrv.URL,
		Token:   secretToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ListPackages(context.Background(), "octocat")
	if err == nil {
		t.Fatal("expected error for foreign next link in ListPackages")
	}
	assert.True(t, errors.Is(err, ErrForeignHost), "expected ErrForeignHost, got %v", err)
	assert.False(t, strings.Contains(err.Error(), secretToken))
	assert.Equal(t, int32(0), atomic.LoadInt32(&foreignHits), "foreign server must never be contacted")
	assert.Nil(t, foreignAuthHeader.Load())

	_, err = client.ListVersions(context.Background(), "octocat", "safe")
	if err == nil {
		t.Fatal("expected error for foreign next link in ListVersions")
	}
	assert.True(t, errors.Is(err, ErrForeignHost), "expected ErrForeignHost, got %v", err)
	assert.False(t, strings.Contains(err.Error(), secretToken))
	assert.Equal(t, int32(0), atomic.LoadInt32(&foreignHits), "foreign server must never be contacted on version pagination")
}

func TestForeignRedirectRefusedWithoutLeakingToken(t *testing.T) {
	const secretToken = "ghp_redirect_secret_token"

	var foreignHits int32
	foreignSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(foreignSrv.Close)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreignSrv.URL+"/users/octocat", http.StatusFound)
	}))
	t.Cleanup(apiSrv.Close)

	client, err := NewClient(Options{
		BaseURL: apiSrv.URL,
		Token:   secretToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ListPackages(context.Background(), "octocat")
	if err == nil {
		t.Fatal("expected redirect to foreign host to fail")
	}
	assert.True(t, errors.Is(err, ErrForeignHost), "expected ErrForeignHost, got %v", err)
	assert.False(t, strings.Contains(err.Error(), secretToken))
	assert.Equal(t, int32(0), atomic.LoadInt32(&foreignHits), "redirect target on foreign host must not be contacted")
}

func TestPlainHTTPNonLoopbackRefusedWithToken(t *testing.T) {
	_, err := NewClient(Options{
		BaseURL: "http://api.github.com",
		Token:   "ghp_secret",
	})
	if !errors.Is(err, ErrInsecureTokenRefused) {
		t.Fatalf("expected ErrInsecureTokenRefused, got %v", err)
	}
}

func TestListVersionsAndResolveThenDelete(t *testing.T) {
	const testToken = "ghp_delete_token_789"

	var deletedVersionPath string
	var deletedPackagePath string

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/users/octocat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"login":"octocat","type":"User"}`)

		case r.Method == http.MethodGet && r.URL.Path == "/users/octocat/packages/container/webapp/versions":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page") == "2" {
				_, _ = fmt.Fprint(w, `[
					{
						"id": 9002,
						"name": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
						"metadata": {
							"package_type": "container",
							"container": {
								"tags": ["v1.0.0", "v1.0", "latest"]
							}
						}
					}
				]`)
				return
			}
			w.Header().Set("Link", fmt.Sprintf(
				`<%s/users/octocat/packages/container/webapp/versions?per_page=100&page=2>; rel="next"`,
				srv.URL,
			))
			_, _ = fmt.Fprint(w, `[
				{
					"id": 9001,
					"name": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"metadata": {
						"package_type": "container",
						"container": {
							"tags": ["v0.9.0"]
						}
					}
				}
			]`)

		case r.Method == http.MethodDelete && r.URL.Path == "/users/octocat/packages/container/webapp/versions/9002":
			deletedVersionPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && r.URL.Path == "/users/octocat/packages/container/webapp":
			deletedPackagePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(Options{
		BaseURL: srv.URL,
		Token:   testToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// 1. ListVersions returns all paginated versions with Digest and Tags.
	versions, err := client.ListVersions(context.Background(), "octocat", "octocat/webapp")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	assert.Equal(t, []Version{
		{
			ID:     9001,
			Digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Tags:   []string{"v0.9.0"},
		},
		{
			ID:     9002,
			Digest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
			Tags:   []string{"v1.0.0", "v1.0", "latest"},
		},
	}, versions)

	// 2. Two-step ResolveTagDeletion + DeleteVersion:
	versionID, blastRadius, err := client.ResolveTagDeletion(context.Background(), "octocat", "webapp", "v1.0")
	if err != nil {
		t.Fatalf("ResolveTagDeletion: %v", err)
	}
	assert.Equal(t, int64(9002), versionID)
	assert.Equal(t, []string{"v1.0.0", "v1.0", "latest"}, blastRadius)
	assert.Empty(t, deletedVersionPath, "ResolveTagDeletion must not delete anything yet")

	if err := client.DeleteVersion(context.Background(), "octocat", "webapp", versionID); err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}
	assert.Equal(t, "/users/octocat/packages/container/webapp/versions/9002", deletedVersionPath)

	// 3. Combined DeleteTag resolves the version, deletes it, and returns the blast radius:
	deletedVersionPath = ""
	coTags, err := client.DeleteTag(context.Background(), "octocat", "octocat/webapp", "latest")
	if err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	assert.Equal(t, []string{"v1.0.0", "v1.0", "latest"}, coTags)
	assert.Equal(t, "/users/octocat/packages/container/webapp/versions/9002", deletedVersionPath)

	// 4. Missing tag returns ErrNotFound without sending DELETE:
	deletedVersionPath = ""
	_, err = client.DeleteTag(context.Background(), "octocat", "webapp", "nonexistent-tag")
	if err == nil {
		t.Fatal("expected error for nonexistent tag")
	}
	assert.True(t, errors.Is(err, ErrNotFound), "expected ErrNotFound, got %v", err)
	assert.Empty(t, deletedVersionPath)

	// 5. DeletePackage deletes the whole container package:
	if err := client.DeletePackage(context.Background(), "octocat", "octocat/webapp"); err != nil {
		t.Fatalf("DeletePackage: %v", err)
	}
	assert.Equal(t, "/users/octocat/packages/container/webapp", deletedPackagePath)
}

func TestOrgDeleteVersionAndPackage(t *testing.T) {
	var deletedPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/users/my-org":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"login":"my-org","type":"Organization"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/orgs/my-org/packages/container/service/versions/42":
			deletedPaths = append(deletedPaths, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/orgs/my-org/packages/container/service":
			deletedPaths = append(deletedPaths, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(Options{
		BaseURL: srv.URL,
		Token:   "ghp_org_del",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := client.DeleteVersion(context.Background(), "my-org", "service", 42); err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}
	if err := client.DeletePackage(context.Background(), "my-org", "service"); err != nil {
		t.Fatalf("DeletePackage: %v", err)
	}
	assert.Equal(t, []string{
		"/orgs/my-org/packages/container/service/versions/42",
		"/orgs/my-org/packages/container/service",
	}, deletedPaths)
}

func TestStatusMapping401And403And404(t *testing.T) {
	const secretToken = "ghp_secret_for_error_mapping"

	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{
			name:       "401 Unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       `{"message":"Bad credentials"}`,
			wantErr:    ErrUnauthorized,
		},
		{
			name:       "403 Forbidden",
			statusCode: http.StatusForbidden,
			body:       `{"message":"Resource not accessible by personal access token"}`,
			wantErr:    ErrForbidden,
		},
		{
			name:       "404 Not Found",
			statusCode: http.StatusNotFound,
			body:       `{"message":"Not Found"}`,
			wantErr:    ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			client, err := NewClient(Options{
				BaseURL: srv.URL,
				Token:   secretToken,
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			_, err = client.ListPackages(context.Background(), "octocat")
			if err == nil {
				t.Fatalf("expected error for status %d", tc.statusCode)
			}
			assert.True(t, errors.Is(err, tc.wantErr), "expected %v, got %v", tc.wantErr, err)
			assert.False(t, strings.Contains(err.Error(), secretToken))

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *APIError, got %T", err)
			}
			assert.Equal(t, tc.statusCode, apiErr.StatusCode)
		})
	}
}

func TestTokenNeverLoggedOrEmbedded(t *testing.T) {
	const secretToken = "ghp_ultra_sensitive_pat_987654321"

	// Server echoes the token in the error message to verify error redaction.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"message":"rejected token %s"}`, secretToken)
	}))
	t.Cleanup(srv.Close)

	opts := Options{
		BaseURL: srv.URL,
		Owner:   "octocat",
		Token:   secretToken,
	}
	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	for _, s := range []string{
		fmt.Sprintf("%v", opts),
		fmt.Sprintf("%#v", opts),
		fmt.Sprintf("%v", client),
		fmt.Sprintf("%#v", client),
	} {
		assert.False(t, strings.Contains(s, secretToken), "leaked token in formatted string: %s", s)
	}

	optsJSON, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("json.Marshal(opts): %v", err)
	}
	assert.False(t, strings.Contains(string(optsJSON), secretToken))

	clientJSON, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("json.Marshal(client): %v", err)
	}
	assert.False(t, strings.Contains(string(clientJSON), secretToken))

	_, err = client.ListPackages(context.Background(), "octocat")
	if err == nil {
		t.Fatal("expected error from 401 response")
	}
	assert.True(t, errors.Is(err, ErrUnauthorized))
	assert.False(t, strings.Contains(err.Error(), secretToken))
	assert.True(t, strings.Contains(err.Error(), "[REDACTED]"))
}

func TestNewRegistryClient(t *testing.T) {
	const ghToken = "ghp_registry_token_abc"

	regClient, err := NewRegistryClient(&registry.Credential{
		Password: ghToken,
	})
	if err != nil {
		t.Fatalf("NewRegistryClient: %v", err)
	}
	assert.Equal(t, RegistryHost, regClient.Host())

	// Verify the OCI bearer handshake against an in-process registrytest server
	// using the GitHub token as the basic-auth password at the token realm.
	fakeReg := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBearer,
		Username: defaultTokenUsername,
		Password: ghToken,
	})
	t.Cleanup(fakeReg.Close)

	authedClient, err := NewRegistryClientWithOptions(
		fakeReg.Host(),
		&registry.Credential{IdentityToken: ghToken},
		registry.Options{},
	)
	if err != nil {
		t.Fatalf("NewRegistryClientWithOptions: %v", err)
	}

	pingRes, err := authedClient.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	assert.Equal(t, registry.StatusOnline, pingRes.Status)
	assert.Equal(t, 1, fakeReg.TokensIssued())
}
