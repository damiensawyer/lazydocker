package registry

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/jesseduffield/lazydocker/pkg/registry/registrytest"
)

type imageFixture struct {
	config     v1.Image
	configBody []byte
	configDgst digest.Digest
	layers     []v1.Descriptor
	manifest   manifestFixture
}

func buildDetailedImageFixture(t *testing.T, manifestMediaType, osName, arch, variant string, layerSizes []int64) imageFixture {
	t.Helper()

	t1 := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 28, 8, 5, 0, 0, time.UTC)

	diffIDs := make([]digest.Digest, len(layerSizes))
	layers := make([]v1.Descriptor, len(layerSizes))
	for i, sz := range layerSizes {
		raw := bytes.Repeat([]byte{byte('a' + i)}, int(sz))
		diffIDs[i] = digest.FromBytes(append([]byte("uncompressed-"), raw...))
		layers[i] = v1.Descriptor{
			MediaType: v1.MediaTypeImageLayerGzip,
			Digest:    digest.FromBytes(raw),
			Size:      sz,
		}
	}

	img := v1.Image{
		Created: &t2,
		Author:  "builder-" + arch,
		Platform: v1.Platform{
			OS:           osName,
			Architecture: arch,
			Variant:      variant,
		},
		Config: v1.ImageConfig{
			User:       "1000:1000",
			Env:        []string{"PATH=/usr/local/bin:/usr/bin", "ARCH=" + arch},
			Entrypoint: []string{"/bin/app"},
			Cmd:        []string{"--serve", "--port=8080"},
			WorkingDir: "/workspace",
			Labels: map[string]string{
				"org.opencontainers.image.title": "demo-" + arch,
				"org.opencontainers.image.arch":  arch,
			},
		},
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: diffIDs,
		},
		History: []v1.History{
			{
				Created:   &t1,
				CreatedBy: "/bin/sh -c #(nop) ADD file:base in /",
				Author:    "base-author",
			},
			{
				Created:    &t2,
				CreatedBy:  "/bin/sh -c #(nop) ENV ARCH=" + arch,
				Author:     "builder-" + arch,
				EmptyLayer: true,
			},
		},
	}

	configBody := mustJSONBytes(t, img)
	configDgst := digest.FromBytes(configBody)

	m := v1.Manifest{
		MediaType: manifestMediaType,
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    configDgst,
			Size:      int64(len(configBody)),
		},
		Layers: layers,
	}
	manifestBody := mustJSONBytes(t, m)

	return imageFixture{
		config:     img,
		configBody: configBody,
		configDgst: configDgst,
		layers:     layers,
		manifest: manifestFixture{
			mediaType: manifestMediaType,
			body:      manifestBody,
			dgst:      digest.FromBytes(manifestBody),
		},
	}
}

