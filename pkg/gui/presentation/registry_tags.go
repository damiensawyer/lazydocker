package presentation

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jesseduffield/lazydocker/pkg/i18n"
	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/jesseduffield/lazydocker/pkg/utils"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func safeTr(tr *i18n.TranslationSet) *i18n.TranslationSet {
	if tr != nil {
		return tr
	}
	return i18n.EnglishSet()
}

// FormatPlatform formats a v1.Platform into a string (e.g. "linux/amd64", "linux/arm64/v8").
func FormatPlatform(p *v1.Platform) string {
	if p == nil {
		return ""
	}
	s := p.OS
	if p.Architecture != "" {
		if s != "" {
			s += "/" + p.Architecture
		} else {
			s = p.Architecture
		}
	}
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// FormatPlatforms formats a slice of v1.Platform into a comma-separated string.
func FormatPlatforms(platforms []v1.Platform) string {
	if len(platforms) == 0 {
		return ""
	}
	var list []string
	for _, p := range platforms {
		s := FormatPlatform(&p)
		if s != "" {
			list = append(list, s)
		}
	}
	return strings.Join(list, ", ")
}

// ShortenMediaType strips standard OCI and Docker prefixes to yield a compact name.
func ShortenMediaType(mediaType string) string {
	switch mediaType {
	case "application/vnd.oci.image.layer.v1.tar+gzip":
		return "tar+gzip"
	case "application/vnd.oci.image.layer.v1.tar":
		return "tar"
	case "application/vnd.oci.image.layer.v1.tar+zstd":
		return "tar+zstd"
	case "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip":
		return "nondistributable+gzip"
	case "application/vnd.oci.image.layer.nondistributable.v1.tar":
		return "nondistributable"
	case "application/vnd.oci.image.layer.nondistributable.v1.tar+zstd":
		return "nondistributable+zstd"
	case "application/vnd.docker.image.rootfs.diff.tar.gzip":
		return "tar.gzip"
	case "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip":
		return "foreign.tar.gzip"
	}

	s := mediaType
	s = strings.TrimPrefix(s, "application/vnd.oci.image.layer.v1.")
	s = strings.TrimPrefix(s, "application/vnd.oci.image.layer.")
	s = strings.TrimPrefix(s, "application/vnd.docker.image.rootfs.diff.")
	s = strings.TrimPrefix(s, "application/vnd.docker.image.rootfs.")
	s = strings.TrimPrefix(s, "application/vnd.")
	s = strings.TrimPrefix(s, "application/")
	return s
}

// ShortDigest returns the first 12 characters of a digest's encoded hash.
func ShortDigest(d digest.Digest) string {
	str := string(d)
	if str == "" {
		return ""
	}
	idx := strings.IndexByte(str, ':')
	var s string
	if idx >= 0 && idx < len(str)-1 {
		s = str[idx+1:]
	} else {
		s = str
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// CleanCreatedBy removes "/bin/sh -c #(nop) " and replaces tabs with spaces.
func CleanCreatedBy(createdBy string) string {
	const nopPrefix = "/bin/sh -c #(nop) "
	s := createdBy
	if strings.HasPrefix(s, nopPrefix) {
		s = strings.TrimPrefix(s, nopPrefix)
	} else if strings.Contains(s, nopPrefix) {
		s = strings.ReplaceAll(s, nopPrefix, "")
	}
	s = strings.TrimPrefix(s, "/bin/sh -c #(nop)")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}

// RenderTagSummary renders the summary tab for an inspected tag.
func RenderTagSummary(tr *i18n.TranslationSet, details *registry.TagDetails) string {
	if details == nil {
		return ""
	}
	tr = safeTr(tr)

	tagDisplay := details.Repository
	if details.Name != "" {
		if tagDisplay != "" {
			tagDisplay += ":" + details.Name
		} else {
			tagDisplay = details.Name
		}
	}

	indexDisplay := tr.No
	if details.IsIndex {
		indexDisplay = tr.Yes
	}

	platformsDisplay := FormatPlatforms(details.Platforms)
	if platformsDisplay == "" {
		if details.Platform != nil {
			platformsDisplay = FormatPlatform(details.Platform)
		} else {
			platformsDisplay = "-"
		}
	}

	selectedPlatformDisplay := "-"
	if details.Platform != nil {
		selectedPlatformDisplay = FormatPlatform(details.Platform)
	} else if details.IsIndex {
		selectedPlatformDisplay = tr.TagSummaryNoPlatformSelected
	}

	padding := 20
	output := ""
	output += utils.WithPadding(tr.TagSummaryTag, padding) + tagDisplay + "\n"
	if details.Digest != "" {
		output += utils.WithPadding(tr.TagSummaryDigest, padding) + details.Digest.String() + "\n"
	}
	if details.MediaType != "" {
		output += utils.WithPadding(tr.TagSummaryMediaType, padding) + details.MediaType + "\n"
	}
	output += utils.WithPadding(tr.TagSummaryIndex, padding) + indexDisplay + "\n"
	output += utils.WithPadding(tr.TagSummaryPlatforms, padding) + platformsDisplay + "\n"
	output += utils.WithPadding(tr.TagSummarySelectedPlatform, padding) + selectedPlatformDisplay + "\n"

	if !details.IsIndex || details.Platform != nil {
		output += utils.WithPadding(tr.TagSummaryTotalSize, padding) + utils.FormatDecimalBytes(int(details.TotalSize)) + "\n"
		output += utils.WithPadding(tr.TagSummaryLayerCount, padding) + fmt.Sprintf("%d", len(details.Layers)) + "\n"
		if details.Image != nil && details.Image.Created != nil && !details.Image.Created.IsZero() {
			output += utils.WithPadding(tr.TagSummaryCreated, padding) + details.Image.Created.Format(time.RFC1123) + "\n"
		}
	}

	if details.IsIndex && details.Platform == nil {
		output += "\n" + tr.TagSelectPlatformPrompt + "\n"
	}

	return strings.TrimRight(output, "\n")
}

// RenderTagLayers renders the layers tab (index, shortened media type, human-readable size, short digest).
func RenderTagLayers(tr *i18n.TranslationSet, details *registry.TagDetails) string {
	if details == nil {
		return ""
	}
	tr = safeTr(tr)

	if details.IsIndex && details.Platform == nil {
		return tr.TagSelectPlatform
	}

	if len(details.Layers) == 0 {
		return tr.TagNoLayers
	}

	headers := []string{
		tr.TagLayersHeaderIndex,
		tr.TagLayersHeaderMediaType,
		tr.TagLayersHeaderSize,
		tr.TagLayersHeaderDigest,
	}

	rows := make([][]string, len(details.Layers)+1)
	rows[0] = headers
	for i, l := range details.Layers {
		digestStr := ShortDigest(l.Digest)
		if digestStr == "" {
			digestStr = "-"
		}
		rows[i+1] = []string{
			fmt.Sprintf("%d", i),
			ShortenMediaType(l.MediaType),
			utils.FormatDecimalBytes(int(l.Size)),
			digestStr,
		}
	}

	out, err := utils.RenderTable(rows)
	if err != nil {
		return ""
	}
	return out
}

// RenderTagHistory renders the history tab (created, created_by trimmed of nop noise, empty-layer marker, comment).
func RenderTagHistory(tr *i18n.TranslationSet, details *registry.TagDetails) string {
	if details == nil {
		return ""
	}
	tr = safeTr(tr)

	if details.IsIndex && details.Platform == nil {
		return tr.TagSelectPlatform
	}

	if len(details.History) == 0 {
		return tr.TagNoHistory
	}

	headers := []string{
		tr.TagHistoryHeaderCreated,
		tr.TagHistoryHeaderCommand,
		tr.TagHistoryHeaderEmpty,
		tr.TagHistoryHeaderComment,
	}

	rows := make([][]string, len(details.History)+1)
	rows[0] = headers
	for i, h := range details.History {
		createdStr := "-"
		if h.Created != nil && !h.Created.IsZero() {
			createdStr = h.Created.Format(time.RFC3339)
		}
		cmdStr := CleanCreatedBy(h.CreatedBy)
		if cmdStr == "" {
			cmdStr = "-"
		}
		emptyMarker := ""
		if h.EmptyLayer {
			emptyMarker = tr.TagHistoryEmptyMarker
		}
		rows[i+1] = []string{
			createdStr,
			cmdStr,
			emptyMarker,
			h.Comment,
		}
	}

	out, err := utils.RenderTable(rows)
	if err != nil {
		return ""
	}
	return out
}

// RenderTagConfig renders the config tab (user, workdir, entrypoint, cmd, env sorted).
func RenderTagConfig(tr *i18n.TranslationSet, details *registry.TagDetails) string {
	if details == nil {
		return ""
	}
	tr = safeTr(tr)

	if details.IsIndex && details.Platform == nil {
		return tr.TagSelectPlatform
	}

	if details.Image == nil && len(details.Env) == 0 && len(details.Entrypoint) == 0 &&
		len(details.Cmd) == 0 && details.WorkingDir == "" && details.User == "" {
		return tr.TagNoConfig
	}

	padding := 14
	output := ""
	output += utils.WithPadding(tr.TagConfigUser, padding) + details.User + "\n"
	output += utils.WithPadding(tr.TagConfigWorkingDir, padding) + details.WorkingDir + "\n"
	output += utils.WithPadding(tr.TagConfigEntrypoint, padding) + strings.Join(details.Entrypoint, " ") + "\n"
	output += utils.WithPadding(tr.TagConfigCmd, padding) + strings.Join(details.Cmd, " ") + "\n"

	output += utils.WithPadding(tr.TagConfigEnv, padding)
	if len(details.Env) == 0 {
		output += "\n"
	} else {
		output += "\n"
		sortedEnv := make([]string, len(details.Env))
		copy(sortedEnv, details.Env)
		sort.Strings(sortedEnv)
		for _, env := range sortedEnv {
			output += "  " + env + "\n"
		}
	}

	return strings.TrimRight(output, "\n")
}

// RenderTagLabels renders the labels tab (sorted key = value).
func RenderTagLabels(tr *i18n.TranslationSet, details *registry.TagDetails) string {
	if details == nil {
		return ""
	}
	tr = safeTr(tr)

	if details.IsIndex && details.Platform == nil {
		return tr.TagSelectPlatform
	}

	if len(details.Labels) == 0 {
		return tr.TagNoLabels
	}

	keys := make([]string, 0, len(details.Labels))
	for k := range details.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = fmt.Sprintf("%s = %s", k, details.Labels[k])
	}
	return strings.Join(lines, "\n")
}

// RenderTagRootFS renders the rootfs tab (type + diff_ids).
func RenderTagRootFS(tr *i18n.TranslationSet, details *registry.TagDetails) string {
	if details == nil {
		return ""
	}
	tr = safeTr(tr)

	if details.IsIndex && details.Platform == nil {
		return tr.TagSelectPlatform
	}

	if details.RootFS.Type == "" && len(details.RootFS.DiffIDs) == 0 {
		return tr.TagNoRootFS
	}

	padding := 12
	output := ""
	output += utils.WithPadding(tr.TagRootFSType, padding) + details.RootFS.Type + "\n"
	output += utils.WithPadding(tr.TagRootFSDiffIDs, padding)
	if len(details.RootFS.DiffIDs) == 0 {
		output += "\n"
	} else {
		output += "\n"
		for _, id := range details.RootFS.DiffIDs {
			output += "  " + id.String() + "\n"
		}
	}

	return strings.TrimRight(output, "\n")
}
