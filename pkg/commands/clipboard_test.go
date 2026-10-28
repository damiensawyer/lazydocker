package commands

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOSC52ExactBytes(t *testing.T) {
	text := "docker pull alpine:latest"
	b64 := base64.StdEncoding.EncodeToString([]byte(text))

	t.Run("plain OSC 52 bytes", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.SetGetenv(func(k string) string {
			return ""
		})

		expected := fmt.Sprintf("\x1b]52;c;%s\x07", b64)
		actual := c.BuildOSC52(text)
		assert.Equal(t, expected, actual)

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)
		c.SetClipboardMode(ClipboardModeOSC52Only)

		err := c.CopyToClipboard(text)
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, expected, buf.String())
	})

	t.Run("tmux passthrough OSC 52 bytes", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.SetGetenv(func(k string) string {
			if k == "TMUX" {
				return "/tmp/tmux-1000/default,1234,0"
			}
			return ""
		})

		inner := fmt.Sprintf("\x1b]52;c;%s\x07", b64)
		escaped := strings.ReplaceAll(inner, "\x1b", "\x1b\x1b")
		expected := "\x1bPtmux;\x1b" + escaped + "\x1b\\"

		actual := c.BuildOSC52(text)
		assert.Equal(t, expected, actual)

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)
		c.SetClipboardMode(ClipboardModeOSC52Only)

		err := c.CopyToClipboard(text)
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, expected, buf.String())
	})

	t.Run("GNU screen DCS passthrough OSC 52 bytes", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.SetGetenv(func(k string) string {
			if k == "STY" {
				return "12345.pts-0.host"
			}
			return ""
		})

		inner := fmt.Sprintf("\x1b]52;c;%s\x07", b64)
		expected := "\x1bP" + inner + "\x1b\\"

		actual := c.BuildOSC52(text)
		assert.Equal(t, expected, actual)

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)
		c.SetClipboardMode(ClipboardModeOSC52Only)

		err := c.CopyToClipboard(text)
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, expected, buf.String())
	})
}

func TestControlCharRefusal(t *testing.T) {
	c := NewDummyOSCommand()
	var buf bytes.Buffer
	c.SetClipboardWriter(&buf)
	c.SetClipboardMode(ClipboardModeOSC52Only)

	validCases := []struct {
		name string
		text string
	}{
		{"empty string", ""},
		{"simple pull command", "docker pull alpine:latest"},
		{"tab and newline allowed", "first line\twith tab\nsecond line\n"},
		{"unicode characters", "hello 🚀 世界! ü, é, å"},
		{"maximum allowed size exactly 100 KB", strings.Repeat("a", 100*1024)},
	}

	for _, tc := range validCases {
		t.Run("valid_"+tc.name, func(t *testing.T) {
			err := c.CopyToClipboard(tc.text)
			assert.NoError(t, err)
		})
	}

	invalidCases := []struct {
		name        string
		text        string
		expectedErr error
	}{
		{"escape character injection", "hello\x1bworld", ErrInvalidClipboardText},
		{"null byte injection", "hello\x00world", ErrInvalidClipboardText},
		{"bel character injection", "hello\x07world", ErrInvalidClipboardText},
		{"backspace", "hello\x08world", ErrInvalidClipboardText},
		{"carriage return", "hello\rworld", ErrInvalidClipboardText},
		{"form feed", "hello\x0cworld", ErrInvalidClipboardText},
		{"del character", "hello\x7fworld", ErrInvalidClipboardText},
		{"C1 control 0x80", "hello\u0080world", ErrInvalidClipboardText},
		{"C1 control 0x9f", "hello\u009fworld", ErrInvalidClipboardText},
		{"size over 100 KB", strings.Repeat("a", 100*1024+1), ErrClipboardTextTooLarge},
		{"large size 200 KB", strings.Repeat("a", 200*1024), ErrClipboardTextTooLarge},
	}

	for _, tc := range invalidCases {
		t.Run("invalid_"+tc.name, func(t *testing.T) {
			err := c.CopyToClipboard(tc.text)
			assert.ErrorIs(t, err, tc.expectedErr)
		})
	}
}

