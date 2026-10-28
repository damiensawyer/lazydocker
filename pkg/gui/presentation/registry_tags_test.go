package presentation

import (
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/lazydocker/pkg/i18n"
	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
)

func sampleSingleImageDetails() *registry.TagDetails {
	tCreated := time.Date(2023, time.May, 1, 12, 0, 0, 0, time.UTC)
	tHist1 := time.Date(2023, time.May, 1, 11, 0, 0, 0, time.UTC)
	tHist2 := time.Date(2023, time.May, 1, 11, 30, 0, 0, time.UTC)

	return &registry.TagDetails{
		Repository: "library/alpine",
		Name:       "3.18",
		Digest:     digest.Digest("sha256:1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"),
		MediaType:  "application/vnd.oci.image.manifest.v1+json",
		IsIndex:    false,
		Platform:   &v1.Platform{OS: "linux", Architecture: "amd64"},
		Platforms:  []v1.Platform{{OS: "linux", Architecture: "amd64"}},
		Image: &v1.Image{
			Created: &tCreated,
			Platform: v1.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
		},
		Env:        []string{"PATH=/usr/local/bin:/usr/bin", "APP_ENV=production", "PORT=8080"},
		Entrypoint: []string{"/entrypoint.sh"},
		Cmd:        []string{"run", "--server"},
		WorkingDir: "/app",
		User:       "1000:1000",
		Labels: map[string]string{
			"version":    "3.18.0",
			"maintainer": "Alpine Team",
			"app":        "myapp",
		},
		History: []v1.History{
			{
				Created:    &tHist1,
				CreatedBy:  "/bin/sh -c #(nop) ADD file:abc in /",
				EmptyLayer: false,
				Comment:    "base layer",
			},
			{
				Created:    &tHist2,
				CreatedBy:  "/bin/sh -c #(nop)  CMD [\"run\" \"--server\"]",
				EmptyLayer: true,
				Comment:    "default command",
			},
		},
		Layers: []registry.Layer{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Size:      3456789,
				Digest:    digest.Digest("sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
			},
			{
				MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip",
				Size:      12345,
				Digest:    digest.Digest("sha256:9876543210fedcba9876543210fedcba9876543210fedcba9876543210fedcba"),
			},
		},
		TotalSize: 3456789 + 12345,
		RootFS: v1.RootFS{
			Type: "layers",
			DiffIDs: []digest.Digest{
				digest.Digest("sha256:diff1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"),
				digest.Digest("sha256:diff222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"),
			},
		},
	}
}

func sampleIndexDetailsWithoutPlatform() *registry.TagDetails {
	return &registry.TagDetails{
		Repository: "library/ubuntu",
		Name:       "latest",
		Digest:     digest.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		MediaType:  "application/vnd.oci.image.index.v1+json",
		IsIndex:    true,
		Platform:   nil,
		Platforms: []v1.Platform{
			{OS: "linux", Architecture: "amd64"},
			{OS: "linux", Architecture: "arm64", Variant: "v8"},
		},
	}
}

func TestRenderTagSummary_SingleImage(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleSingleImageDetails()
	out := RenderTagSummary(tr, details)

	assert.Contains(t, out, "Tag:                library/alpine:3.18")
	assert.Contains(t, out, "Digest:             sha256:1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff")
	assert.Contains(t, out, "Media type:         application/vnd.oci.image.manifest.v1+json")
	assert.Contains(t, out, "Index:              no")
	assert.Contains(t, out, "Platforms:          linux/amd64")
	assert.Contains(t, out, "Selected platform:  linux/amd64")
	assert.Contains(t, out, "Total size:         3.47MB")
	assert.Contains(t, out, "Layers:             2")
	assert.Contains(t, out, "Created:            Mon, 01 May 2023 12:00:00 UTC")
	assert.NotContains(t, out, "Select a platform to view details.")
}

