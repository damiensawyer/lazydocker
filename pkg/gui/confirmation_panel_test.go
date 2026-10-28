package gui

import (
	"errors"
	"testing"

	"github.com/jesseduffield/gocui"
	"github.com/sirupsen/logrus"

	"github.com/jesseduffield/lazydocker/pkg/commands"
	"github.com/jesseduffield/lazydocker/pkg/gui/panels"
	"github.com/jesseduffield/lazydocker/pkg/gui/types"
	"github.com/jesseduffield/lazydocker/pkg/i18n"
)

// newHeadlessPromptTestGui builds a Gui just sufficient to exercise the
// confirmation prompt panels without a real terminal: a headless gocui (the
// same mode SetupFakeGui uses for the cheatsheet), the views the popup code
// touches, and the side panels the focus-stack code walks.
func newHeadlessPromptTestGui(t *testing.T) *Gui {
	t.Helper()

	log := logrus.NewEntry(logrus.New())
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("creating headless gui: %s", err)
	}
	t.Cleanup(g.Close)

	newView := func(name string) *gocui.View {
		view, err := g.SetView(name, 0, 0, 10, 5, 0)
		// SetView returns ErrUnknownView when it creates a brand-new view;
		// that is the expected first-call behaviour, not a failure.
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			t.Fatalf("creating view %q: %s", name, err)
		}
		return view
	}

	return &Gui{
		g:   g,
		Log: log,
		Tr:  i18n.NewTranslationSet(log, "en"),
		Views: Views{
			Confirmation: newView("confirmation"),
			Menu:         newView("menu"),
			Options:      newView("options"),
		},
		Panels: Panels{
			Projects:   &panels.SideListPanel[*commands.Project]{ListPanel: panels.ListPanel[*commands.Project]{View: newView("projects")}},
			Services:   &panels.SideListPanel[*commands.Service]{ListPanel: panels.ListPanel[*commands.Service]{View: newView("services")}},
			Containers: &panels.SideListPanel[*commands.Container]{ListPanel: panels.ListPanel[*commands.Container]{View: newView("containers")}},
			Images:     &panels.SideListPanel[*commands.Image]{ListPanel: panels.ListPanel[*commands.Image]{View: newView("images")}},
			Volumes:    &panels.SideListPanel[*commands.Volume]{ListPanel: panels.ListPanel[*commands.Volume]{View: newView("volumes")}},
			Networks:   &panels.SideListPanel[*commands.Network]{ListPanel: panels.ListPanel[*commands.Network]{View: newView("networks")}},
			Menu:       &panels.SideListPanel[*types.MenuItem]{ListPanel: panels.ListPanel[*types.MenuItem]{View: newView("menu")}},
		},
	}
}

func TestCreateMaskedPromptPanelMasksInput(t *testing.T) {
	gui := newHeadlessPromptTestGui(t)

	if err := gui.createMaskedPromptPanel("Enter password", nil); err != nil {
		t.Fatalf("createMaskedPromptPanel: %s", err)
	}

	if got := gui.Views.Confirmation.Mask; got != '*' {
		t.Fatalf("confirmation Mask = %q, want %q", got, '*')
	}
	if !gui.Views.Confirmation.Editable {
		t.Fatal("confirmation view should be editable in a prompt panel")
	}
	if !gui.Views.Confirmation.Visible {
		t.Fatal("confirmation view should be visible in a prompt panel")
	}
}

func TestCloseConfirmationPromptResetsMask(t *testing.T) {
	gui := newHeadlessPromptTestGui(t)
	gui.Views.Confirmation.Mask = '*'
	gui.Views.Confirmation.Visible = true

	if err := gui.closeConfirmationPrompt(); err != nil {
		t.Fatalf("closeConfirmationPrompt: %s", err)
	}

	if got := gui.Views.Confirmation.Mask; got != 0 {
		t.Fatalf("confirmation Mask = %q after close, want 0 (mask must not leak into the next popup)", got)
	}
	if gui.Views.Confirmation.Visible {
		t.Fatal("confirmation view should be hidden after close")
	}
}

// A prompt panel's confirm callback reads the typed input after the panel is
// closed (see handleCustomCommand), so closing must not wipe the buffer
// before the callback runs.
func TestPromptConfirmCallbackReadsTypedInput(t *testing.T) {
	gui := newHeadlessPromptTestGui(t)

	var seen string
	handleConfirm := func(g *gocui.Gui, v *gocui.View) error {
		seen = gui.trimmedContent(v)
		return nil
	}
	if err := gui.createPromptPanel("Enter command", handleConfirm); err != nil {
		t.Fatalf("createPromptPanel: %s", err)
	}

	_, _ = gui.Views.Confirmation.Write([]byte("my command"))

	// this is exactly what the Enter keybinding invokes
	if err := gui.wrappedConfirmationFunction(handleConfirm)(gui.g, gui.Views.Confirmation); err != nil {
		t.Fatalf("confirm handler: %s", err)
	}

	if seen != "my command" {
		t.Fatalf("confirm callback read %q, want %q", seen, "my command")
	}
}

// A secret typed into a masked prompt must not be visible in, nor mask, the
// next popup.
func TestPreviousPromptInputDoesNotLeakIntoNextPanel(t *testing.T) {
	gui := newHeadlessPromptTestGui(t)

	// first popup: a masked prompt filled with a secret
	if err := gui.createMaskedPromptPanel("Enter password", nil); err != nil {
		t.Fatalf("createMaskedPromptPanel: %s", err)
	}
	_, _ = gui.Views.Confirmation.Write([]byte("hunter2"))

	if err := gui.closeConfirmationPrompt(); err != nil {
		t.Fatalf("closeConfirmationPrompt: %s", err)
	}

	// second popup: an ordinary prompt
	if err := gui.createPromptPanel("Enter command", nil); err != nil {
		t.Fatalf("createPromptPanel: %s", err)
	}

	if got := gui.Views.Confirmation.Buffer(); got != "" {
		t.Fatalf("next prompt buffer = %q, want empty (previous input must not leak)", got)
	}
	if got := gui.Views.Confirmation.Mask; got != 0 {
		t.Fatalf("next prompt Mask = %q, want 0 (next prompt must not be masked)", got)
	}
}

// Non-editable popups must still show their content after the buffer is
// cleared on open. createPopupPanel cannot be driven directly in a headless
// test because its body runs inside g.Update, which is async and only
// executes while the gui event loop is running; so we exercise the same
// sequence it performs: prepareConfirmationPanel (clears) followed by
// setViewContent (the exact call renderString makes to render the prompt).
func TestPopupPanelRendersContentAfterClear(t *testing.T) {
	gui := newHeadlessPromptTestGui(t)
	_, _ = gui.Views.Confirmation.Write([]byte("stale content from a previous prompt"))

	if err := gui.prepareConfirmationPanel("Title", "Are you sure?"); err != nil {
		t.Fatalf("prepareConfirmationPanel: %s", err)
	}
	if err := gui.setViewContent(gui.Views.Confirmation, "Are you sure?"); err != nil {
		t.Fatalf("setViewContent: %s", err)
	}

	if got := gui.trimmedContent(gui.Views.Confirmation); got != "Are you sure?" {
		t.Fatalf("popup content = %q, want %q", got, "Are you sure?")
	}
}