func TestFallbackSelectionLogic(t *testing.T) {
	type scenario struct {
		name         string
		os           string
		env          map[string]string
		available    map[string]string // tool name -> path
		expectedCmd  string
		expectedArgs []string
	}

	scenarios := []scenario{
		{
			name: "Wayland wl-copy",
			os:   "linux",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0",
				"DISPLAY":         ":0",
			},
			available: map[string]string{
				"wl-copy": "/usr/bin/wl-copy",
				"xclip":   "/usr/bin/xclip",
			},
			expectedCmd:  "/usr/bin/wl-copy",
			expectedArgs: nil,
		},
		{
			name: "Wayland without wl-copy falls back to X11 xclip",
			os:   "linux",
			env: map[string]string{
				"WAYLAND_DISPLAY": "wayland-0",
				"DISPLAY":         ":0",
			},
			available: map[string]string{
				"xclip": "/usr/bin/xclip",
			},
			expectedCmd:  "/usr/bin/xclip",
			expectedArgs: []string{"-selection", "clipboard"},
		},
		{
			name: "X11 xclip",
			os:   "linux",
			env: map[string]string{
				"DISPLAY": ":0",
			},
			available: map[string]string{
				"xclip": "/usr/bin/xclip",
				"xsel":  "/usr/bin/xsel",
			},
			expectedCmd:  "/usr/bin/xclip",
			expectedArgs: []string{"-selection", "clipboard"},
		},
		{
			name: "X11 without xclip falls back to xsel",
			os:   "linux",
			env: map[string]string{
				"DISPLAY": ":0",
			},
			available: map[string]string{
				"xsel": "/usr/bin/xsel",
			},
			expectedCmd:  "/usr/bin/xsel",
			expectedArgs: []string{"--clipboard", "--input"},
		},
		{
			name: "Darwin pbcopy",
			os:   "darwin",
			env: map[string]string{
				"DISPLAY": ":0",
			},
			available: map[string]string{
				"pbcopy": "/usr/bin/pbcopy",
				"xclip":  "/usr/bin/xclip",
			},
			expectedCmd:  "/usr/bin/pbcopy",
			expectedArgs: nil,
		},
		{
			name: "Windows clip.exe",
			os:   "windows",
			env:  map[string]string{},
			available: map[string]string{
				"clip.exe": "C:\\Windows\\System32\\clip.exe",
			},
			expectedCmd:  "C:\\Windows\\System32\\clip.exe",
			expectedArgs: nil,
		},
		{
			name: "Windows clip fallback",
			os:   "windows",
			env:  map[string]string{},
			available: map[string]string{
				"clip": "C:\\Windows\\System32\\clip",
			},
			expectedCmd:  "C:\\Windows\\System32\\clip",
			expectedArgs: nil,
		},
		{
			name: "WSL clip.exe via WSL_DISTRO_NAME",
			os:   "linux",
			env: map[string]string{
				"WSL_DISTRO_NAME": "Ubuntu",
			},
			available: map[string]string{
				"clip.exe": "/mnt/c/Windows/System32/clip.exe",
			},
			expectedCmd:  "/mnt/c/Windows/System32/clip.exe",
			expectedArgs: nil,
		},
		{
			name: "WSL clip.exe via WSL_INTEROP",
			os:   "linux",
			env: map[string]string{
				"WSL_INTEROP": "/run/WSL/1_interop",
			},
			available: map[string]string{
				"clip.exe": "/mnt/c/Windows/System32/clip.exe",
			},
			expectedCmd:  "/mnt/c/Windows/System32/clip.exe",
			expectedArgs: nil,
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			c := NewDummyOSCommand()
			c.Platform.os = s.os
			c.SetGetenv(func(k string) string {
				return s.env[k]
			})
			c.SetClipboardLookPath(func(tool string) (string, error) {
				if path, ok := s.available[tool]; ok {
					return path, nil
				}
				return "", os.ErrNotExist
			})

			var ranCmd string
			var ranArgs []string
			var ranStdin string

			c.SetClipboardRunner(func(name string, args []string, stdin string) error {
				ranCmd = name
				ranArgs = args
				ranStdin = stdin
				return nil
			})
			c.SetClipboardMode(ClipboardModeNativeOnly)

			text := "docker pull test:tag"
			err := c.CopyToClipboard(text)
			if !assert.NoError(t, err) {
				return
			}

			assert.Equal(t, s.expectedCmd, ranCmd)
			assert.Equal(t, s.expectedArgs, ranArgs)
			assert.Equal(t, text, ranStdin)
		})
	}
}

func TestStdinNeverAsArgument(t *testing.T) {
	c := NewDummyOSCommand()
	c.Platform.os = "linux"
	c.SetGetenv(func(k string) string {
		if k == "DISPLAY" {
			return ":0"
		}
		return ""
	})
	c.SetClipboardLookPath(func(tool string) (string, error) {
		if tool == "xclip" {
			return "/usr/bin/xclip", nil
		}
		return "", os.ErrNotExist
	})

	text := "secret payload --arg-injection"
	var receivedArgs []string
	var receivedStdin string

	c.SetClipboardRunner(func(name string, args []string, stdin string) error {
		receivedArgs = args
		receivedStdin = stdin
		return nil
	})
	c.SetClipboardMode(ClipboardModeNativeOnly)

	err := c.CopyToClipboard(text)
	if !assert.NoError(t, err) {
		return
	}

	for _, arg := range receivedArgs {
		assert.NotContains(t, arg, text)
		assert.NotContains(t, arg, "secret")
	}
	assert.Equal(t, text, receivedStdin)
}

