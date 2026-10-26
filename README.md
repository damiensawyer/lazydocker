# lazydocker (bots are going to kill us all edition)

A fork of [jesseduffield/lazydocker](https://github.com/jesseduffield/lazydocker) with a few extra features I wanted that aren't in upstream. The fork tracks upstream's `master`, so everything upstream lazydocker does still works the same way. For the basics (installation, configuration, all the standard keybindings) see the [upstream README](https://github.com/jesseduffield/lazydocker#readme).

Same idea as its sibling [lazygit fork](https://github.com/damiensawyer/lazygit): *Lazydocker - crazy bot edition*. This page covers only what the fork adds.

- [Resizable side panels](#resizable-side-panels)
- [Installation](#installation)

## Resizable side panels

You can change the width of the side panels from the keyboard, in both normal and half screen mode. Each screen mode keeps its own width, and adjustments are remembered for this machine.

| Key | Action |
|-----|--------|
| `<alt+shift+right>` | Widen the side panels |
| `<alt+shift+left>` | Narrow the side panels |
| `<alt+shift+=>` | Reset the width to the configured default |

Adjustments step by 0.05 and are clamped between 0.1 and 0.9. The defaults come from `gui.sidePanelWidth` (normal mode) and `gui.halfScreenModeSidePanelWidth` (half screen mode, default `0.5`). Full screen mode has no side/main split, so these keys do nothing there.

Widths are persisted per machine in a sqlite database in your temp folder (`$TMPDIR/lazydocker/state.db`), so they survive restarting lazydocker without your config file ever being touched. Resetting a width deletes its saved value. If the database can't be opened, lazydocker logs a warning and simply doesn't persist that session.

## Installation

Build it from source. You'll need [Go](https://go.dev/doc/install) 1.24 or newer.

```sh
git clone https://github.com/damiensawyer/lazydocker.git
cd lazydocker
go install
```

This installs the binary as `lazydocker` in `$(go env GOPATH)/bin`, so it replaces any upstream lazydocker installed with `go install`. Releases and package-manager builds (Homebrew, etc.) are upstream's and don't include these features.
