package dockerhub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Sentinel errors returned (wrapped) by the client. Test with errors.Is; the
// wrapped *StatusError carries the status code, the request path and the
// Hub's own message field. None of them ever carries credential material.
var (
	// ErrUnauthorized: the Hub answered 401 — missing, malformed or rejected
	// credentials.
	ErrUnauthorized = errors.New("docker hub: unauthorized")

	// ErrForbidden: the Hub answered 403 — the credential is valid but lacks
	// permission for the operation (or the anonymous pagination cap was hit).
	ErrForbidden = errors.New("docker hub: forbidden")

	// ErrNotFound: the Hub answered 404 — unknown namespace, repository or
	// tag.
	ErrNotFound = errors.New("docker hub: not found")

	// ErrLoginRequired: the operation needs a logged-in client but no
	// credential was configured. Returned before any request is made.
	ErrLoginRequired = errors.New("docker hub: login required")

	// ErrForeignNext: a pagination "next" URL pointed away from the Hub API
	// origin (different scheme or host) and was refused before any request
	// was made to it, so the JWT could not leak to a foreign host.
	ErrForeignNext = errors.New("docker hub: refused pagination URL on a foreign origin")

	// ErrInsecureHTTP: a credential was configured for a plain-HTTP non-loopback
	// Hub base URL and was refused so neither the password nor the JWT can
	// travel in cleartext over the network.
	ErrInsecureHTTP = errors.New("docker hub: refusing to send credentials over plain http to a non-loopback host")
)

// StatusError describes a non-2xx Hub API response. It unwraps to
// ErrUnauthorized, ErrForbidden or ErrNotFound for those status codes. The
// Hub's response "message" field is included verbatim (capped) because it is
// server-generated; request bodies — which carry the password on login — are
// never included, and the JWT never appears in any error.
type StatusError struct {
	// StatusCode is the HTTP status the Hub returned.
	StatusCode int
	// Status is the canonical status text, e.g. "401 Unauthorized".
	Status string
	// Method and Path identify the request that failed (path plus query).
	Method string
	Path   string
	// Message is the Hub's own "message" response field, when present.
	Message string
}

func (e *StatusError) Error() string {
	detail := fmt.Sprintf("docker hub: %s %s: %s", e.Method, e.Path, e.Status)
	if e.Message != "" {
		detail += ": " + e.Message
	}
	return detail
}

// Unwrap makes errors.Is pick up the status-specific sentinel.
func (e *StatusError) Unwrap() error {
	switch e.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	}
	return nil
}

// maxErrorMessageLen caps how much of the Hub's message field lands in an
// error string.
const maxErrorMessageLen = 512

// mapStatusError converts a non-2xx response into a *StatusError, consuming
// a bounded amount of the body to extract the Hub's "message" field.
func mapStatusError(method string, u *url.URL, resp *http.Response) error {
	err := &StatusError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Method:     method,
		Path:       u.Path,
	}
	if u.RawQuery != "" {
		err.Path += "?" + u.RawQuery
	}

	var body struct {
		Message string `json:"message"`
	}
	if raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); readErr == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if len(body.Message) > maxErrorMessageLen {
		body.Message = body.Message[:maxErrorMessageLen]
	}
	err.Message = strings.TrimSpace(body.Message)
	return err
}

// statusMessage returns the *StatusError message of err, or "" when err is
// not a StatusError.
func statusMessage(err error) string {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Message
	}
	return ""
}
