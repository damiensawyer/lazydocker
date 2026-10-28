package commands

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unicode"
)

const (
	// MaxClipboardTextSize is the maximum allowed size (100 KB) for clipboard text.
	MaxClipboardTextSize = 100 * 1024
)

var (
	// ErrClipboardTextTooLarge is returned when clipboard text exceeds 100 KB.
	ErrClipboardTextTooLarge = errors.New("clipboard text exceeds maximum size (100 KB)")

	// ErrInvalidClipboardText is returned when clipboard text contains control characters
	// other than \t and \n.
	ErrInvalidClipboardText = errors.New("clipboard text contains invalid control characters")

	// ErrNoNativeClipboard is returned when ClipboardModeNativeOnly is used and no native tool is available.
	ErrNoNativeClipboard = errors.New("no supported native clipboard command found (wl-copy, xclip, xsel, pbcopy, clip.exe)")

	// custom overrides for testing and configuration without altering OSCommand struct layout.
	customClipboardModes   sync.Map // map[*OSCommand]ClipboardMode
	customTTYWriters       sync.Map // map[*OSCommand]io.Writer
	customLookPaths        sync.Map // map[*OSCommand]func(string) (string, error)
	customClipboardRunners sync.Map // map[*OSCommand]ClipboardRunner
)

// ClipboardMode defines the strategy used by CopyToClipboard.
type ClipboardMode int

const (
	// ClipboardModeDefault writes OSC 52 to the controlling terminal, and additionally
	// copies using a native clipboard tool if one is available.
	ClipboardModeDefault ClipboardMode = iota

	// ClipboardModeOSC52Only only emits OSC 52 escape sequences to the terminal.
	ClipboardModeOSC52Only

	// ClipboardModeNativeOnly only uses native system clipboard commands.
	ClipboardModeNativeOnly
)

// ClipboardRunner executes a clipboard command, passing text via stdin.
type ClipboardRunner func(name string, args []string, stdin string) error

// SetClipboardMode sets the clipboard mode on c.
func (c *OSCommand) SetClipboardMode(mode ClipboardMode) {
	if c == nil {
		return
	}
	customClipboardModes.Store(c, mode)
}

// SetClipboardWriter sets an io.Writer override for OSC 52 terminal output.
func (c *OSCommand) SetClipboardWriter(w io.Writer) {
	if c == nil {
		return
	}
	if w == nil {
		customTTYWriters.Delete(c)
		return
	}
	customTTYWriters.Store(c, w)
}

// SetClipboardLookPath sets an exec.LookPath override on c.
func (c *OSCommand) SetClipboardLookPath(lp func(string) (string, error)) {
	if c == nil {
		return
	}
	if lp == nil {
		customLookPaths.Delete(c)
		return
	}
	customLookPaths.Store(c, lp)
}

// SetClipboardRunner sets a command runner override on c.
func (c *OSCommand) SetClipboardRunner(runner ClipboardRunner) {
	if c == nil {
		return
	}
	if runner == nil {
		customClipboardRunners.Delete(c)
		return
	}
	customClipboardRunners.Store(c, runner)
}

// SetGetenv sets the environment lookup function on c.
func (c *OSCommand) SetGetenv(fn func(string) string) {
	if c != nil {
		c.getenv = fn
	}
}

func (c *OSCommand) getClipboardMode() ClipboardMode {
	if c != nil {
		if val, ok := customClipboardModes.Load(c); ok {
			if m, ok := val.(ClipboardMode); ok {
				return m
			}
		}
		if env := c.getEnv("LAZYDOCKER_CLIPBOARD_MODE"); env != "" {
			switch strings.ToLower(env) {
			case "osc52", "osc_52":
				return ClipboardModeOSC52Only
			case "native":
				return ClipboardModeNativeOnly
			case "default", "all":
				return ClipboardModeDefault
			}
		}
	}
	return ClipboardModeDefault
}

func (c *OSCommand) getEnv(key string) string {
	if c != nil && c.getenv != nil {
		return c.getenv(key)
	}
	return os.Getenv(key)
}

func (c *OSCommand) getOS() string {
	if c != nil && c.Platform != nil && c.Platform.os != "" {
		return c.Platform.os
	}
	return runtime.GOOS
}

func (c *OSCommand) isWSL() bool {
	return c.getEnv("WSL_DISTRO_NAME") != "" || c.getEnv("WSL_INTEROP") != ""
}

func (c *OSCommand) lookPath(file string) (string, error) {
	if c != nil {
		if val, ok := customLookPaths.Load(c); ok {
			if lp, ok := val.(func(string) (string, error)); ok && lp != nil {
				return lp(file)
			}
		}
	}
	return exec.LookPath(file)
}

func validateClipboardText(text string) error {
	if len(text) > MaxClipboardTextSize {
		return ErrClipboardTextTooLarge
	}
	for i := 0; i < len(text); i++ {
		b := text[i]
		if b < 32 || b == 127 {
			if b != '\t' && b != '\n' {
				return ErrInvalidClipboardText
			}
		}
	}
	for _, r := range text {
		if r == '\t' || r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			return ErrInvalidClipboardText
		}
	}
	return nil
}