func TestGetTagDetailsSingleManifest(t *testing.T) {
	fx := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "amd64", "", []int64{120, 340, 50})
	dockerFx := buildDetailedImageFixture(t, MediaTypeDockerManifest, "linux", "arm64", "v8", []int64{200, 80})

	s := registrytest.New(registrytest.Config{
		Repositories: []registrytest.Repository{
			{
				Name: "app/server",
				Tags: map[string]registrytest.Manifest{
					"v1.0":   {MediaType: fx.manifest.mediaType, Body: fx.manifest.body},
					"docker": {MediaType: dockerFx.manifest.mediaType, Body: dockerFx.manifest.body},
				},
				Blobs: map[string][]byte{
					fx.configDgst.String():       fx.configBody,
					dockerFx.configDgst.String(): dockerFx.configBody,
				},
			},
		},
	})
	defer s.Close()

	c, err := NewClient(s.Host(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := catalogCtx(t)

	details, err := c.GetTagDetails(ctx, "app/server", "v1.0", nil)
	if err != nil {
		t.Fatalf("GetTagDetails: %v", err)
	}

	if details.Repository != "app/server" || details.Name != "v1.0" {
		t.Errorf("identity = %s:%s, want app/server:v1.0", details.Repository, details.Name)
	}
	if details.IsIndex {
		t.Error("IsIndex = true, want false for single manifest")
	}
	if details.Digest != fx.manifest.dgst || details.ManifestDigest != fx.manifest.dgst {
		t.Errorf("digests = (%s, %s), want %s", details.Digest, details.ManifestDigest, fx.manifest.dgst)
	}
	if details.ConfigDigest != fx.configDgst {
		t.Errorf("ConfigDigest = %s, want %s", details.ConfigDigest, fx.configDgst)
	}
	if details.Image == nil {
		t.Fatal("Image is nil")
	}
	if details.Platform == nil || details.Platform.OS != "linux" || details.Platform.Architecture != "amd64" {
		t.Errorf("Platform = %+v, want linux/amd64", details.Platform)
	}
	if len(details.Platforms) != 1 || details.Platforms[0].Architecture != "amd64" {
		t.Errorf("Platforms = %+v, want [linux/amd64]", details.Platforms)
	}

	// Verify config fields and top-level projections.
	if !reflect.DeepEqual(details.Env, fx.config.Config.Env) {
		t.Errorf("Env = %v, want %v", details.Env, fx.config.Config.Env)
	}
	if !reflect.DeepEqual(details.Entrypoint, fx.config.Config.Entrypoint) {
		t.Errorf("Entrypoint = %v, want %v", details.Entrypoint, fx.config.Config.Entrypoint)
	}
	if !reflect.DeepEqual(details.Cmd, fx.config.Config.Cmd) {
		t.Errorf("Cmd = %v, want %v", details.Cmd, fx.config.Config.Cmd)
	}
	if details.WorkingDir != "/workspace" {
		t.Errorf("WorkingDir = %q, want /workspace", details.WorkingDir)
	}
	if details.User != "1000:1000" {
		t.Errorf("User = %q, want 1000:1000", details.User)
	}
	if !reflect.DeepEqual(details.Labels, fx.config.Config.Labels) {
		t.Errorf("Labels = %v, want %v", details.Labels, fx.config.Config.Labels)
	}
	if len(details.History) != 2 || details.History[0].CreatedBy != fx.config.History[0].CreatedBy ||
		!details.History[1].EmptyLayer || details.History[1].Author != "builder-amd64" ||
		details.History[1].Created == nil || !details.History[1].Created.Equal(*fx.config.History[1].Created) {
		t.Errorf("History = %+v, want %+v", details.History, fx.config.History)
	}
	if details.RootFS.Type != "layers" || !reflect.DeepEqual(details.RootFS.DiffIDs, fx.config.RootFS.DiffIDs) {
		t.Errorf("RootFS = %+v, want %+v", details.RootFS, fx.config.RootFS)
	}

	// Verify layers and total compressed size (120 + 340 + 50 = 510).
	if len(details.Layers) != 3 {
		t.Fatalf("len(Layers) = %d, want 3", len(details.Layers))
	}
	for i, want := range fx.layers {
		got := details.Layers[i]
		if got.MediaType != want.MediaType || got.Digest != want.Digest || got.Size != want.Size {
			t.Errorf("Layers[%d] = %+v, want %+v", i, got, want)
		}
	}
	if details.TotalSize != 510 {
		t.Errorf("TotalSize = %d, want 510", details.TotalSize)
	}

	// Docker v2 manifest also works.
	dockerDetails, err := c.GetTagDetails(ctx, "app/server", "docker", nil)
	if err != nil {
		t.Fatalf("GetTagDetails(docker): %v", err)
	}
	if dockerDetails.TotalSize != 280 || dockerDetails.User != "1000:1000" {
		t.Errorf("dockerDetails = %+v, want TotalSize=280 User=1000:1000", dockerDetails)
	}
}

func TestGetTagDetailsIndex(t *testing.T) {
	amd64Fx := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "amd64", "", []int64{100, 250})
	arm64Fx := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "arm64", "v8", []int64{150, 300})
	referrerFx := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "", "", "", []int64{10})
	idxFx := buildIndex(t, v1.MediaTypeImageIndex, amd64Fx.manifest, arm64Fx.manifest, referrerFx.manifest)

	repo := registrytest.Repository{
		Name: "app/multi",
		Tags: map[string]registrytest.Manifest{
			"latest": {MediaType: idxFx.mediaType, Body: idxFx.body},
		},
		Manifests: []registrytest.Manifest{
			{MediaType: amd64Fx.manifest.mediaType, Body: amd64Fx.manifest.body},
			{MediaType: arm64Fx.manifest.mediaType, Body: arm64Fx.manifest.body},
			{MediaType: referrerFx.manifest.mediaType, Body: referrerFx.manifest.body},
		},
		Blobs: map[string][]byte{
			amd64Fx.configDgst.String(): amd64Fx.configBody,
			arm64Fx.configDgst.String(): arm64Fx.configBody,
		},
	}

	t.Run("nil platform on index returns platform list and no config", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: []registrytest.Repository{repo}})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})

		details, err := c.GetTagDetails(catalogCtx(t), "app/multi", "latest", nil)
		if err != nil {
			t.Fatalf("GetTagDetails(nil platform): %v", err)
		}
		if !details.IsIndex {
			t.Error("IsIndex = false, want true")
		}
		if details.Digest != idxFx.dgst {
			t.Errorf("Digest = %s, want %s", details.Digest, idxFx.dgst)
		}
		if details.Image != nil || details.Platform != nil || len(details.Layers) != 0 || details.TotalSize != 0 {
			t.Errorf("expected nil Image/Platform and empty Layers when platform is nil, got %+v", details)
		}
		wantPlatforms := []v1.Platform{
			{OS: "linux", Architecture: "amd64"},
			{OS: "linux", Architecture: "arm64", Variant: "v8"},
		}
		if !reflect.DeepEqual(details.Platforms, wantPlatforms) {
			t.Errorf("Platforms = %+v, want %+v", details.Platforms, wantPlatforms)
		}
		// Only the index manifest itself was fetched; no child manifest or blob requests.
		for _, r := range s.Requests() {
			if strings.Contains(r.Path, "/blobs/") {
				t.Errorf("unexpected blob request %+v when platform is nil", r)
			}
			if r.Path != "/v2/app/multi/manifests/latest" {
				t.Errorf("unexpected request %+v when platform is nil", r)
			}
		}
	})

	t.Run("chosen platform fetches child manifest by digest and its config blob", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: []registrytest.Repository{repo}})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		ctx := catalogCtx(t)

		// Select arm64 (with and without explicit v8 variant).
		for _, pick := range []v1.Platform{
			{OS: "linux", Architecture: "arm64", Variant: "v8"},
			{OS: "linux", Architecture: "arm64"},
		} {
			details, err := c.GetTagDetails(ctx, "app/multi", "latest", &pick)
			if err != nil {
				t.Fatalf("GetTagDetails(%+v): %v", pick, err)
			}
			if !details.IsIndex {
				t.Error("IsIndex = false, want true")
			}
			if details.Digest != idxFx.dgst {
				t.Errorf("Digest = %s, want index digest %s", details.Digest, idxFx.dgst)
			}
			if details.ManifestDigest != arm64Fx.manifest.dgst {
				t.Errorf("ManifestDigest = %s, want child digest %s", details.ManifestDigest, arm64Fx.manifest.dgst)
			}
			if details.ConfigDigest != arm64Fx.configDgst {
				t.Errorf("ConfigDigest = %s, want %s", details.ConfigDigest, arm64Fx.configDgst)
			}
			if details.Platform == nil || details.Platform.Architecture != "arm64" || details.Platform.Variant != "v8" {
				t.Errorf("Platform = %+v, want linux/arm64/v8", details.Platform)
			}
			if details.Image == nil || details.Image.Author != "builder-arm64" {
				t.Errorf("Image = %+v, want Author=builder-arm64", details.Image)
			}
			if details.TotalSize != 450 || len(details.Layers) != 2 {
				t.Errorf("TotalSize=%d len(Layers)=%d, want 450 and 2", details.TotalSize, len(details.Layers))
			}
		}
	})

	t.Run("unknown platform in index returns ErrPlatformNotFound", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{Repositories: []registrytest.Repository{repo}})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})

		_, err := c.GetTagDetails(catalogCtx(t), "app/multi", "latest", &v1.Platform{OS: "linux", Architecture: "s390x"})
		if !errors.Is(err, ErrPlatformNotFound) {
			t.Fatalf("err = %v, want ErrPlatformNotFound", err)
		}
		// Variant mismatch when variant is explicitly requested must also fail.
		_, err = c.GetTagDetails(catalogCtx(t), "app/multi", "latest", &v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v7"})
		if !errors.Is(err, ErrPlatformNotFound) {
			t.Fatalf("err = %v, want ErrPlatformNotFound for wrong variant", err)
		}
	})
}

