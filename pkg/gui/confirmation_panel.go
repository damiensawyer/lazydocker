// lots of this has been directly ported from one of the example files, will brush up later

// Copyright 2014 The gocui Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gui

import (
	"strings"

	"github.com/fatih/color"
	"github.com/jesseduffield/gocui"
)

func (gui *Gui) wrappedConfirmationFunction(function func(*gocui.Gui, *gocui.View) error) func(*gocui.Gui, *gocui.View) error {
	return func(g *gocui.Gui, v *gocui.View) error {
		if err := gui.closeConfirmationPrompt(); err != nil {
			return err
		}

		if function != nil {
			if err := function(g, v); err != nil {
				return err
			}
		}

		return nil
	}
}

func (gui *Gui) closeConfirmationPrompt() error {
	if err := gui.returnFocus(); err != nil {
		return err
	}
	gui.g.DeleteViewKeybindings("confirmation")
	gui.Views.Confirmation.Visible = false
	// Reset the input mask so a masked prompt (e.g. for a secret) does not
	// leak its masking into the next popup. The buffer is deliberately NOT
	// cleared here: a prompt panel's confirm callback reads the typed input
	// after this function returns (see handleCustomCommand). The buffer is
	// cleared when the next panel opens (see prepareConfirmationPanel).
	gui.Views.Confirmation.Mask = 0
	return nil
}

func (gui *Gui) getMessageHeight(wrap bool, message string, width int) int {
	lines := strings.Split(message, "\n")
	lineCount := 0
	// if we need to wrap, calculate height to fit content within view's width
	if wrap {
		for _, line := range lines {
			lineCount += len(line)/width + 1
		}
	} else {
		lineCount = len(lines)
	}
	return lineCount
}

func (gui *Gui) getConfirmationPanelDimensions(wrap bool, prompt string) (int, int, int, int) {
	width, height := gui.g.Size()
	panelWidth := width / 2
	panelHeight := gui.getMessageHeight(wrap, prompt, panelWidth)
	return width/2 - panelWidth/2,
		height/2 - panelHeight/2 - panelHeight%2 - 1,
		width/2 + panelWidth/2,
		height/2 + panelHeight/2
}

func (gui *Gui) createPromptPanel(title string, handleConfirm func(*gocui.Gui, *gocui.View) error) error {
	return gui.createPrefilledPromptPanel(title, "", handleConfirm)
}

func (gui *Gui) createPrefilledPromptPanel(title, initialValue string, handleConfirm func(*gocui.Gui, *gocui.View) error) error {
	gui.onNewPopupPanel()
	err := gui.prepareConfirmationPanel(title, "")
	if err != nil {
		return err
	}
	gui.Views.Confirmation.Editable = true
	if initialValue != "" {
		gui.Views.Confirmation.TextArea.TypeString(initialValue)
		gui.Views.Confirmation.RenderTextArea()
	}
	return gui.setKeyBindings(gui.g, handleConfirm, nil)
}

// createMaskedPromptPanel is the masked-input variant of createPromptPanel:
// typed characters are rendered as '*' so secrets (e.g. registry passwords)
// are not echoed to the screen. The mask is reset and the buffer cleared so
// the next popup neither masks nor shows the secret (see
// closeConfirmationPrompt and prepareConfirmationPanel).
func (gui *Gui) createMaskedPromptPanel(title string, handleConfirm func(*gocui.Gui, *gocui.View) error) error {
	gui.onNewPopupPanel()
	err := gui.prepareConfirmationPanel(title, "")
	if err != nil {
		return err
	}
	_ = gui.Views.Confirmation.SetOrigin(0, 0)
	_ = gui.Views.Confirmation.SetCursor(0, 0)
	gui.Views.Confirmation.Editable = true
	gui.Views.Confirmation.Mask = '*'
	return gui.setKeyBindings(gui.g, handleConfirm, nil)
}