func TestClipboardModes(t *testing.T) {
	t.Run("ClipboardModeOSC52Only does not call native runner", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "darwin"
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "/usr/bin/pbcopy", nil
		})

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)

		nativeCalled := false
		c.SetClipboardRunner(func(name string, args []string, stdin string) error {
			nativeCalled = true
			return nil
		})
		c.SetClipboardMode(ClipboardModeOSC52Only)

		err := c.CopyToClipboard("hello")
		if !assert.NoError(t, err) {
			return
		}
		assert.NotEmpty(t, buf.String())
		assert.False(t, nativeCalled)
	})

	t.Run("ClipboardModeNativeOnly does not write to TTY", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "darwin"
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "/usr/bin/pbcopy", nil
		})

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)

		nativeCalled := false
		c.SetClipboardRunner(func(name string, args []string, stdin string) error {
			nativeCalled = true
			return nil
		})
		c.SetClipboardMode(ClipboardModeNativeOnly)

		err := c.CopyToClipboard("hello")
		if !assert.NoError(t, err) {
			return
		}
		assert.Empty(t, buf.String())
		assert.True(t, nativeCalled)
	})

	t.Run("ClipboardModeNativeOnly returns error when no native tool found", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "linux"
		c.SetGetenv(func(k string) string { return "" })
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "", os.ErrNotExist
		})
		c.SetClipboardMode(ClipboardModeNativeOnly)

		err := c.CopyToClipboard("hello")
		assert.ErrorIs(t, err, ErrNoNativeClipboard)
	})

	t.Run("ClipboardModeDefault calls both OSC 52 and native when available", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "darwin"
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "/usr/bin/pbcopy", nil
		})

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)

		nativeCalled := false
		c.SetClipboardRunner(func(name string, args []string, stdin string) error {
			nativeCalled = true
			return nil
		})
		c.SetClipboardMode(ClipboardModeDefault)

		err := c.CopyToClipboard("hello")
		if !assert.NoError(t, err) {
			return
		}
		assert.NotEmpty(t, buf.String())
		assert.True(t, nativeCalled)
	})

	t.Run("ClipboardModeDefault succeeds if OSC 52 succeeds even if native fails", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "darwin"
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "/usr/bin/pbcopy", nil
		})

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)

		c.SetClipboardRunner(func(name string, args []string, stdin string) error {
			return errors.New("native tool crashed")
		})
		c.SetClipboardMode(ClipboardModeDefault)

		err := c.CopyToClipboard("hello")
		assert.NoError(t, err)
		assert.NotEmpty(t, buf.String())
	})

	t.Run("ClipboardModeDefault succeeds if native succeeds even if OSC 52 fails", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "darwin"
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "/usr/bin/pbcopy", nil
		})

		// Failing writer for OSC 52
		c.SetClipboardWriter(&failingWriter{})

		nativeCalled := false
		c.SetClipboardRunner(func(name string, args []string, stdin string) error {
			nativeCalled = true
			return nil
		})
		c.SetClipboardMode(ClipboardModeDefault)

		err := c.CopyToClipboard("hello")
		assert.NoError(t, err)
		assert.True(t, nativeCalled)
	})

	t.Run("ClipboardModeDefault fails if both OSC 52 and native fail", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "darwin"
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "/usr/bin/pbcopy", nil
		})

		c.SetClipboardWriter(&failingWriter{})
		c.SetClipboardRunner(func(name string, args []string, stdin string) error {
			return errors.New("native boom")
		})
		c.SetClipboardMode(ClipboardModeDefault)

		err := c.CopyToClipboard("hello")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "clipboard copy failed")
	})

	t.Run("ClipboardMode configured via environment variable", func(t *testing.T) {
		c := NewDummyOSCommand()
		c.Platform.os = "darwin"
		c.SetGetenv(func(k string) string {
			if k == "LAZYDOCKER_CLIPBOARD_MODE" {
				return "osc52"
			}
			return ""
		})
		c.SetClipboardLookPath(func(tool string) (string, error) {
			return "/usr/bin/pbcopy", nil
		})

		var buf bytes.Buffer
		c.SetClipboardWriter(&buf)
		nativeCalled := false
		c.SetClipboardRunner(func(name string, args []string, stdin string) error {
			nativeCalled = true
			return nil
		})

		err := c.CopyToClipboard("hello")
		if !assert.NoError(t, err) {
			return
		}
		assert.NotEmpty(t, buf.String())
		assert.False(t, nativeCalled)
	})
}

type failingWriter struct{}

func (f *failingWriter) Write(p []byte) (n int, err error) {
	return 0, errors.New("write failed")
}

func TestNilOSCommand(t *testing.T) {
	var c *OSCommand
	err := c.CopyToClipboard("hello")
	assert.Error(t, err)

	seq := c.BuildOSC52("hello")
	assert.NotEmpty(t, seq)

	assert.NotPanics(t, func() {
		c.SetClipboardMode(ClipboardModeDefault)
		c.SetClipboardWriter(nil)
		c.SetClipboardLookPath(nil)
		c.SetClipboardRunner(nil)
		c.SetGetenv(nil)
	})
}