func TestGetTagDetailsBlobRedirectNeverLeaksAuthorization(t *testing.T) {
	fx := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "amd64", "", []int64{64, 128})
	repo := registrytest.Repository{
		Name: "app/redirect",
		Tags: map[string]registrytest.Manifest{
			"v1": {MediaType: fx.manifest.mediaType, Body: fx.manifest.body},
		},
		Blobs: map[string][]byte{
			fx.configDgst.String(): fx.configBody,
		},
	}

	authCases := []struct {
		name string
		auth registrytest.AuthMode
		cred *Credential
	}{
		{
			name: "basic auth",
			auth: registrytest.AuthBasic,
			cred: &Credential{Username: "alice", Password: "wonderland"},
		},
		{
			name: "bearer auth with username and password",
			auth: registrytest.AuthBearer,
			cred: &Credential{Username: "alice", Password: "wonderland"},
		},
		{
			name: "bearer auth with identity token",
			auth: registrytest.AuthBearer,
			cred: &Credential{IdentityToken: "secret-identity-token"},
		},
	}

	for _, tc := range authCases {
		t.Run(tc.name, func(t *testing.T) {
			s := registrytest.New(registrytest.Config{
				Auth:         tc.auth,
				Username:     "alice",
				Password:     "wonderland",
				Repositories: []registrytest.Repository{repo},
			})
			defer s.Close()

			blobSrv := s.NewBlobRedirectServer()
			defer blobSrv.Close()

			c, err := NewClient(s.Host(), Options{Credential: tc.cred})
			if err != nil {
				t.Fatal(err)
			}

			details, err := c.GetTagDetails(catalogCtx(t), "app/redirect", "v1", nil)
			if err != nil {
				t.Fatalf("GetTagDetails via redirect: %v", err)
			}
			if details.Image == nil || details.WorkingDir != "/workspace" || details.TotalSize != 192 {
				t.Errorf("unexpected details after redirect: %+v", details)
			}

			// The second server must have served the blob and must NOT have
			// received any Authorization header.
			blobReqs := blobSrv.Requests()
			if len(blobReqs) != 1 {
				t.Fatalf("blob redirect server requests = %d, want 1", len(blobReqs))
			}
			if blobReqs[0].Authorization != "" {
				t.Errorf("redirected blob request carried Authorization %q to foreign server", blobReqs[0].Authorization)
			}
		})
	}
}

