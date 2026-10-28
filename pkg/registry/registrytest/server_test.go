package registrytest

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
)

func TestServerAnonymous(t *testing.T) {
	s := New(Config{})
	defer s.Close()

	if !strings.HasPrefix(s.URL(), "http://") {
		t.Errorf("URL = %q, want plain http", s.URL())
	}

	resp, err := http.Get(s.URL() + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
		t.Errorf("api version header = %q, want registry/2.0", got)
	}
}

func TestServerBasicAuth(t *testing.T) {
	s := New(Config{Auth: AuthBasic, Username: "alice", Password: "wonderland"})
	defer s.Close()

	t.Run("challenge without credentials", func(t *testing.T) {
		resp, err := http.Get(s.URL() + "/v2/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if !strings.HasPrefix(resp.Header.Get("Www-Authenticate"), "Basic ") {
			t.Errorf("challenge = %q, want Basic", resp.Header.Get("Www-Authenticate"))
		}
	})

	t.Run("accepted with credentials", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/", nil)
		req.SetBasicAuth("alice", "wonderland")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})
}

func TestServerBearerAuth(t *testing.T) {
	s := New(Config{Auth: AuthBearer, Username: "alice", Password: "wonderland"})
	defer s.Close()

	t.Run("challenge names realm and service", func(t *testing.T) {
		resp, err := http.Get(s.URL() + "/v2/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		challenge := resp.Header.Get("Www-Authenticate")
		if !strings.HasPrefix(challenge, "Bearer ") {
			t.Fatalf("challenge = %q, want Bearer", challenge)
		}
		for _, want := range []string{`realm="` + s.RealmURL() + `"`, `service="` + s.Host() + `"`} {
			if !strings.Contains(challenge, want) {
				t.Errorf("challenge %q missing %s", challenge, want)
			}
		}
	})

	t.Run("anonymous token flow", func(t *testing.T) {
		resp, err := http.Get(s.RealmURL() + "?service=" + s.Host())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("token status = %d, want 200", resp.StatusCode)
		}
		var body struct {
			Token     string `json:"token"`
			ExpiresIn int    `json:"expires_in"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Token == "" || body.ExpiresIn != 300 {
			t.Errorf("token response = %+v, want a token with expires_in 300", body)
		}

		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/", nil)
		req.Header.Set("Authorization", "Bearer "+body.Token)
		authed, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer authed.Body.Close()
		if authed.StatusCode != http.StatusOK {
			t.Errorf("status with issued token = %d, want 200", authed.StatusCode)
		}
	})

	t.Run("invalid basic auth at the realm is rejected", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, s.RealmURL(), nil)
		req.SetBasicAuth("alice", "nope")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("unknown token is rejected at /v2/", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/", nil)
		req.Header.Set("Authorization", "Bearer made-up-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})
}

func TestServerUnknownEndpointOCIError(t *testing.T) {
	s := New(Config{})
	defer s.Close()

	// /v2/_catalog is a real endpoint now (an empty catalog answers 200);
	// a genuinely unknown /v2/ path 404s the OCI way.
	resp, err := http.Get(s.URL() + "/v2/_bogus")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Errors) != 1 || body.Errors[0].Code != "NAME_UNKNOWN" {
		t.Errorf("oci error body = %+v, want one NAME_UNKNOWN", body)
	}
}

func TestServerRecording(t *testing.T) {
	s := New(Config{Auth: AuthBasic, Username: "alice", Password: "wonderland"})
	defer s.Close()

	req, _ := http.NewRequest(http.MethodGet, s.URL()+"/v2/?x=1", nil)
	req.SetBasicAuth("alice", "wonderland")
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}

	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodGet || r.Path != "/v2/" || r.RawQuery != "x=1" {
		t.Errorf("recorded = %+v", r)
	}
	if r.Authorization != "basic" || r.BasicUsername != "alice" {
		t.Errorf("recorded auth = %q/%q, want basic/alice", r.Authorization, r.BasicUsername)
	}

	// The recording must never contain the password.
	if strings.Contains(fmt.Sprint(reqs), "wonderland") {
		t.Error("recording leaked the password")
	}
}

func TestServerTokenCounters(t *testing.T) {
	s := New(Config{Auth: AuthBearer, TokenTTL: 60 * time.Second})
	defer s.Close()

	if got := s.TokensIssued(); got != 0 {
		t.Fatalf("tokens issued = %d, want 0", got)
	}
	for i := 0; i < 2; i++ {
		if _, err := http.Get(s.RealmURL()); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.TokensIssued(); got != 2 {
		t.Errorf("tokens issued = %d, want 2", got)
	}
	if got := len(s.TokenRequests()); got != 2 {
		t.Errorf("token requests = %d, want 2", got)
	}
}

func TestServerRealmOverride(t *testing.T) {
	s := New(Config{Auth: AuthBearer})
	defer s.Close()

	s.SetRealmBaseURL("https://auth.example.com")
	if got := s.RealmURL(); got != "https://auth.example.com/token" {
		t.Errorf("realm = %q, want https://auth.example.com/token", got)
	}

	s.SetRealmBaseURL("https://auth.example.com/")
	if got := s.RealmURL(); got != "https://auth.example.com/token" {
		t.Errorf("realm = %q (trailing slash must be trimmed), want https://auth.example.com/token", got)
	}
}

func TestServerTLS(t *testing.T) {
	// A config with no certificate: httptest appends its own, so the server
	// still comes up on https and rejects plain http.
	s := New(Config{TLS: &tls.Config{}})
	defer s.Close()

	if !strings.HasPrefix(s.URL(), "https://") {
		t.Errorf("URL = %q, want https", s.URL())
	}

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Get(s.URL() + "/v2/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServerConcurrentUse(t *testing.T) {
	s := New(Config{Auth: AuthBearer, Username: "alice", Password: "wonderland"})
	defer s.Close()

	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			resp, err := http.Get(s.RealmURL())
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d", resp.StatusCode)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if got := s.TokensIssued(); got != 4 {
		t.Errorf("tokens issued = %d, want 4", got)
	}
}

// --- catalog, tags and manifests (L1.2) ---------------------------------

// fixtureManifests builds one single manifest and one index over two
// children, as raw fixture data.
func fixtureManifests(t *testing.T) (single Manifest, index Manifest, singleDigest string, indexDigest string) {
	t.Helper()
	mustMarshal := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	childBody := mustMarshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json"})
	child := Manifest{
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Body:      childBody,
	}
	indexBody := mustMarshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size":      len(child.Body),
			"digest":    digest.FromBytes(childBody).String(),
			"platform":  map[string]any{"os": "linux", "architecture": "amd64"},
		}},
	})
	index = Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Body:      indexBody,
	}
	single = child
	return single, index, digest.FromBytes(childBody).String(), digest.FromBytes(indexBody).String()
}

func fixtureRepos(t *testing.T) []Repository {
	t.Helper()
	single, index, _, _ := fixtureManifests(t)
	return []Repository{
		{Name: "alpha", Tags: map[string]Manifest{
			"1.0":   single,
			"1.1":   single,
			"multi": index,
		}},
		{Name: "beta", Tags: map[string]Manifest{"2.0": single}},
		{Name: "gamma", Tags: map[string]Manifest{"3.0": single}},
	}
}

func getBody(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

func TestServerCatalogSinglePage(t *testing.T) {
	s := New(Config{Repositories: fixtureRepos(t)})
	defer s.Close()

	status, header, body := getBody(t, s.URL()+"/v2/_catalog")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var page struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(page.Repositories) != 3 || page.Repositories[0] != "alpha" || page.Repositories[2] != "gamma" {
		t.Errorf("repositories = %v, want %v sorted", page.Repositories, want)
	}
	if link := header.Get("Link"); link != "" {
		t.Errorf("Link = %q, want none on the only page", link)
	}
}

func TestServerCatalogPagination(t *testing.T) {
	t.Run("client n drives the page", func(t *testing.T) {
		s := New(Config{Repositories: fixtureRepos(t)})
		defer s.Close()

		_, header, body := getBody(t, s.URL()+"/v2/_catalog?n=2")
		var page struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Repositories) != 2 || page.Repositories[0] != "alpha" {
			t.Fatalf("first page = %v, want [alpha beta]", page.Repositories)
		}
		if link := header.Get("Link"); link != `</v2/_catalog?last=beta&n=2>; rel="next"` {
			t.Errorf("Link = %q, want the beta cursor with n echoed", link)
		}

		// Following the link yields the tail, with no further Link.
		_, header, body = getBody(t, s.URL()+"/v2/_catalog?last=beta&n=2")
		page.Repositories = nil
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Repositories) != 1 || page.Repositories[0] != "gamma" {
			t.Errorf("second page = %v, want [gamma]", page.Repositories)
		}
		if link := header.Get("Link"); link != "" {
			t.Errorf("Link on last page = %q, want none", link)
		}
	})

	t.Run("forced page size caps whatever n says", func(t *testing.T) {
		s := New(Config{Repositories: fixtureRepos(t), CatalogPageSize: 2})
		defer s.Close()

		_, header, body := getBody(t, s.URL()+"/v2/_catalog?n=50")
		var page struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Repositories) != 2 {
			t.Errorf("page = %v, want 2 entries despite n=50", page.Repositories)
		}
		if link := header.Get("Link"); link != `</v2/_catalog?last=beta&n=50>; rel="next"` {
			t.Errorf("Link = %q, want the forced page cursor", link)
		}
	})

	t.Run("forced page size with no client n", func(t *testing.T) {
		s := New(Config{Repositories: fixtureRepos(t), CatalogPageSize: 2})
		defer s.Close()

		_, header, body := getBody(t, s.URL()+"/v2/_catalog")
		var page struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Repositories) != 2 || page.Repositories[1] != "beta" {
			t.Fatalf("page = %v, want [alpha beta]", page.Repositories)
		}
		if link := header.Get("Link"); link != `</v2/_catalog?last=beta>; rel="next"` {
			t.Errorf("Link = %q, want last-only cursor (no n to echo)", link)
		}
	})
}

func TestServerTagsList(t *testing.T) {
	s := New(Config{Repositories: fixtureRepos(t)})
	defer s.Close()

	status, _, body := getBody(t, s.URL()+"/v2/alpha/tags/list")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var page struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Name != "alpha" {
		t.Errorf("name = %q, want alpha", page.Name)
	}
	want := []string{"1.0", "1.1", "multi"}
	if len(page.Tags) != 3 || page.Tags[0] != "1.0" || page.Tags[2] != "multi" {
		t.Errorf("tags = %v, want %v sorted", page.Tags, want)
	}

	t.Run("paginated", func(t *testing.T) {
		srv := New(Config{Repositories: fixtureRepos(t), TagsPageSize: 2})
		defer srv.Close()
		_, header, body := getBody(t, srv.URL()+"/v2/alpha/tags/list")
		page.Tags = nil
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Tags) != 2 {
			t.Fatalf("page = %v, want 2 tags", page.Tags)
		}
		if link := header.Get("Link"); link != `</v2/alpha/tags/list?last=1.1>; rel="next"` {
			t.Errorf("Link = %q, want the 1.1 cursor", link)
		}
	})

	t.Run("unknown repository", func(t *testing.T) {
		srv := New(Config{Repositories: fixtureRepos(t)})
		defer srv.Close()
		status, _, body := getBody(t, srv.URL()+"/v2/nope/tags/list")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
		var apiErr struct {
			Errors []struct {
				Code string `json:"code"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(body, &apiErr); err != nil {
			t.Fatal(err)
		}
		if len(apiErr.Errors) != 1 || apiErr.Errors[0].Code != "NAME_UNKNOWN" {
			t.Errorf("error body = %s, want NAME_UNKNOWN", body)
		}
	})
}

func TestServerManifest(t *testing.T) {
	repos := fixtureRepos(t)
	s := New(Config{Repositories: repos})
	defer s.Close()

	_, _, singleDgst, indexDgst := fixtureManifests(t)
	single := fixtureRepos(t)[0].Tags["1.0"]

	t.Run("by tag", func(t *testing.T) {
		status, header, body := getBody(t, s.URL()+"/v2/alpha/manifests/1.0")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if ct := header.Get("Content-Type"); ct != single.MediaType {
			t.Errorf("Content-Type = %q, want %q", ct, single.MediaType)
		}
		if dcd := header.Get("Docker-Content-Digest"); dcd != singleDgst {
			t.Errorf("Docker-Content-Digest = %q, want %q (digest of the body)", dcd, singleDgst)
		}
		if string(body) != string(single.Body) {
			t.Error("manifest body not served verbatim")
		}
	})

	t.Run("by digest", func(t *testing.T) {
		status, _, body := getBody(t, s.URL()+"/v2/alpha/manifests/"+indexDgst)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		var probe struct {
			MediaType string `json:"mediaType"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			t.Fatal(err)
		}
		if probe.MediaType != "application/vnd.oci.image.index.v1+json" {
			t.Errorf("served the wrong manifest for digest: mediaType %q", probe.MediaType)
		}
	})

	t.Run("digest override is served as-is", func(t *testing.T) {
		overridden := Manifest{
			MediaType:     single.MediaType,
			Body:          single.Body,
			ContentDigest: "sha256:" + strings.Repeat("ab", 32),
		}
		srv := New(Config{Repositories: []Repository{
			{Name: "odd", Tags: map[string]Manifest{"v1": overridden}},
		}})
		defer srv.Close()
		_, header, _ := getBody(t, srv.URL()+"/v2/odd/manifests/v1")
		if dcd := header.Get("Docker-Content-Digest"); dcd != overridden.ContentDigest {
			t.Errorf("Docker-Content-Digest = %q, want the override %q", dcd, overridden.ContentDigest)
		}
	})

	t.Run("unknown tag", func(t *testing.T) {
		status, _, body := getBody(t, s.URL()+"/v2/alpha/manifests/9.9")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
		if !strings.Contains(string(body), "MANIFEST_UNKNOWN") {
			t.Errorf("error body = %s, want MANIFEST_UNKNOWN", body)
		}
	})

	t.Run("unknown repository", func(t *testing.T) {
		status, _, body := getBody(t, s.URL()+"/v2/nope/manifests/v1")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
		if !strings.Contains(string(body), "NAME_UNKNOWN") {
			t.Errorf("error body = %s, want NAME_UNKNOWN", body)
		}
	})
}

// TestServerBearerScopeEnforcement pins the fixture's scope model: an
// unscoped token satisfies the ping but not repository endpoints, whose
// challenges name the scope the token must carry.
func TestServerBearerScopeEnforcement(t *testing.T) {
	single, index, _, _ := fixtureManifests(t)
	s := New(Config{
		Auth:     AuthBearer,
		Username: "alice",
		Password: "wonderland",
		Repositories: []Repository{
			{Name: "alpha", Tags: map[string]Manifest{"v1": single, "multi": index}},
		},
	})
	defer s.Close()

	tagsURL := s.URL() + "/v2/alpha/tags/list"

	// No token: the challenge names the repository pull scope.
	status, header, _ := getBody(t, tagsURL)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	challenge := header.Get("Www-Authenticate")
	if !strings.Contains(challenge, `scope="repository:alpha:pull"`) {
		t.Errorf("challenge %q must name scope repository:alpha:pull", challenge)
	}

	// A scope-less token (the ping shape) is NOT enough.
	getToken := func(query string) string {
		_, _, body := getBody(t, s.RealmURL()+query)
		var token struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &token); err != nil {
			t.Fatal(err)
		}
		return token.Token
	}
	fetch := func(url, auth string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	unscoped := getToken("?service=" + s.Host())
	if code := fetch(tagsURL, "Bearer "+unscoped); code != http.StatusUnauthorized {
		t.Errorf("unscoped token at tags/list: status %d, want 401", code)
	}

	// A token granted for the scope is.
	scoped := getToken("?service=" + s.Host() + "&scope=repository:alpha:pull")
	if code := fetch(tagsURL, "Bearer "+scoped); code != http.StatusOK {
		t.Errorf("scoped token at tags/list: status %d, want 200", code)
	}

	// The catalog demands its own scope.
	scopedCatalog := getToken("?service=" + s.Host() + "&scope=registry:catalog:*")
	if code := fetch(s.URL()+"/v2/_catalog", "Bearer "+scopedCatalog); code != http.StatusOK {
		t.Errorf("catalog-scoped token at _catalog: status %d, want 200", code)
	}
	if code := fetch(s.URL()+"/v2/_catalog", "Bearer "+scoped); code != http.StatusUnauthorized {
		t.Errorf("repo-scoped token at _catalog: status %d, want 401 (wrong scope)", code)
	}
}