func TestRenderTagSummary_IndexWithoutPlatform(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleIndexDetailsWithoutPlatform()
	out := RenderTagSummary(tr, details)

	assert.Contains(t, out, "Tag:                library/ubuntu:latest")
	assert.Contains(t, out, "Index:              yes")
	assert.Contains(t, out, "Platforms:          linux/amd64, linux/arm64/v8")
	assert.Contains(t, out, "Selected platform:  (none - select a platform)")
	assert.Contains(t, out, "Select a platform to view details.")
	assert.NotContains(t, out, "Total size:")
	assert.NotContains(t, out, "Layers:")
}

func TestRenderTagLayers_SingleImage(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleSingleImageDetails()
	out := RenderTagLayers(tr, details)

	assert.Contains(t, out, "#")
	assert.Contains(t, out, "MEDIA TYPE")
	assert.Contains(t, out, "SIZE")
	assert.Contains(t, out, "DIGEST")

	assert.Contains(t, out, "tar+gzip")
	assert.Contains(t, out, "tar.gzip")
	assert.Contains(t, out, "3.46MB")
	assert.Contains(t, out, "12.35kB")
	assert.Contains(t, out, "abcdef123456")
	assert.Contains(t, out, "9876543210fe")
}

func TestRenderTagLayers_IndexWithoutPlatform(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleIndexDetailsWithoutPlatform()
	out := RenderTagLayers(tr, details)

	assert.Equal(t, "Select a platform to view details.", out)
}

func TestRenderTagHistory_SingleImage(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleSingleImageDetails()
	out := RenderTagHistory(tr, details)

	assert.Contains(t, out, "CREATED")
	assert.Contains(t, out, "COMMAND")
	assert.Contains(t, out, "EMPTY")
	assert.Contains(t, out, "COMMENT")

	assert.Contains(t, out, "ADD file:abc in /")
	assert.Contains(t, out, "CMD [\"run\" \"--server\"]")
	assert.Contains(t, out, "<empty>")
	assert.Contains(t, out, "base layer")
	assert.Contains(t, out, "default command")
	assert.NotContains(t, out, "#(nop)")
}

func TestRenderTagHistory_IndexWithoutPlatform(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleIndexDetailsWithoutPlatform()
	out := RenderTagHistory(tr, details)

	assert.Equal(t, "Select a platform to view details.", out)
}

func TestRenderTagConfig_SingleImage(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleSingleImageDetails()
	out := RenderTagConfig(tr, details)

	assert.Contains(t, out, "User:         1000:1000")
	assert.Contains(t, out, "Working dir:  /app")
	assert.Contains(t, out, "Entrypoint:   /entrypoint.sh")
	assert.Contains(t, out, "Cmd:          run --server")
	assert.Contains(t, out, "Env:")

	lines := strings.Split(out, "\n")
	envIndex := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "Env:") {
			envIndex = i
			break
		}
	}
	assert.True(t, envIndex >= 0)
	// Assert sorted environment variables
	assert.Contains(t, lines[envIndex+1], "APP_ENV=production")
	assert.Contains(t, lines[envIndex+2], "PATH=/usr/local/bin:/usr/bin")
	assert.Contains(t, lines[envIndex+3], "PORT=8080")
}

func TestRenderTagConfig_IndexWithoutPlatform(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleIndexDetailsWithoutPlatform()
	out := RenderTagConfig(tr, details)

	assert.Equal(t, "Select a platform to view details.", out)
}

func TestRenderTagLabels_SingleImage(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleSingleImageDetails()
	out := RenderTagLabels(tr, details)

	expected := "app = myapp\nmaintainer = Alpine Team\nversion = 3.18.0"
	assert.Equal(t, expected, out)
}

func TestRenderTagLabels_IndexWithoutPlatform(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleIndexDetailsWithoutPlatform()
	out := RenderTagLabels(tr, details)

	assert.Equal(t, "Select a platform to view details.", out)
}