func TestGetTagDetailsBlobIntegrityAndErrors(t *testing.T) {
	fx := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "amd64", "", []int64{100})

	t.Run("corrupted config blob body fails with ErrDigestMismatch", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			Repositories: []registrytest.Repository{
				{
					Name: "app/tampered",
					Tags: map[string]registrytest.Manifest{
						"v1": {MediaType: fx.manifest.mediaType, Body: fx.manifest.body},
					},
					Blobs: map[string][]byte{
						fx.configDgst.String(): []byte(`{"architecture":"tampered"}`),
					},
				},
			},
		})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})

		_, err := c.GetTagDetails(catalogCtx(t), "app/tampered", "v1", nil)
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("err = %v, want ErrDigestMismatch", err)
		}
	})

	t.Run("missing config blob returns 404 ErrNotFound BLOB_UNKNOWN", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			Repositories: []registrytest.Repository{
				{
					Name: "app/missingblob",
					Tags: map[string]registrytest.Manifest{
						"v1": {MediaType: fx.manifest.mediaType, Body: fx.manifest.body},
					},
				},
			},
		})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})

		_, err := c.GetTagDetails(catalogCtx(t), "app/missingblob", "v1", nil)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want errors.Is(ErrNotFound)", err)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound || apiErr.Code() != "BLOB_UNKNOWN" {
			t.Errorf("apiErr = %+v, want 404 BLOB_UNKNOWN", apiErr)
		}
	})

	t.Run("oversized config blob is rejected by size limit", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/manifests/"):
				w.Header().Set("Content-Type", fx.manifest.mediaType)
				w.Header().Set("Docker-Content-Digest", fx.manifest.dgst.String())
				_, _ = w.Write(fx.manifest.body)
			case strings.Contains(r.URL.Path, "/blobs/"):
				w.Header().Set("Content-Type", "application/octet-stream")
				// Write maxConfigBlobBytes + 1 bytes so readBody trips the cap.
				_, _ = w.Write(make([]byte, maxConfigBlobBytes+1))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.GetTagDetails(catalogCtx(t), "app/huge", "v1", nil)
		if err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
			t.Fatalf("err = %v, want size limit error", err)
		}
	})
}

