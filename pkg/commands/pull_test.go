package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/stretchr/testify/assert"
)

type fakePullCall struct {
	ref     string
	options image.PullOptions
}

type fakeImagePuller struct {
	calls []fakePullCall
	fn    func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

func (f *fakeImagePuller) ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
	f.calls = append(f.calls, fakePullCall{ref: refStr, options: options})
	if f.fn != nil {
		return f.fn(ctx, refStr, options)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (t *trackingReadCloser) Close() error {
	t.closed = true
	return nil
}

func newTestDockerCommand(t *testing.T, puller ImagePuller) *DockerCommand {
	t.Helper()
	cmd := &DockerCommand{
		Log: NewDummyLog(),
	}
	cmd.SetImagePuller(puller)
	t.Cleanup(func() { cmd.SetImagePuller(nil) })
	return cmd
}

func TestPullImage_ProgressStreamAndFolding(t *testing.T) {
	cannedStream := strings.Join([]string{
		`{"status":"Pulling from library/alpine","id":"3.19"}`,
		`{"status":"Pulling fs layer","progressDetail":{},"id":"4abcf2066143"}`,
		`{"status":"Pulling fs layer","progressDetail":{},"id":"c9fc342a1b22"}`,
		`{"status":"Downloading","progressDetail":{"current":512,"total":2048},"id":"4abcf2066143"}`,
		`{"status":"Downloading","progressDetail":{"current":1024,"total":4096},"id":"c9fc342a1b22"}`,
		`{"status":"Download complete","progressDetail":{},"id":"4abcf2066143"}`,
		`{"status":"Pull complete","progressDetail":{},"id":"4abcf2066143"}`,
		`{"status":"Extracting","progressDetail":{"current":2048,"total":4096},"id":"c9fc342a1b22"}`,
		`{"status":"Pull complete","progressDetail":{},"id":"c9fc342a1b22"}`,
		`{"status":"Digest: sha256:c5b1261d6d3e43071626931fc004f70149baeba2c8ec672bd4f27761f8e1ad6b"}`,
		`{"status":"Status: Downloaded newer image for alpine:3.19"}`,
	}, "\n") + "\n"

	rc := &trackingReadCloser{Reader: strings.NewReader(cannedStream)}
	fake := &fakeImagePuller{
		fn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return rc, nil
		},
	}

	cmd := newTestDockerCommand(t, fake)

	auth := &registry.AuthConfig{
		Username:      "alice",
		Password:      "top-secret-token",
		ServerAddress: "https://index.docker.io/v1/",
	}
	expectedAuth, err := registry.EncodeAuthConfig(*auth)
	if !assert.NoError(t, err) {
		return
	}

	var events []PullProgress
	var snapshots [][]string
	err = cmd.PullImage(context.Background(), "alpine:3.19", auth, func(p PullProgress) {
		events = append(events, p)
		snapshots = append(snapshots, FoldPullProgress(events))
	})
	if !assert.NoError(t, err) {
		return
	}
	assert.True(t, rc.closed, "ImagePull response body must be closed")

	if !assert.Len(t, fake.calls, 1) {
		return
	}
	assert.Equal(t, "docker.io/library/alpine:3.19", fake.calls[0].ref)
	assert.Equal(t, expectedAuth, fake.calls[0].options.RegistryAuth)

	if !assert.Len(t, events, 11) {
		return
	}
	assert.Equal(t, int64(512), events[3].Current)
	assert.Equal(t, int64(2048), events[3].Total)
	assert.Equal(t, int64(512), events[3].ProgressDetail.Current)
	assert.Equal(t, int64(2048), events[3].ProgressDetail.Total)

	// Verify mid-stream snapshot after both layers are downloading: each layer ID
	// occupies one line in order of first appearance.
	assert.Equal(t, []string{
		"3.19: Pulling from library/alpine",
		"4abcf2066143: Downloading (512.00B / 2.05kB)",
		"c9fc342a1b22: Downloading (1.02kB / 4.10kB)",
	}, snapshots[4])

	// Verify final folded output: both layers updated in place to "Pull complete",
	// followed by the id-less Digest and Status lines.
	finalLines := FoldPullProgress(events)
	assert.Equal(t, []string{
		"3.19: Pulling from library/alpine",
		"4abcf2066143: Pull complete",
		"c9fc342a1b22: Pull complete",
		"Digest: sha256:c5b1261d6d3e43071626931fc004f70149baeba2c8ec672bd4f27761f8e1ad6b",
		"Status: Downloaded newer image for alpine:3.19",
	}, finalLines)
}

func TestPullImage_AnonymousPull(t *testing.T) {
	fake := &fakeImagePuller{
		fn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{"status":"Pulling from org/app","id":"v1"}` + "\n")), nil
		},
	}

	cmd := newTestDockerCommand(t, fake)

	// Passing nil auth performs an anonymous pull; passing nil onProgress must not panic.
	err := cmd.PullImage(context.Background(), "ghcr.io/org/app:v1", nil, nil)
	if !assert.NoError(t, err) {
		return
	}

	if !assert.Len(t, fake.calls, 1) {
		return
	}
	assert.Equal(t, "ghcr.io/org/app:v1", fake.calls[0].ref)
	assert.Empty(t, fake.calls[0].options.RegistryAuth)
}

func TestPullImage_StreamErrorLines(t *testing.T) {
	t.Run("error field after partial progress", func(t *testing.T) {
		stream := strings.Join([]string{
			`{"status":"Pulling from org/private","id":"latest"}`,
			`{"status":"Pulling fs layer","id":"a1b2c3d4e5f6"}`,
			`{"errorDetail":{"message":"unauthorized: authentication required"},"error":"unauthorized: authentication required"}`,
		}, "\n")

		fake := &fakeImagePuller{
			fn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(stream)), nil
			},
		}

		cmd := newTestDockerCommand(t, fake)

		var progress []PullProgress
		err := cmd.PullImage(context.Background(), "ghcr.io/org/private:latest", nil, func(p PullProgress) {
			progress = append(progress, p)
		})
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "unauthorized: authentication required")
		assert.Len(t, progress, 2)
	})

	t.Run("errorDetail.message only", func(t *testing.T) {
		stream := `{"errorDetail":{"code":404,"message":"manifest for ghcr.io/org/app:missing not found"}}` + "\n"

		fake := &fakeImagePuller{
			fn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(stream)), nil
			},
		}

		cmd := newTestDockerCommand(t, fake)

		err := cmd.PullImage(context.Background(), "ghcr.io/org/app:missing", nil, nil)
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "manifest for ghcr.io/org/app:missing not found")
	})

	t.Run("malformed JSON stream", func(t *testing.T) {
		fake := &fakeImagePuller{
			fn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewBufferString(`{"status":"ok"}` + "\nnot-json\n")), nil
			},
		}

		cmd := newTestDockerCommand(t, fake)

		err := cmd.PullImage(context.Background(), "ghcr.io/org/app:v1", nil, nil)
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "decoding image pull stream")
	})
}

func TestPullImage_ReferenceValidation(t *testing.T) {
	invalidRefs := []struct {
		name string
		ref  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"leading whitespace", " alpine:latest"},
		{"trailing whitespace", "alpine:latest "},
		{"uppercase repository", "ghcr.io/Org/Repo:latest"},
		{"64-char hex identifier", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"invalid tag characters", "ghcr.io/org/repo:bad@tag"},
		{"scheme in reference", "https://ghcr.io/org/repo:v1"},
	}

	for _, tc := range invalidRefs {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeImagePuller{}
			cmd := newTestDockerCommand(t, fake)

			err := cmd.PullImage(context.Background(), tc.ref, nil, nil)
			assert.Error(t, err)
			assert.Empty(t, fake.calls, "daemon must not be called when reference validation fails")
		})
	}

	validRefs := []struct {
		name        string
		ref         string
		expectedRef string
	}{
		{"familiar official image without tag", "ubuntu", "docker.io/library/ubuntu:latest"},
		{"familiar official image with tag", "ubuntu:24.04", "docker.io/library/ubuntu:24.04"},
		{"explicit registry with port", "localhost:5000/myteam/service:1.2.3", "localhost:5000/myteam/service:1.2.3"},
		{"digested reference", "ghcr.io/org/app@sha256:7cc4b5aefd1d0cadf8d97d4350462ba51c694ebca145b08d7d41b41acc8db5aa", "ghcr.io/org/app@sha256:7cc4b5aefd1d0cadf8d97d4350462ba51c694ebca145b08d7d41b41acc8db5aa"},
	}

	for _, tc := range validRefs {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeImagePuller{}
			cmd := newTestDockerCommand(t, fake)

			err := cmd.PullImage(context.Background(), tc.ref, nil, nil)
			if !assert.NoError(t, err) {
				return
			}
			if !assert.Len(t, fake.calls, 1) {
				return
			}
			assert.Equal(t, tc.expectedRef, fake.calls[0].ref)
		})
	}
}

func TestPullImage_AuthDomainMatching(t *testing.T) {
	t.Run("refuses mismatched registry domains before calling daemon", func(t *testing.T) {
		mismatchCases := []struct {
			name          string
			ref           string
			serverAddress string
		}{
			{"ghcr auth for docker hub image", "alpine:latest", "ghcr.io"},
			{"docker hub auth for ghcr image", "ghcr.io/owner/app:v1", "https://index.docker.io/v1/"},
			{"different port on localhost", "localhost:5000/app:latest", "http://localhost:5001"},
			{"subdomain spoofing", "ghcr.io/owner/app:v1", "https://ghcr.io.evil.example.com"},
			{"empty ServerAddress on non-nil AuthConfig", "ghcr.io/owner/app:v1", ""},
			{"embedded userinfo in ServerAddress", "ghcr.io/owner/app:v1", "https://evil.example.com@ghcr.io"},
			{"unsupported scheme in ServerAddress", "ghcr.io/owner/app:v1", "ftp://ghcr.io"},
		}

		for _, tc := range mismatchCases {
			t.Run(tc.name, func(t *testing.T) {
				fake := &fakeImagePuller{}
				cmd := newTestDockerCommand(t, fake)

				auth := &registry.AuthConfig{
					Username:      "bob",
					Password:      "hunter2-secret",
					ServerAddress: tc.serverAddress,
				}
				err := cmd.PullImage(context.Background(), tc.ref, auth, nil)
				if !assert.Error(t, err) {
					return
				}
				assert.ErrorIs(t, err, ErrAuthDomainMismatch)
				assert.Empty(t, fake.calls, "daemon must never be called when auth domain mismatches ref domain")
			})
		}
	})

	t.Run("accepts matching domains and Docker Hub aliases", func(t *testing.T) {
		matchCases := []struct {
			name          string
			ref           string
			serverAddress string
		}{
			{"familiar image with docker.io", "alpine:latest", "docker.io"},
			{"familiar image with legacy index URL", "alpine:latest", "https://index.docker.io/v1/"},
			{"familiar image with registry-1.docker.io", "alpine:latest", "registry-1.docker.io"},
			{"familiar image with registry.hub.docker.com", "alpine:latest", "https://registry.hub.docker.com"},
			{"registry-1 ref with index.docker.io auth", "registry-1.docker.io/library/alpine:latest", "index.docker.io"},
			{"ghcr ref with https URL", "ghcr.io/owner/app:v1", "https://ghcr.io/v2/"},
			{"localhost with port and http URL", "localhost:5000/app:v1", "http://localhost:5000"},
		}

		for _, tc := range matchCases {
			t.Run(tc.name, func(t *testing.T) {
				fake := &fakeImagePuller{}
				cmd := newTestDockerCommand(t, fake)

				auth := &registry.AuthConfig{
					Username:      "bob",
					Password:      "secret",
					ServerAddress: tc.serverAddress,
				}
				err := cmd.PullImage(context.Background(), tc.ref, auth, nil)
				if !assert.NoError(t, err) {
					return
				}
				if !assert.Len(t, fake.calls, 1) {
					return
				}
				assert.NotEmpty(t, fake.calls[0].options.RegistryAuth)
			})
		}
	})
}

func TestPullImage_NeverLeaksEncodedAuthOrSecretsInErrors(t *testing.T) {
	auth := &registry.AuthConfig{
		Username:      "secops",
		Password:      "pw-ultra-secret-998877",
		IdentityToken: "idtok-ultra-secret-112233",
		RegistryToken: "regtok-ultra-secret-445566",
		ServerAddress: "https://ghcr.io",
	}
	encodedAuth, err := registry.EncodeAuthConfig(*auth)
	if !assert.NoError(t, err) {
		return
	}
	assert.NotEmpty(t, encodedAuth)

	assertNoSecrets := func(t *testing.T, gotErr error) {
		t.Helper()
		if !assert.Error(t, gotErr) {
			return
		}
		msg := gotErr.Error()
		assert.NotContains(t, msg, encodedAuth, "encoded RegistryAuth must never appear in error string")
		assert.NotContains(t, msg, strings.TrimRight(encodedAuth, "="), "unpadded encoded RegistryAuth must never appear in error string")
		assert.NotContains(t, msg, auth.Password, "password must never appear in error string")
		assert.NotContains(t, msg, auth.IdentityToken, "identity token must never appear in error string")
		assert.NotContains(t, msg, auth.RegistryToken, "registry token must never appear in error string")
	}

	t.Run("SDK error echoing RegistryAuth header and secrets is scrubbed", func(t *testing.T) {
		fake := &fakeImagePuller{
			fn: func(_ context.Context, _ string, opts image.PullOptions) (io.ReadCloser, error) {
				return nil, fmt.Errorf("request failed with X-Registry-Auth=%s password=%s idtok=%s",
					opts.RegistryAuth, auth.Password, auth.IdentityToken)
			},
		}

		cmd := newTestDockerCommand(t, fake)

		gotErr := cmd.PullImage(context.Background(), "ghcr.io/org/app:v1", auth, nil)
		assertNoSecrets(t, gotErr)
		assert.Contains(t, gotErr.Error(), pullRedactedPlaceholder)
	})

	t.Run("stream error line echoing RegistryAuth or password is scrubbed", func(t *testing.T) {
		fake := &fakeImagePuller{
			fn: func(_ context.Context, _ string, opts image.PullOptions) (io.ReadCloser, error) {
				line := fmt.Sprintf(`{"error":"upstream rejected auth %s (regtok=%s)"}`+"\n", opts.RegistryAuth, auth.RegistryToken)
				return io.NopCloser(strings.NewReader(line)), nil
			},
		}

		cmd := newTestDockerCommand(t, fake)

		gotErr := cmd.PullImage(context.Background(), "ghcr.io/org/app:v1", auth, nil)
		assertNoSecrets(t, gotErr)
		assert.Contains(t, gotErr.Error(), pullRedactedPlaceholder)
	})

	t.Run("domain mismatch error never contains encoded auth or secrets", func(t *testing.T) {
		fake := &fakeImagePuller{}
		cmd := newTestDockerCommand(t, fake)

		gotErr := cmd.PullImage(context.Background(), "docker.io/library/alpine:latest", auth, nil)
		assertNoSecrets(t, gotErr)
		assert.ErrorIs(t, gotErr, ErrAuthDomainMismatch)
	})

	t.Run("canceled context error is returned cleanly without secrets", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		fake := &fakeImagePuller{}
		cmd := newTestDockerCommand(t, fake)

		gotErr := cmd.PullImage(ctx, "ghcr.io/org/app:v1", auth, nil)
		assertNoSecrets(t, gotErr)
		assert.True(t, errors.Is(gotErr, context.Canceled))
	})
}
