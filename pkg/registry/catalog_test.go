package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/jesseduffield/lazydocker/pkg/registry/registrytest"
)

// manifestFixture is a manifest the tests build, serve and verify against.
type manifestFixture struct {
	mediaType string
	body      []byte
	dgst      digest.Digest
}

func mustJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// buildImageManifest makes a single OCI (or Docker v2) image manifest with
// one config and one layer, distinguished by variant so each fixture has its
// own digest.
func buildImageManifest(t *testing.T, mediaType, variant string) manifestFixture {
	t.Helper()
	configBody := []byte(`{"architecture":"` + variant + `","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"config":{}}`)
	layerBody := []byte("catalog-test-layer-bytes-" + variant)
	m := v1.Manifest{
		MediaType: mediaType,
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    digest.FromBytes(configBody),
			Size:      int64(len(configBody)),
		},
	}
	m.Layers = append(m.Layers, v1.Descriptor{
		MediaType: v1.MediaTypeImageLayerGzip,
		Digest:    digest.FromBytes(layerBody),
		Size:      int64(len(layerBody)),
	})
	body := mustJSONBytes(t, m)
	return manifestFixture{mediaType: mediaType, body: body, dgst: digest.FromBytes(body)}
}

// buildIndex makes an OCI image index (or Docker manifest list) over the
// given child manifests. Children alternate amd64 / arm64 / no-platform, so
// platform extraction has something to skip.
func buildIndex(t *testing.T, mediaType string, children ...manifestFixture) manifestFixture {
	t.Helper()
	idx := v1.Index{MediaType: mediaType}
	for i, child := range children {
		d := v1.Descriptor{
			MediaType: child.mediaType,
			Digest:    child.dgst,
			Size:      int64(len(child.body)),
		}
		switch i % 3 {
		case 0:
			d.Platform = &v1.Platform{OS: "linux", Architecture: "amd64"}
		case 1:
			d.Platform = &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
		}
		idx.Manifests = append(idx.Manifests, d)
	}
	body := mustJSONBytes(t, idx)
	return manifestFixture{mediaType: mediaType, body: body, dgst: digest.FromBytes(body)}
}

// catalogFixture is the standard test catalog: five repositories; "alpha"
// carries a single manifest, a multi-platform index and a Docker v2
// manifest; the rest one tag each.
func catalogFixture(t *testing.T) (repos []registrytest.Repository, amd64Child manifestFixture, index manifestFixture, single manifestFixture, dockerSingle manifestFixture) {
	t.Helper()
	single = buildImageManifest(t, v1.MediaTypeImageManifest, "single")
	amd64Child = buildImageManifest(t, v1.MediaTypeImageManifest, "amd64")
	arm64Child := buildImageManifest(t, v1.MediaTypeImageManifest, "arm64")
	referrerChild := buildImageManifest(t, v1.MediaTypeImageManifest, "referrer")
	index = buildIndex(t, v1.MediaTypeImageIndex, amd64Child, arm64Child, referrerChild)
	dockerSingle = buildImageManifest(t, MediaTypeDockerManifest, "docker-single")
	dockerIndex := buildIndex(t, MediaTypeDockerManifestList, amd64Child)

	return []registrytest.Repository{
		{
			Name: "alpha",
			Tags: map[string]registrytest.Manifest{
				"v1":    {MediaType: single.mediaType, Body: single.body},
				"multi": {MediaType: index.mediaType, Body: index.body},
				"docker": {
					MediaType:     dockerSingle.mediaType,
					Body:          dockerSingle.body,
					ContentDigest: dockerSingle.dgst.String(), // honest by default
				},
				"dockerlist": {MediaType: dockerIndex.mediaType, Body: dockerIndex.body},
			},
			Manifests: []registrytest.Manifest{
				{MediaType: amd64Child.mediaType, Body: amd64Child.body},
				{MediaType: arm64Child.mediaType, Body: arm64Child.body},
				{MediaType: referrerChild.mediaType, Body: referrerChild.body},
			},
		},
		{Name: "beta/one", Tags: map[string]registrytest.Manifest{
			"1.0": {MediaType: single.mediaType, Body: single.body},
		}},
		{Name: "beta/two", Tags: map[string]registrytest.Manifest{
			"1.0": {MediaType: single.mediaType, Body: single.body},
		}},
		{Name: "gamma", Tags: map[string]registrytest.Manifest{
			"latest": {MediaType: single.mediaType, Body: single.body},
		}},
		{Name: "zeta", Tags: map[string]registrytest.Manifest{
			"edge": {MediaType: single.mediaType, Body: single.body},
		}},
	}, amd64Child, index, single, dockerSingle
}

func catalogCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var wantCatalog = []string{"alpha", "beta/one", "beta/two", "gamma", "zeta"}

func TestLinkNext(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		want    string // resolved target, "" for none
	}{
		{
			name:    "registry-style relative link",
			headers: []string{`</v2/_catalog?last=beta%2Fone&n=2>; rel="next"`},
			want:    "/v2/_catalog?last=beta%2Fone&n=2",
		},
		{
			name:    "absolute link",
			headers: []string{`<https://reg.example.com/v2/_catalog?last=a>; rel="next"`},
			want:    "https://reg.example.com/v2/_catalog?last=a",
		},
		{
			name:    "unquoted rel",
			headers: []string{`</v2/_catalog?last=a>; rel=next`},
			want:    "/v2/_catalog?last=a",
		},
		{
			name:    "other rels ignored",
			headers: []string{`</v2/_catalog?last=a>; rel="prev", </v2/_catalog?last=b>; rel="next"`},
			want:    "/v2/_catalog?last=b",
		},
		{
			name:    "multiple params and values",
			headers: []string{`</v2/tags/list?last=x>; title="page 2"; rel="next"`},
			want:    "/v2/tags/list?last=x",
		},
		{
			name:    "no link header",
			headers: nil,
			want:    "",
		},
		{
			name:    "link without rel",
			headers: []string{`</v2/_catalog?last=a>`},
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linkNext(tt.headers)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("linkNext = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("linkNext = nil, want %s", tt.want)
			}
			if got.String() != tt.want {
				t.Errorf("linkNext = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestValidRepositoryAndTagNames(t *testing.T) {
	for _, good := range []string{"a", "library/ubuntu", "foo/bar/baz", "a-b.c_d__e/x-1"} {
		if err := validRepositoryName(good); err != nil {
			t.Errorf("validRepositoryName(%q) = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{"", "Ubuntu", "-lead", "trail-", "a//b", "/a", "a/", "..", "a/b:c", "a b"} {
		if err := validRepositoryName(bad); err == nil {
			t.Errorf("validRepositoryName(%q) = nil, want error", bad)
		}
	}
	for _, good := range []string{"latest", "v1.0", "1.0.0-alpine", "_x", "A.B"} {
		if err := validTagName(good); err != nil {
			t.Errorf("validTagName(%q) = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{"", ".lead", "-lead", "with space", "a:b"} {
		if err := validTagName(bad); err == nil {
			t.Errorf("validTagName(%q) = nil, want error", bad)
		}
	}
}

func TestListRepositories(t *testing.T) {
	repos, _, _, _, _ := catalogFixture(t)

	names := func(rs []Repository) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.Name
		}
		return out
	}

	t.Run("single page without n", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, err := NewClient(s.Host(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.ListRepositories(catalogCtx(t), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 || names(got)[0] != "alpha" || names(got)[4] != "zeta" {
			t.Errorf("catalog = %v, want %v (sorted)", names(got), wantCatalog)
		}
		if reqs := len(s.Requests()); reqs != 1 {
			t.Errorf("requests = %d, want 1 (everything in one page)", reqs)
		}
	})

	t.Run("server-forced pagination over more than one page", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos, CatalogPageSize: 2})
		defer s.Close()
		c, err := NewClient(s.Host(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.ListRepositories(catalogCtx(t), 0)
		if err != nil {
			t.Fatal(err)
		}
		if names(got) != nil && len(got) != 5 {
			t.Errorf("catalog has %d entries, want 5", len(got))
		}
		wantNames := []string{"alpha", "beta/one", "beta/two", "gamma", "zeta"}
		for i, want := range wantNames {
			if got[i].Name != want {
				t.Errorf("catalog[%d] = %q, want %q (full walk, in order)", i, got[i].Name, want)
			}
		}
		catalogReqs := 0
		for _, r := range s.Requests() {
			if r.Path == "/v2/_catalog" {
				catalogReqs++
			}
		}
		if catalogReqs != 3 { // 2 + 2 + 1
			t.Errorf("catalog requests = %d, want 3", catalogReqs)
		}
		// Page 2 must resume after the last entry of page 1.
		resumed := false
		for _, r := range s.Requests() {
			if r.Path == "/v2/_catalog" && r.RawQuery != "" {
				q, err := url.ParseQuery(r.RawQuery)
				if err != nil {
					t.Fatal(err)
				}
				if q.Get("last") == "beta/one" {
					resumed = true
				}
			}
		}
		if !resumed {
			t.Error("no page resumed after last=beta/one — Link walking is broken")
		}
	})

	t.Run("client page size drives n", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, err := NewClient(s.Host(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.ListRepositories(catalogCtx(t), 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 {
			t.Errorf("catalog has %d entries, want 5", len(got))
		}
		for _, r := range s.Requests() {
			if r.Path != "/v2/_catalog" {
				continue
			}
			q, err := url.ParseQuery(r.RawQuery)
			if err != nil {
				t.Fatal(err)
			}
			if q.Get("n") != "2" {
				t.Errorf("catalog page %q carries n=%q, want 2", r.RawQuery, q.Get("n"))
			}
		}
	})

	t.Run("empty catalog", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		got, err := c.ListRepositories(catalogCtx(t), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("empty catalog returned %d entries", len(got))
		}
	})

	t.Run("cross-host pagination Link is refused", func(t *testing.T) {
		foreignHit := false
		foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			foreignHit = true
			w.WriteHeader(http.StatusOK)
		}))
		defer foreign.Close()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Link", `<`+foreign.URL+`/v2/_catalog?last=a>; rel="next"`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"repositories":["a"]}`))
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ListRepositories(catalogCtx(t), 0); err == nil {
			t.Fatal("expected error when Link points to a foreign host")
		}
		if foreignHit {
			t.Error("foreign host was contacted via pagination Link")
		}
	})
}

func TestListTags(t *testing.T) {
	repos, _, _, single, _ := catalogFixture(t)
	repo := repos[0] // alpha: v1, multi, docker, dockerlist

	t.Run("all tags in one page, sorted", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		got, err := c.ListTags(catalogCtx(t), repo.Name, 0)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"docker", "dockerlist", "multi", "v1"}
		if len(got) != len(want) {
			t.Fatalf("tags = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("tags[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("pagination over more than one page", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos, TagsPageSize: 2})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		got, err := c.ListTags(catalogCtx(t), repo.Name, 0)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"docker", "dockerlist", "multi", "v1"}
		if len(got) != 4 || got[0] != want[0] || got[3] != want[3] {
			t.Errorf("tags = %v, want %v", got, want)
		}
		tagReqs := 0
		for _, r := range s.Requests() {
			if r.Path == "/v2/alpha/tags/list" {
				tagReqs++
			}
		}
		if tagReqs != 2 {
			t.Errorf("tags requests = %d, want 2", tagReqs)
		}
	})

	t.Run("unknown repository", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		_, err := c.ListTags(catalogCtx(t), "nope/nope", 0)
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("err = %v, want *APIError", err)
		}
		if apiErr.StatusCode != 404 || apiErr.Code() != "NAME_UNKNOWN" {
			t.Errorf("api error = %d/%s, want 404/NAME_UNKNOWN", apiErr.StatusCode, apiErr.Code())
		}
	})

	t.Run("invalid repository name is refused client-side", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		_, err := c.ListTags(catalogCtx(t), "UPPER", 0)
		if err == nil {
			t.Fatal("invalid repository name must be refused before any request")
		}
		if got := len(s.Requests()); got != 0 {
			t.Errorf("requests = %d, want 0", got)
		}
	})

	_ = single
}

