package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/jesseduffield/lazydocker/pkg/utils"
)

const pullRedactedPlaceholder = "[REDACTED]"

var (
	// ErrAuthDomainMismatch is returned when an AuthConfig's ServerAddress does
	// not match the registry domain of the image reference being pulled.
	ErrAuthDomainMismatch = errors.New("registry auth server address does not match image reference domain")

	// ErrNilDockerClient is returned when PullImage is called on a DockerCommand
	// with neither an injected ImagePuller nor an initialized Docker SDK client.
	ErrNilDockerClient = errors.New("docker client is not initialized")

	// customImagePullers stores per-DockerCommand ImagePuller overrides so tests
	// can inject a fake SDK client without modifying DockerCommand's struct layout.
	customImagePullers sync.Map // map[*DockerCommand]ImagePuller
)

// ImagePuller abstracts the Docker SDK's ImagePull method so callers and tests
// can substitute a fake implementation returning a canned JSON stream.
type ImagePuller interface {
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

// PullProgressDetail holds byte progress counters for a single pull event.
type PullProgressDetail struct {
	Current int64 `json:"current,omitempty"`
	Total   int64 `json:"total,omitempty"`
}

// PullErrorDetail holds structured error details from a Docker pull stream line.
type PullErrorDetail struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// PullProgress represents a single progress event decoded from the Docker
// ImagePull JSON stream.
type PullProgress struct {
	ID             string             `json:"id,omitempty"`
	Status         string             `json:"status,omitempty"`
	ProgressDetail PullProgressDetail `json:"progressDetail,omitempty"`
	Error          string             `json:"error,omitempty"`
	ErrorDetail    PullErrorDetail    `json:"errorDetail,omitempty"`

	// Current and Total mirror ProgressDetail.Current and ProgressDetail.Total
	// for convenient top-level access.
	Current int64 `json:"-"`
	Total   int64 `json:"-"`
}

func (p PullProgress) currentBytes() int64 {
	if p.Current != 0 {
		return p.Current
	}
	return p.ProgressDetail.Current
}

func (p PullProgress) totalBytes() int64 {
	if p.Total != 0 {
		return p.Total
	}
	return p.ProgressDetail.Total
}

func (p PullProgress) errorMessage() string {
	if msg := strings.TrimSpace(p.Error); msg != "" {
		return msg
	}
	return strings.TrimSpace(p.ErrorDetail.Message)
}

// SetImagePuller registers an ImagePuller override on c (used by tests to
// supply a fake ImagePull stream without touching a Docker daemon). Passing nil
// clears any override and restores c.Client.
func (c *DockerCommand) SetImagePuller(puller ImagePuller) {
	if c == nil {
		return
	}
	if puller == nil {
		customImagePullers.Delete(c)
		return
	}
	customImagePullers.Store(c, puller)
}

func (c *DockerCommand) resolveImagePuller() ImagePuller {
	if c == nil {
		return nil
	}
	if v, ok := customImagePullers.Load(c); ok {
		if p, ok := v.(ImagePuller); ok && p != nil {
			return p
		}
	}
	if c.Client != nil {
		return c.Client
	}
	return nil
}

// PullImage pulls a remote image reference through the Docker daemon using the
// SDK's ImagePull endpoint.
//
// Security invariants:
//   - ref is validated with reference.ParseNormalizedNamed before contacting the daemon.
//   - If auth is non-nil, auth.ServerAddress must match the normalized registry
//     domain of ref (including Docker Hub aliases); mismatches are refused with
//     ErrAuthDomainMismatch before any daemon call is made.
//   - Credentials are encoded via registry.EncodeAuthConfig and passed solely in
//     image.PullOptions.RegistryAuth — never on a command line, never logged,
//     and scrubbed from any returned error string.
func (c *DockerCommand) PullImage(ctx context.Context, ref string, auth *registry.AuthConfig, onProgress func(PullProgress)) error {
	return pullImageWith(ctx, c.resolveImagePuller(), ref, auth, onProgress)
}

func pullImageWith(ctx context.Context, puller ImagePuller, ref string, auth *registry.AuthConfig, onProgress func(PullProgress)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return sanitizePullError(err, auth, "")
	}

	if strings.TrimSpace(ref) == "" || strings.TrimSpace(ref) != ref {
		return fmt.Errorf("invalid image reference %q: empty or contains leading/trailing whitespace", ref)
	}

	namedRef, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return fmt.Errorf("invalid image reference %q: %w", ref, err)
	}

	refDomain := normalizePullRegistryDomain(reference.Domain(namedRef))
	if refDomain == "" {
		return fmt.Errorf("invalid image reference %q: missing registry domain", ref)
	}

	var encodedAuth string
	if auth != nil {
		authDomain := normalizePullRegistryDomain(auth.ServerAddress)
		if authDomain == "" || authDomain != refDomain {
			return sanitizePullError(
				fmt.Errorf("%w: auth server %q does not match reference domain %q", ErrAuthDomainMismatch, authDomain, refDomain),
				auth,
				"",
			)
		}

		encodedAuth, err = registry.EncodeAuthConfig(*auth)
		if err != nil {
			return sanitizePullError(fmt.Errorf("encoding registry auth: %w", err), auth, "")
		}
	}

	if puller == nil {
		return ErrNilDockerClient
	}

	pullRef := reference.TagNameOnly(namedRef).String()
	body, err := puller.ImagePull(ctx, pullRef, image.PullOptions{
		RegistryAuth: encodedAuth,
	})
	if err != nil {
		return sanitizePullError(err, auth, encodedAuth)
	}
	if body == nil {
		return errors.New("image pull returned nil stream")
	}
	defer body.Close()

	if err := decodePullStream(body, onProgress); err != nil {
		return sanitizePullError(err, auth, encodedAuth)
	}
	return nil
}