// BuildOSC52 generates the OSC 52 sequence for text, wrapping it in tmux or screen
// DCS passthrough envelopes if running inside tmux or GNU screen.
func (c *OSCommand) BuildOSC52(text string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", b64)

	if c.getEnv("TMUX") != "" {
		// Inside tmux ($TMUX set) wrap in tmux passthrough:
		// "\x1bPtmux;" + seq with each ESC doubled + "\x1b\\" (tmux passthrough)
		escapedSeq := strings.ReplaceAll(seq, "\x1b", "\x1b\x1b")
		return "\x1bPtmux;" + escapedSeq + "\x1b\\"
	}

	if c.getEnv("STY") != "" {
		// Inside GNU screen ($STY set) use its DCS passthrough:
		// "\x1bP" + seq + "\x1b\\"
		return "\x1bP" + seq + "\x1b\\"
	}

	return seq
}

func (c *OSCommand) writeTTY(seq string) error {
	if c != nil {
		if val, ok := customTTYWriters.Load(c); ok {
			if w, ok := val.(io.Writer); ok && w != nil {
				_, err := io.WriteString(w, seq)
				return err
			}
		}
	}

	ttyPath := "/dev/tty"
	if c.getOS() == "windows" {
		ttyPath = "CONOUT$"
	}

	f, err := os.OpenFile(ttyPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("failed to open controlling terminal (%s): %w", ttyPath, err)
	}
	defer f.Close()

	_, err = io.WriteString(f, seq)
	return err
}

func (c *OSCommand) getNativeClipboardCmd() (string, []string, bool) {
	if c.getOS() == "darwin" {
		if path, err := c.lookPath("pbcopy"); err == nil {
			return path, nil, true
		}
		return "", nil, false
	}

	if c.getEnv("WAYLAND_DISPLAY") != "" {
		if path, err := c.lookPath("wl-copy"); err == nil {
			return path, nil, true
		}
	}

	if c.getEnv("DISPLAY") != "" {
		if path, err := c.lookPath("xclip"); err == nil {
			return path, []string{"-selection", "clipboard"}, true
		}
		if path, err := c.lookPath("xsel"); err == nil {
			return path, []string{"--clipboard", "--input"}, true
		}
	}

	if c.getOS() == "windows" {
		if path, err := c.lookPath("clip.exe"); err == nil {
			return path, nil, true
		}
		if path, err := c.lookPath("clip"); err == nil {
			return path, nil, true
		}
	}

	if c.isWSL() {
		if path, err := c.lookPath("clip.exe"); err == nil {
			return path, nil, true
		}
	}

	// Also check clip.exe if available in PATH and not on darwin
	if path, err := c.lookPath("clip.exe"); err == nil {
		return path, nil, true
	}

	return "", nil, false
}

func (c *OSCommand) runNativeClipboard(name string, args []string, stdin string) error {
	if c != nil {
		if val, ok := customClipboardRunners.Load(c); ok {
			if runner, ok := val.(ClipboardRunner); ok && runner != nil {
				return runner(name, args, stdin)
			}
		}
	}

	var cmd *exec.Cmd
	if c != nil && c.command != nil {
		cmd = c.command(name, args...)
	} else {
		cmd = exec.Command(name, args...)
	}
	cmd.Stdin = strings.NewReader(stdin)
	if c != nil {
		return c.RunExecutable(cmd)
	}
	return cmd.Run()
}

// CopyToClipboard copies text to the system clipboard.
// It enforces that text does not exceed 100 KB and contains no control characters
// other than \t and \n.
// By default, it writes an OSC 52 escape sequence to the controlling terminal (/dev/tty),
// and additionally executes a native clipboard tool (wl-copy, xclip, xsel, pbcopy, clip.exe)
// if one is available.
func (c *OSCommand) CopyToClipboard(text string) error {
	if c == nil {
		return errors.New("OSCommand is nil")
	}

	if err := validateClipboardText(text); err != nil {
		return err
	}

	mode := c.getClipboardMode()

	var osc52Err error
	var osc52Attempted bool

	if mode == ClipboardModeDefault || mode == ClipboardModeOSC52Only {
		osc52Attempted = true
		seq := c.BuildOSC52(text)
		osc52Err = c.writeTTY(seq)
		if mode == ClipboardModeOSC52Only {
			return osc52Err
		}
	}

	cmdName, cmdArgs, found := c.getNativeClipboardCmd()
	if !found {
		if mode == ClipboardModeNativeOnly {
			return ErrNoNativeClipboard
		}
		if osc52Err != nil {
			return fmt.Errorf("clipboard copy failed: OSC 52 failed (%v) and no native clipboard command available", osc52Err)
		}
		return nil
	}

	nativeErr := c.runNativeClipboard(cmdName, cmdArgs, text)
	if mode == ClipboardModeNativeOnly {
		return nativeErr
	}

	// ClipboardModeDefault:
	if osc52Attempted && osc52Err == nil {
		if nativeErr != nil && c.Log != nil {
			c.Log.Warn(fmt.Sprintf("native clipboard tool (%s) failed: %v", cmdName, nativeErr))
		}
		return nil
	}

	if nativeErr == nil {
		return nil
	}

	return fmt.Errorf("clipboard copy failed: OSC 52 error: %v; native (%s) error: %w", osc52Err, cmdName, nativeErr)
}