func TestRenderTagRootFS_SingleImage(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleSingleImageDetails()
	out := RenderTagRootFS(tr, details)

	assert.Contains(t, out, "Type:       layers")
	assert.Contains(t, out, "Diff IDs:")
	assert.Contains(t, out, "sha256:diff1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff")
	assert.Contains(t, out, "sha256:diff222233334444555566667777888899990000aaaabbbbccccddddeeeeffff")
}

func TestRenderTagRootFS_IndexWithoutPlatform(t *testing.T) {
	tr := i18n.EnglishSet()
	details := sampleIndexDetailsWithoutPlatform()
	out := RenderTagRootFS(tr, details)

	assert.Equal(t, "Select a platform to view details.", out)
}

func TestNilSafety(t *testing.T) {
	// All renderers must safely handle nil details
	assert.Equal(t, "", RenderTagSummary(nil, nil))
	assert.Equal(t, "", RenderTagLayers(nil, nil))
	assert.Equal(t, "", RenderTagHistory(nil, nil))
	assert.Equal(t, "", RenderTagConfig(nil, nil))
	assert.Equal(t, "", RenderTagLabels(nil, nil))
	assert.Equal(t, "", RenderTagRootFS(nil, nil))

	// All renderers must safely handle nil tr with empty details
	empty := &registry.TagDetails{}
	assert.NotEmpty(t, RenderTagSummary(nil, empty))
	assert.Equal(t, "No layers available", RenderTagLayers(nil, empty))
	assert.Equal(t, "No history available", RenderTagHistory(nil, empty))
	assert.Equal(t, "No configuration available", RenderTagConfig(nil, empty))
	assert.Equal(t, "No labels", RenderTagLabels(nil, empty))
	assert.Equal(t, "No RootFS information available", RenderTagRootFS(nil, empty))
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "", FormatPlatform(nil))
	assert.Equal(t, "linux/amd64", FormatPlatform(&v1.Platform{OS: "linux", Architecture: "amd64"}))
	assert.Equal(t, "linux/arm64/v8", FormatPlatform(&v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}))
	assert.Equal(t, "windows", FormatPlatform(&v1.Platform{OS: "windows"}))

	platforms := []v1.Platform{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "arm64", Variant: "v8"},
	}
	assert.Equal(t, "linux/amd64, linux/arm64/v8", FormatPlatforms(platforms))
	assert.Equal(t, "", FormatPlatforms(nil))

	assert.Equal(t, "tar+gzip", ShortenMediaType("application/vnd.oci.image.layer.v1.tar+gzip"))
	assert.Equal(t, "tar", ShortenMediaType("application/vnd.oci.image.layer.v1.tar"))
	assert.Equal(t, "tar+zstd", ShortenMediaType("application/vnd.oci.image.layer.v1.tar+zstd"))
	assert.Equal(t, "nondistributable+gzip", ShortenMediaType("application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"))
	assert.Equal(t, "tar.gzip", ShortenMediaType("application/vnd.docker.image.rootfs.diff.tar.gzip"))
	assert.Equal(t, "custom.layer", ShortenMediaType("application/vnd.custom.layer"))

	d := digest.Digest("sha256:1234567890abcdef1234567890abcdef")
	assert.Equal(t, "1234567890ab", ShortDigest(d))
	assert.Equal(t, "", ShortDigest(""))

	assert.Equal(t, "CMD [\"sh\"]", CleanCreatedBy("/bin/sh -c #(nop)  CMD [\"sh\"]"))
	assert.Equal(t, "ADD file:123 in /", CleanCreatedBy("/bin/sh -c #(nop) ADD file:123 in /"))
	assert.Equal(t, "/bin/sh -c apk add bash", CleanCreatedBy("/bin/sh -c apk add bash"))
	assert.Equal(t, "apk add bash", CleanCreatedBy("/bin/sh -c #(nop) apk add bash"))
	assert.Equal(t, "", CleanCreatedBy(""))
}