// decodePullStream reads newline-delimited (or concatenated) JSON objects from
// r, emitting PullProgress values to onProgress and returning a final error if
// any line carries an error or if JSON decoding fails.
func decodePullStream(r io.Reader, onProgress func(PullProgress)) error {
	dec := json.NewDecoder(r)
	var streamErr error

	for {
		var ev PullProgress
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decoding image pull stream: %w", err)
		}

		ev.Current = ev.ProgressDetail.Current
		ev.Total = ev.ProgressDetail.Total
		if ev.Error == "" && ev.ErrorDetail.Message != "" {
			ev.Error = ev.ErrorDetail.Message
		}

		if errMsg := ev.errorMessage(); errMsg != "" {
			if streamErr == nil {
				streamErr = fmt.Errorf("image pull failed: %s", errMsg)
			}
			continue
		}

		if onProgress != nil {
			onProgress(ev)
		}
	}

	return streamErr
}

// FormatPullProgressLine formats a single PullProgress event into a human-readable
// single-line string suitable for display in the main view.
func FormatPullProgressLine(p PullProgress) string {
	id := strings.TrimSpace(p.ID)
	status := strings.TrimSpace(p.Status)
	current := p.currentBytes()
	total := p.totalBytes()

	if errMsg := p.errorMessage(); errMsg != "" {
		if id != "" {
			return fmt.Sprintf("%s: error: %s", id, errMsg)
		}
		return fmt.Sprintf("error: %s", errMsg)
	}

	var detail string
	if total > 0 && current >= 0 {
		detail = fmt.Sprintf(" (%s / %s)", utils.FormatDecimalBytes(int(current)), utils.FormatDecimalBytes(int(total)))
	} else if current > 0 {
		detail = fmt.Sprintf(" (%s)", utils.FormatDecimalBytes(int(current)))
	}

	if id != "" && status != "" {
		return fmt.Sprintf("%s: %s%s", id, status, detail)
	}
	if id != "" {
		return id + detail
	}
	return status + detail
}

// FoldPullProgress is a pure function that folds a sequence of PullProgress
// events into a render-ready slice of lines where each layer ID occupies one
// line (in order of first appearance) and is updated in place by subsequent
// events with the same ID. Events without an ID (such as final Digest and
// Status summary lines) are appended in order.
func FoldPullProgress(events []PullProgress) []string {
	lines := make([]string, 0, len(events))
	indexByID := make(map[string]int)

	for _, ev := range events {
		line := FormatPullProgressLine(ev)
		if line == "" {
			continue
		}

		id := strings.TrimSpace(ev.ID)
		if id != "" {
			if idx, exists := indexByID[id]; exists {
				lines[idx] = line
			} else {
				indexByID[id] = len(lines)
				lines = append(lines, line)
			}
			continue
		}

		lines = append(lines, line)
	}

	return lines
}

// normalizePullRegistryDomain normalizes a registry domain or ServerAddress URL
// to a canonical lowercase host[:port], collapsing Docker Hub aliases to
// "docker.io" and rejecting malformed inputs (such as embedded userinfo or
// query strings).
func normalizePullRegistryDomain(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s, " \t\r\n@?#") {
		return ""
	}

	var host string
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.User != nil || u.Host == "" || u.Hostname() == "" {
			return ""
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" {
			return ""
		}
		if u.Port() == "" {
			host = u.Hostname()
		} else {
			host = net.JoinHostPort(u.Hostname(), u.Port())
		}
	} else {
		host, _, _ = strings.Cut(s, "/")
		if host == "" || strings.Contains(host, "@") {
			return ""
		}
	}

	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return ""
	}

	switch host {
	case "docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com", "hub.docker.com":
		return "docker.io"
	default:
		return host
	}
}

// sanitizePullError guarantees that neither the encoded RegistryAuth value nor
// any raw credential fields in auth ever appear in a returned error message.
func sanitizePullError(err error, auth *registry.AuthConfig, encodedAuth string) error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	var sensitive []string

	addSensitive := func(val string) {
		if val == "" {
			return
		}
		sensitive = append(sensitive, val)
		if trimmed := strings.TrimRight(val, "="); trimmed != "" && trimmed != val {
			sensitive = append(sensitive, trimmed)
		}
	}

	addSensitive(encodedAuth)

	if auth != nil {
		if encoded, encErr := registry.EncodeAuthConfig(*auth); encErr == nil {
			addSensitive(encoded)
		}
		if rawJSON, jsonErr := json.Marshal(*auth); jsonErr == nil {
			addSensitive(base64.StdEncoding.EncodeToString(rawJSON))
			addSensitive(base64.URLEncoding.EncodeToString(rawJSON))
		}
		addSensitive(auth.Password)
		addSensitive(auth.IdentityToken)
		addSensitive(auth.RegistryToken)
		addSensitive(auth.Auth)
		if auth.Username != "" && auth.Password != "" {
			basic := auth.Username + ":" + auth.Password
			addSensitive(basic)
			addSensitive(base64.StdEncoding.EncodeToString([]byte(basic)))
			addSensitive(base64.URLEncoding.EncodeToString([]byte(basic)))
		}
	}

	redacted := msg
	for _, secret := range sensitive {
		if strings.Contains(redacted, secret) {
			redacted = strings.ReplaceAll(redacted, secret, pullRedactedPlaceholder)
		}
	}

	if redacted == msg {
		return err
	}
	if errors.Is(err, ErrAuthDomainMismatch) {
		return fmt.Errorf("%w: %s", ErrAuthDomainMismatch, redacted)
	}
	return errors.New(redacted)
}