func TestGetTag(t *testing.T) {
	repos, amd64Child, index, single, dockerSingle := catalogFixture(t)

	t.Run("single OCI image manifest", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		ctx := catalogCtx(t)

		tag, err := c.GetTag(ctx, "alpha", "v1")
		if err != nil {
			t.Fatal(err)
		}
		if tag.Digest != single.dgst {
			t.Errorf("digest = %s, want %s", tag.Digest, single.dgst)
		}
		if tag.MediaType != v1.MediaTypeImageManifest {
			t.Errorf("media type = %s, want %s", tag.MediaType, v1.MediaTypeImageManifest)
		}
		if tag.IsIndex() || tag.Index != nil {
			t.Error("a single manifest must not parse as an index")
		}
		if tag.Manifest == nil {
			t.Fatal("manifest not parsed")
		}
		if tag.Manifest.Config.Digest != singleDigestOf(t, single) {
			t.Errorf("config digest = %s, want the fixture's", tag.Manifest.Config.Digest)
		}
		if len(tag.Manifest.Layers) != 1 {
			t.Errorf("layers = %d, want 1", len(tag.Manifest.Layers))
		}
		if tag.Repository != "alpha" || tag.Name != "v1" {
			t.Errorf("tag identity = %s/%s, want alpha/v1", tag.Repository, tag.Name)
		}
		if plats := tag.Platforms(); plats != nil {
			t.Errorf("Platforms() = %v, want nil for a single manifest", plats)
		}

		// The request must offer every manifest media type this client
		// understands.
		seen := false
		for _, r := range s.Requests() {
			if r.Path == "/v2/alpha/manifests/v1" {
				seen = true
				for _, mt := range []string{
					v1.MediaTypeImageManifest,
					v1.MediaTypeImageIndex,
					MediaTypeDockerManifest,
					MediaTypeDockerManifestList,
				} {
					if !strings.Contains(r.Accept, mt) {
						t.Errorf("Accept %q missing %q", r.Accept, mt)
					}
				}
			}
		}
		if !seen {
			t.Error("no manifest request recorded")
		}
	})

	t.Run("docker v2 image manifest", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		tag, err := c.GetTag(catalogCtx(t), "alpha", "docker")
		if err != nil {
			t.Fatal(err)
		}
		if tag.MediaType != MediaTypeDockerManifest || tag.Manifest == nil || tag.Digest != dockerSingle.dgst {
			t.Errorf("docker manifest tag = %+v", tag)
		}
	})

	t.Run("OCI index with platforms", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		tag, err := c.GetTag(catalogCtx(t), "alpha", "multi")
		if err != nil {
			t.Fatal(err)
		}
		if !tag.IsIndex() || tag.Manifest != nil {
			t.Fatalf("index tag = %+v, want IsIndex with no single manifest", tag)
		}
		if tag.Digest != index.dgst {
			t.Errorf("digest = %s, want %s", tag.Digest, index.dgst)
		}
		want := []v1.Platform{
			{OS: "linux", Architecture: "amd64"},
			{OS: "linux", Architecture: "arm64", Variant: "v8"},
		}
		got := tag.Platforms()
		if len(got) != len(want) {
			t.Fatalf("platforms = %v, want %v (no-platform child skipped)", got, want)
		}
		for i := range want {
			if got[i].OS != want[i].OS || got[i].Architecture != want[i].Architecture || got[i].Variant != want[i].Variant {
				t.Errorf("platforms[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
		// The index's children were NOT fetched with it: one manifest
		// request is all the server saw.
		manifestReqs := 0
		for _, r := range s.Requests() {
			if r.Path == "/v2/alpha/manifests/multi" {
				manifestReqs++
			}
		}
		if manifestReqs != 1 {
			t.Errorf("manifest requests = %d, want 1 (children are on-demand)", manifestReqs)
		}
	})

	t.Run("docker manifest list is an index", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		tag, err := c.GetTag(catalogCtx(t), "alpha", "dockerlist")
		if err != nil {
			t.Fatal(err)
		}
		if !tag.IsIndex() || tag.MediaType != MediaTypeDockerManifestList {
			t.Errorf("docker list tag = %+v, want an index of %s", tag, MediaTypeDockerManifestList)
		}
	})

	t.Run("index child fetched on demand by digest", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		ctx := catalogCtx(t)

		indexTag, err := c.GetTag(ctx, "alpha", "multi")
		if err != nil {
			t.Fatal(err)
		}
		child := indexTag.Index.Manifests[0]
		got, err := c.GetManifestByDigest(ctx, "alpha", child.Digest)
		if err != nil {
			t.Fatal(err)
		}
		if got.Manifest == nil || got.Digest != child.Digest || got.Digest != amd64Child.dgst {
			t.Errorf("child manifest = %+v, want digest %s", got, child.Digest)
		}
		if got.Name != child.Digest.String() {
			t.Errorf("child Name = %q, want the digest string", got.Name)
		}
	})

	t.Run("manifest served with a wrong Docker-Content-Digest", func(t *testing.T) {
		bad := registrytest.Manifest{
			MediaType:     v1.MediaTypeImageManifest,
			Body:          single.body,
			ContentDigest: "sha256:" + strings.Repeat("0", 64),
		}
		s := registrytest.New(registrytest.Config{Repositories: []registrytest.Repository{
			{Name: "bad", Tags: map[string]registrytest.Manifest{"v1": bad}},
		}})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		_, err := c.GetTag(catalogCtx(t), "bad", "v1")
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("err = %v, want ErrDigestMismatch", err)
		}
	})

	t.Run("unknown tag and repository", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		ctx := catalogCtx(t)

		_, err := c.GetTag(ctx, "alpha", "9.9.9-not-there")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code() != "MANIFEST_UNKNOWN" || apiErr.StatusCode != 404 {
			t.Errorf("unknown tag err = %v, want 404 MANIFEST_UNKNOWN", err)
		}
		_, err = c.GetTag(ctx, "nope/nope", "v1")
		if !errors.As(err, &apiErr) || apiErr.Code() != "NAME_UNKNOWN" {
			t.Errorf("unknown repo err = %v, want NAME_UNKNOWN", err)
		}
	})

	t.Run("invalid tag and digest are refused client-side", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: repos})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		ctx := catalogCtx(t)

		if _, err := c.GetTag(ctx, "alpha", "bad tag!"); err == nil {
			t.Error("invalid tag name must be refused")
		}
		if _, err := c.GetManifestByDigest(ctx, "alpha", digest.Digest("not-a-digest")); err == nil {
			t.Error("invalid digest must be refused")
		}
		if got := len(s.Requests()); got != 0 {
			t.Errorf("requests = %d, want 0", got)
		}
	})
}