func TestTagsSharingDigest(t *testing.T) {
	fxA := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "amd64", "", []int64{100})
	fxB := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "arm64", "", []int64{200})
	fxUnused := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "386", "", []int64{50})

	s := registrytest.New(registrytest.Config{
		TagsPageSize: 2, // force multi-page tag listing
		Repositories: []registrytest.Repository{
			{
				Name: "app/shared",
				Tags: map[string]registrytest.Manifest{
					"1.0":    {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
					"1.0.0":  {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
					"latest": {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
					"2.0":    {MediaType: fxB.manifest.mediaType, Body: fxB.manifest.body},
				},
			},
		},
	})
	defer s.Close()

	c, err := NewClient(s.Host(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := catalogCtx(t)

	gotA, err := c.TagsSharingDigest(ctx, "app/shared", fxA.manifest.dgst)
	if err != nil {
		t.Fatalf("TagsSharingDigest(A): %v", err)
	}
	wantA := []string{"1.0", "1.0.0", "latest"}
	if !reflect.DeepEqual(gotA, wantA) {
		t.Errorf("TagsSharingDigest(A) = %v, want %v", gotA, wantA)
	}

	// Verify that every manifest probe used HEAD (not GET) and sent the
	// manifest Accept header.
	headCount := 0
	for _, r := range s.Requests() {
		if strings.HasPrefix(r.Path, "/v2/app/shared/manifests/") {
			if r.Method != http.MethodHead {
				t.Errorf("manifest request method = %s, want HEAD", r.Method)
			}
			if r.Accept != acceptManifests {
				t.Errorf("HEAD Accept = %q, want %q", r.Accept, acceptManifests)
			}
			headCount++
		}
	}
	if headCount != 4 {
		t.Errorf("HEAD requests = %d, want 4 (one per tag)", headCount)
	}

	gotB, err := c.TagsSharingDigest(ctx, "app/shared", fxB.manifest.dgst)
	if err != nil {
		t.Fatalf("TagsSharingDigest(B): %v", err)
	}
	if !reflect.DeepEqual(gotB, []string{"2.0"}) {
		t.Errorf("TagsSharingDigest(B) = %v, want [2.0]", gotB)
	}

	gotNone, err := c.TagsSharingDigest(ctx, "app/shared", fxUnused.manifest.dgst)
	if err != nil {
		t.Fatalf("TagsSharingDigest(unused): %v", err)
	}
	if len(gotNone) != 0 {
		t.Errorf("TagsSharingDigest(unused) = %v, want empty", gotNone)
	}

	// Client-side validation of repo and digest.
	if _, err := c.TagsSharingDigest(ctx, "BAD_REPO", fxA.manifest.dgst); err == nil {
		t.Error("expected error for invalid repository name")
	}
	if _, err := c.TagsSharingDigest(ctx, "app/shared", "not-a-digest"); err == nil {
		t.Error("expected error for invalid digest")
	}

	t.Run("omits Docker-Content-Digest on HEAD falls back to GET", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			OmitDigestOnHead: true,
			Repositories: []registrytest.Repository{
				{
					Name: "app/omit-head",
					Tags: map[string]registrytest.Manifest{
						"1.0":    {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
						"latest": {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
						"2.0":    {MediaType: fxB.manifest.mediaType, Body: fxB.manifest.body},
					},
				},
			},
		})
		defer s.Close()

		c, err := NewClient(s.Host(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.TagsSharingDigest(catalogCtx(t), "app/omit-head", fxA.manifest.dgst)
		if err != nil {
			t.Fatalf("TagsSharingDigest: %v", err)
		}
		want := []string{"1.0", "latest"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("TagsSharingDigest = %v, want %v", got, want)
		}

		// Verify both HEAD and GET were made, and GET carried acceptManifests.
		var heads, gets int
		for _, r := range s.Requests() {
			if strings.HasPrefix(r.Path, "/v2/app/omit-head/manifests/") {
				if r.Method == http.MethodHead {
					heads++
				}
				if r.Method == http.MethodGet {
					gets++
					if r.Accept != acceptManifests {
						t.Errorf("GET Accept = %q, want %q", r.Accept, acceptManifests)
					}
				}
			}
		}
		if heads != 3 || gets != 3 {
			t.Errorf("expected 3 HEAD and 3 GET requests, got heads=%d gets=%d", heads, gets)
		}
	})

	t.Run("HEAD 405 Method Not Allowed falls back to GET", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			ManifestHead405: true,
			Repositories: []registrytest.Repository{
				{
					Name: "app/head-405",
					Tags: map[string]registrytest.Manifest{
						"1.0":    {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
						"latest": {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
						"2.0":    {MediaType: fxB.manifest.mediaType, Body: fxB.manifest.body},
					},
				},
			},
		})
		defer s.Close()

		c, err := NewClient(s.Host(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.TagsSharingDigest(catalogCtx(t), "app/head-405", fxA.manifest.dgst)
		if err != nil {
			t.Fatalf("TagsSharingDigest: %v", err)
		}
		want := []string{"1.0", "latest"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("TagsSharingDigest = %v, want %v", got, want)
		}

		var heads, gets int
		for _, r := range s.Requests() {
			if strings.HasPrefix(r.Path, "/v2/app/head-405/manifests/") {
				if r.Method == http.MethodHead {
					heads++
				}
				if r.Method == http.MethodGet {
					gets++
					if r.Accept != acceptManifests {
						t.Errorf("GET Accept = %q, want %q", r.Accept, acceptManifests)
					}
				}
			}
		}
		if heads != 3 || gets != 3 {
			t.Errorf("expected 3 HEAD and 3 GET requests, got heads=%d gets=%d", heads, gets)
		}
	})

	t.Run("tag 404 between list and probe is safely skipped", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/v2/app/race/tags/list":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"name":"app/race","tags":["1.0","deleted"]}`))
			case r.URL.Path == "/v2/app/race/manifests/1.0":
				w.Header().Set("Content-Type", fxA.manifest.mediaType)
				w.Header().Set("Docker-Content-Digest", fxA.manifest.dgst.String())
				if r.Method == http.MethodGet {
					_, _ = w.Write(fxA.manifest.body)
				}
			case r.URL.Path == "/v2/app/race/manifests/deleted":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		c, err := NewClient(srv.URL, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.TagsSharingDigest(catalogCtx(t), "app/race", fxA.manifest.dgst)
		if err != nil {
			t.Fatalf("TagsSharingDigest: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"1.0"}) {
			t.Errorf("TagsSharingDigest = %v, want [1.0]", got)
		}
	})

	t.Run("undeterminable digest errors naming tag", func(t *testing.T) {
		t.Run("GET 500 error", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/app/err/tags/list":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"name":"app/err","tags":["flaky-tag"]}`))
				case r.URL.Path == "/v2/app/err/manifests/flaky-tag":
					if r.Method == http.MethodHead {
						w.WriteHeader(http.StatusMethodNotAllowed)
						return
					}
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"errors":[{"code":"INTERNAL","message":"disk fail"}]}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c, _ := NewClient(srv.URL, Options{})
			_, err := c.TagsSharingDigest(catalogCtx(t), "app/err", fxA.manifest.dgst)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "flaky-tag") {
				t.Errorf("err = %q, want it to name flaky-tag", err.Error())
			}
		})

		t.Run("malformed digest on HEAD", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/app/err/tags/list":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"name":"app/err","tags":["malformed-tag"]}`))
				case r.URL.Path == "/v2/app/err/manifests/malformed-tag":
					w.Header().Set("Docker-Content-Digest", "not-a-valid-digest")
					w.WriteHeader(http.StatusOK)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c, _ := NewClient(srv.URL, Options{})
			_, err := c.TagsSharingDigest(catalogCtx(t), "app/err", fxA.manifest.dgst)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "malformed-tag") {
				t.Errorf("err = %q, want it to name malformed-tag", err.Error())
			}
		})

		t.Run("digest mismatch on GET", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/app/err/tags/list":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"name":"app/err","tags":["mismatch-tag"]}`))
				case r.URL.Path == "/v2/app/err/manifests/mismatch-tag":
					if r.Method == http.MethodHead {
						w.WriteHeader(http.StatusOK)
						return
					}
					w.Header().Set("Content-Type", fxA.manifest.mediaType)
					w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("99", 32))
					_, _ = w.Write(fxA.manifest.body)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c, _ := NewClient(srv.URL, Options{})
			_, err := c.TagsSharingDigest(catalogCtx(t), "app/err", fxA.manifest.dgst)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrDigestMismatch) {
				t.Errorf("err = %v, want ErrDigestMismatch", err)
			}
			if !strings.Contains(err.Error(), "mismatch-tag") {
				t.Errorf("err = %q, want it to name mismatch-tag", err.Error())
			}
		})

		t.Run("HEAD 500 error", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/app/err/tags/list":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"name":"app/err","tags":["head-fail-tag"]}`))
				case r.URL.Path == "/v2/app/err/manifests/head-fail-tag":
					w.WriteHeader(http.StatusInternalServerError)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c, _ := NewClient(srv.URL, Options{})
			_, err := c.TagsSharingDigest(catalogCtx(t), "app/err", fxA.manifest.dgst)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "head-fail-tag") {
				t.Errorf("err = %q, want it to name head-fail-tag", err.Error())
			}
		})
	})
}