func (gui *Gui) prepareConfirmationPanel(title, prompt string) error {
	x0, y0, x1, y1 := gui.getConfirmationPanelDimensions(true, prompt)
	confirmationView := gui.Views.Confirmation
	_, err := gui.g.SetView("confirmation", x0, y0, x1, y1, 0)
	if err != nil {
		return err
	}
	// Start from an empty buffer and reset cursor/origin to (0, 0) so input
	// left behind by a previous prompt (which may contain a masked secret or
	// a non-zero cursor column) never leaks into this popup. Non-editable
	// popups render their own content after this point.
	confirmationView.ClearTextArea()
	confirmationView.Title = title
	confirmationView.Visible = true
	gui.g.Update(func(g *gocui.Gui) error {
		return gui.switchFocus(confirmationView)
	})
	return nil
}

func (gui *Gui) onNewPopupPanel() {
	gui.Views.Menu.Visible = false
	gui.Views.Confirmation.Visible = false
}

// It is very important that within this function we never include the original prompt in any error messages, because it may contain e.g. a user password.
// The golangcilint unparam linter complains that handleClose is alwans nil but one day it won't be nil.
// nolint:unparam
func (gui *Gui) createConfirmationPanel(title, prompt string, handleConfirm, handleClose func(*gocui.Gui, *gocui.View) error) error {
	return gui.createPopupPanel(title, prompt, handleConfirm, handleClose)
}

func (gui *Gui) createPopupPanel(title, prompt string, handleConfirm, handleClose func(*gocui.Gui, *gocui.View) error) error {
	gui.onNewPopupPanel()
	gui.runOnUIThread(func(g *gocui.Gui) error {
		if gui.currentViewName() == "confirmation" {
			if err := gui.closeConfirmationPrompt(); err != nil {
				gui.Log.Error(err.Error())
			}
		}
		err := gui.prepareConfirmationPanel(title, prompt)
		if err != nil {
			return err
		}
		gui.Views.Confirmation.Editable = false
		// Already on the UI thread here, so set the content directly rather
		// than through renderString's second g.Update hop.
		if err := gui.setViewContent(gui.Views.Confirmation, prompt); err != nil {
			return err
		}
		return gui.setKeyBindings(g, handleConfirm, handleClose)
	})
	return nil
}

// runOnUIThread schedules f on the gocui event loop (g.Update), which is what
// makes popups safe to open from background goroutines. Tests set uiUpdate to
// run f synchronously, since a headless gui has no running event loop.
func (gui *Gui) runOnUIThread(f func(*gocui.Gui) error) {
	if gui.uiUpdate != nil {
		gui.uiUpdate(f)
		return
	}
	gui.g.Update(f)
}

func (gui *Gui) setKeyBindings(g *gocui.Gui, handleConfirm, handleClose func(*gocui.Gui, *gocui.View) error) error {
	wrappedConfirm := gui.wrappedConfirmationFunction(handleConfirm)
	wrappedClose := gui.wrappedConfirmationFunction(handleClose)

	gui.popupBindings = popupBindings{confirm: wrappedConfirm, close: wrappedClose}

	// would use a loop here but because the function takes an interface{} and slices of interfaces require even more boilerplate
	if err := g.SetKeybinding("confirmation", gocui.KeyEnter, gocui.ModNone, wrappedConfirm); err != nil {
		return err
	}
	if err := g.SetKeybinding("confirmation", 'y', gocui.ModNone, wrappedConfirm); err != nil {
		return err
	}

	if err := g.SetKeybinding("confirmation", gocui.KeyEsc, gocui.ModNone, wrappedClose); err != nil {
		return err
	}
	if err := g.SetKeybinding("confirmation", 'n', gocui.ModNone, wrappedClose); err != nil {
		return err
	}

	return nil
}

func (gui *Gui) createErrorPanel(message string) error {
	colorFunction := color.New(color.FgRed).SprintFunc()
	coloredMessage := colorFunction(strings.TrimSpace(message))
	return gui.createConfirmationPanel(gui.Tr.ErrorTitle, coloredMessage, nil, nil)
}

func (gui *Gui) renderConfirmationOptions() error {
	optionsMap := map[string]string{
		"n/esc":   gui.Tr.No,
		"y/enter": gui.Tr.Yes,
	}
	return gui.renderOptionsMap(optionsMap)
}