// singleDigestOf re-derives the config digest of a buildImageManifest
// fixture (its Config descriptor's digest, from the parsed manifest).
func singleDigestOf(t *testing.T, fx manifestFixture) digest.Digest {
	t.Helper()
	var m v1.Manifest
	if err := json.Unmarshal(fx.body, &m); err != nil {
		t.Fatal(err)
	}
	return m.Config.Digest
}

// TestCatalogTagsManifestAuthScopes pins the bearer flow's scope handling
// end to end: the registry challenges each endpoint with its own scope, the
// client requests exactly that scope from the realm, and tokens are cached
// per scope (one grant per distinct scope).
func TestCatalogTagsManifestAuthScopes(t *testing.T) {
	repos, _, _, _, _ := catalogFixture(t)
	s := registrytest.New(registrytest.Config{
		Auth:         registrytest.AuthBearer,
		Username:     "alice",
		Password:     "wonderland",
		Repositories: repos,
	})
	defer s.Close()

	c, err := NewClient(s.Host(), Options{Credential: &Credential{Username: "alice", Password: "wonderland"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := catalogCtx(t)

	scopesOf := func(t *testing.T) map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, r := range s.TokenRequests() {
			q, err := url.ParseQuery(r.RawQuery)
			if err != nil {
				t.Fatal(err)
			}
			out[q.Get("scope")]++
		}
		return out
	}

	if _, err := c.ListRepositories(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := scopesOf(t)["registry:catalog:*"]; got != 1 {
		t.Errorf("catalog token requests with scope registry:catalog:* = %d, want 1", got)
	}

	if _, err := c.ListTags(ctx, "alpha", 0); err != nil {
		t.Fatal(err)
	}
	if got := scopesOf(t)["repository:alpha:pull"]; got != 1 {
		t.Errorf("tags token requests with scope repository:alpha:pull = %d, want 1", got)
	}

	// Same scope again: the token is cached, no new grant.
	if _, err := c.GetTag(ctx, "alpha", "v1"); err != nil {
		t.Fatal(err)
	}
	if got := scopesOf(t)["repository:alpha:pull"]; got != 1 {
		t.Errorf("manifest request re-fetched the same scope: %d grants, want 1", got)
	}

	// A different repository is a different scope and a new grant.
	if _, err := c.ListTags(ctx, "beta/one", 0); err != nil {
		t.Fatal(err)
	}
	if got := scopesOf(t)["repository:beta/one:pull"]; got != 1 {
		t.Errorf("beta/one token requests = %d, want 1", got)
	}

	// Three grants in total (catalog, alpha, beta/one); every /v2/ request
	// beyond the initial 401s carried a bearer token.
	if got := s.TokensIssued(); got != 3 {
		t.Errorf("tokens issued = %d, want 3 (one per scope)", got)
	}
	bearerless := 0
	for _, r := range s.Requests() {
		if r.Path != "/token" && strings.HasPrefix(r.Path, "/v2/") && r.Authorization == "" {
			bearerless++
		}
	}
	if bearerless != 4 { // the four first-contact 401s
		t.Errorf("%d /v2/ requests went out without a token, want 4 (the first-contact 401s)", bearerless)
	}
}