func TestDeleteManifest(t *testing.T) {
	fxA := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "amd64", "", []int64{100})
	fxB := buildDetailedImageFixture(t, v1.MediaTypeImageManifest, "linux", "arm64", "", []int64{200})

	repo := registrytest.Repository{
		Name: "app/delete",
		Tags: map[string]registrytest.Manifest{
			"1.0":    {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
			"latest": {MediaType: fxA.manifest.mediaType, Body: fxA.manifest.body},
			"2.0":    {MediaType: fxB.manifest.mediaType, Body: fxB.manifest.body},
		},
	}

	t.Run("refuses tag name client-side without touching the network", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			DeleteEnabled: true,
			Repositories:  []registrytest.Repository{repo},
		})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})

		if err := c.DeleteManifest(catalogCtx(t), "app/delete", "latest"); err == nil {
			t.Fatal("DeleteManifest must reject a tag name")
		}
		if err := c.DeleteManifest(catalogCtx(t), "INVALID/REPO", fxA.manifest.dgst); err == nil {
			t.Fatal("DeleteManifest must reject an invalid repository name")
		}
		if got := len(s.Requests()); got != 0 {
			t.Errorf("requests = %d, want 0", got)
		}
	})

	t.Run("delete disabled returns ErrDeleteUnsupported on 405", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			DeleteEnabled: false,
			Repositories:  []registrytest.Repository{repo},
		})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})

		err := c.DeleteManifest(catalogCtx(t), "app/delete", fxA.manifest.dgst)
		if !errors.Is(err, ErrDeleteUnsupported) {
			t.Fatalf("err = %v, want errors.Is(ErrDeleteUnsupported)", err)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("apiErr = %+v, want 405", apiErr)
		}
	})

	t.Run("UNSUPPORTED error code with non-405 status also matches ErrDeleteUnsupported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNSUPPORTED","message":"The operation is unsupported."}]}`))
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL, Options{})
		err := c.DeleteManifest(catalogCtx(t), "app/delete", fxA.manifest.dgst)
		if !errors.Is(err, ErrDeleteUnsupported) {
			t.Fatalf("err = %v, want errors.Is(ErrDeleteUnsupported)", err)
		}
	})

	t.Run("delete enabled removes all co-tagged references and toggle works", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			DeleteEnabled: false,
			Repositories:  []registrytest.Repository{repo},
		})
		defer s.Close()
		c, _ := NewClient(s.Host(), Options{})
		ctx := catalogCtx(t)

		// Initially disabled.
		if err := c.DeleteManifest(ctx, "app/delete", fxA.manifest.dgst); !errors.Is(err, ErrDeleteUnsupported) {
			t.Fatalf("before toggle: err = %v, want ErrDeleteUnsupported", err)
		}

		// Enable delete and verify blast radius before deleting.
		s.SetDeleteEnabled(true)
		shared, err := c.TagsSharingDigest(ctx, "app/delete", fxA.manifest.dgst)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(shared, []string{"1.0", "latest"}) {
			t.Fatalf("shared before delete = %v, want [1.0 latest]", shared)
		}

		if err := c.DeleteManifest(ctx, "app/delete", fxA.manifest.dgst); err != nil {
			t.Fatalf("DeleteManifest: %v", err)
		}

		// Both "1.0" and "latest" were removed; only "2.0" remains.
		remaining, err := c.ListTags(ctx, "app/delete", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(remaining, []string{"2.0"}) {
			t.Errorf("remaining tags = %v, want [2.0]", remaining)
		}

		// Deleting the same digest again yields 404 ErrNotFound.
		err = c.DeleteManifest(ctx, "app/delete", fxA.manifest.dgst)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("second delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run("200 OK on delete is also accepted", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c, _ := NewClient(srv.URL, Options{})
		if err := c.DeleteManifest(catalogCtx(t), "app/delete", fxA.manifest.dgst); err != nil {
			t.Fatalf("DeleteManifest(200 OK): %v", err)
		}
	})

	t.Run("401, 403 and 404 return typed errors", func(t *testing.T) {
		cases := []struct {
			status  int
			code    string
			wantErr error
		}{
			{status: http.StatusUnauthorized, code: "UNAUTHORIZED", wantErr: ErrUnauthorized},
			{status: http.StatusForbidden, code: "DENIED", wantErr: ErrForbidden},
			{status: http.StatusNotFound, code: "MANIFEST_UNKNOWN", wantErr: ErrNotFound},
		}
		for _, tc := range cases {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"errors":[{"code":"` + tc.code + `","message":"refused"}]}`))
			}))
			c, _ := NewClient(srv.URL, Options{})
			err := c.DeleteManifest(catalogCtx(t), "app/delete", fxA.manifest.dgst)
			srv.Close()

			if !errors.Is(err, tc.wantErr) {
				t.Errorf("status %d (%s): err = %v, want errors.Is(%v)", tc.status, tc.code, err, tc.wantErr)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || apiErr.Code() != tc.code {
				t.Errorf("status %d: apiErr = %+v", tc.status, apiErr)
			}
		}
	})

	t.Run("bearer auth challenges delete with repository delete scope", func(t *testing.T) {
		s := registrytest.New(registrytest.Config{
			Auth:          registrytest.AuthBearer,
			Username:      "alice",
			Password:      "wonderland",
			DeleteEnabled: true,
			Repositories:  []registrytest.Repository{repo},
		})
		defer s.Close()

		c, _ := NewClient(s.Host(), Options{
			Credential: &Credential{Username: "alice", Password: "wonderland"},
		})
		if err := c.DeleteManifest(catalogCtx(t), "app/delete", fxB.manifest.dgst); err != nil {
			t.Fatalf("DeleteManifest with bearer auth: %v", err)
		}

		toks := s.TokenRequests()
		if len(toks) != 1 {
			t.Fatalf("token requests = %d, want 1", len(toks))
		}
		q, err := url.ParseQuery(toks[0].RawQuery)
		if err != nil {
			t.Fatal(err)
		}
		if got := q.Get("scope"); got != "repository:app/delete:delete" {
			t.Errorf("delete token scope = %q, want repository:app/delete:delete", got)
		}
	})
}
