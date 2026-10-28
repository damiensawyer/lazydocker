package gui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazydocker/pkg/config"
	"github.com/jesseduffield/lazydocker/pkg/gui/panels"
	"github.com/jesseduffield/lazydocker/pkg/utils"
	"github.com/samber/lo"
	"github.com/spkg/bom"
)

func (gui *Gui) handleGoTo(view *gocui.View) func(g *gocui.Gui, v *gocui.View) error {
	return func(g *gocui.Gui, v *gocui.View) error {
		gui.resetMainView()
		return gui.switchFocus(view)
	}
}

func (gui *Gui) nextView(g *gocui.Gui, v *gocui.View) error {
	sideViewNames := gui.sideViewNames()
	var focusedViewName string
	if v == nil || v.Name() == sideViewNames[len(sideViewNames)-1] {
		focusedViewName = sideViewNames[0]
	} else {
		viewName := v.Name()
		for i := range sideViewNames {
			if viewName == sideViewNames[i] {
				focusedViewName = sideViewNames[i+1]
				break
			}
			if i == len(sideViewNames)-1 {
				gui.Log.Info("not in list of views")
				return nil
			}
		}
	}
	focusedView, err := g.View(focusedViewName)
	if err != nil {
		panic(err)
	}
	gui.resetMainView()
	return gui.switchFocus(focusedView)
}

func (gui *Gui) previousView(g *gocui.Gui, v *gocui.View) error {
	sideViewNames := gui.sideViewNames()
	var focusedViewName string
	if v == nil || v.Name() == sideViewNames[0] {
		focusedViewName = sideViewNames[len(sideViewNames)-1]
	} else {
		viewName := v.Name()
		for i := range sideViewNames {
			if viewName == sideViewNames[i] {
				focusedViewName = sideViewNames[i-1]
				break
			}
			if i == len(sideViewNames)-1 {
				gui.Log.Info("not in list of views")
				return nil
			}
		}
	}
	focusedView, err := g.View(focusedViewName)
	if err != nil {
		panic(err)
	}
	gui.resetMainView()
	return gui.switchFocus(focusedView)
}

func (gui *Gui) resetMainView() {
	gui.State.Panels.Main.ObjectKey = ""
	gui.Views.Main.Wrap = gui.Config.UserConfig.Gui.WrapMainPanel
}

// if the cursor down past the last item, move it to the last line
// nolint:unparam
func (gui *Gui) focusPoint(selectedX int, selectedY int, lineCount int, v *gocui.View) {
	if selectedY < 0 || selectedY > lineCount {
		return
	}
	ox, oy := v.Origin()
	originalOy := oy
	cx, cy := v.Cursor()
	originalCy := cy
	_, height := v.Size()

	ly := utils.Max(height-1, 0)

	windowStart := oy
	windowEnd := oy + ly

	if selectedY < windowStart {
		oy = utils.Max(oy-(windowStart-selectedY), 0)
	} else if selectedY > windowEnd {
		oy += (selectedY - windowEnd)
	}

	if windowEnd > lineCount-1 {
		shiftAmount := (windowEnd - (lineCount - 1))
		oy = utils.Max(oy-shiftAmount, 0)
	}

	if originalOy != oy {
		_ = v.SetOrigin(ox, oy)
	}

	cy = selectedY - oy
	if originalCy != cy {
		_ = v.SetCursor(cx, selectedY-oy)
	}
}

func (gui *Gui) FocusY(selectedY int, lineCount int, v *gocui.View) {
	gui.focusPoint(0, selectedY, lineCount, v)
}

func (gui *Gui) ResetOrigin(v *gocui.View) {
	_ = v.SetOrigin(0, 0)
	_ = v.SetCursor(0, 0)
}

func (gui *Gui) cleanString(s string) string {
	output := string(bom.Clean([]byte(s)))
	return utils.NormalizeLinefeeds(output)
}

func (gui *Gui) setViewContent(v *gocui.View, s string) error {
	v.Clear()
	fmt.Fprint(v, gui.cleanString(s))
	return nil
}

// renderString resets the origin of a view and sets its content
func (gui *Gui) renderString(g *gocui.Gui, viewName, s string) error {
	g.Update(func(*gocui.Gui) error {
		v, err := g.View(viewName)
		if err != nil {
			return nil // return gracefully if view has been deleted
		}
		if err := v.SetOrigin(0, 0); err != nil {
			return err
		}
		if err := v.SetCursor(0, 0); err != nil {
			return err
		}
		return gui.setViewContent(v, s)
	})
	return nil
}

func (gui *Gui) RenderStringMain(s string) {
	_ = gui.renderString(gui.g, "main", s)
}

// reRenderString sets the main view's content, without changing its origin
func (gui *Gui) reRenderStringMain(s string) {
	gui.reRenderString("main", s)
}

// reRenderString sets the view's content, without changing its origin
func (gui *Gui) reRenderString(viewName, s string) {
	gui.g.Update(func(*gocui.Gui) error {
		v, err := gui.g.View(viewName)
		if err != nil {
			return nil // return gracefully if view has been deleted
		}
		return gui.setViewContent(v, s)
	})
}

func (gui *Gui) optionsMapToString(optionsMap map[string]string) string {
	optionsArray := make([]string, 0)
	for key, description := range optionsMap {
		optionsArray = append(optionsArray, key+": "+description)
	}
	sort.Strings(optionsArray)
	return strings.Join(optionsArray, ", ")
}

func (gui *Gui) renderOptionsMap(optionsMap map[string]string) error {
	return gui.renderString(gui.g, "options", gui.optionsMapToString(optionsMap))
}

func (gui *Gui) GetMainView() *gocui.View {
	return gui.Views.Main
}

func (gui *Gui) trimmedContent(v *gocui.View) string {
	return strings.TrimSpace(v.Buffer())
}

func (gui *Gui) currentViewName() string {
	currentView := gui.g.CurrentView()
	// this can happen when the app is first starting up
	if currentView == nil {
		return gui.initiallyFocusedViewName()
	}
	return currentView.Name()
}

func (gui *Gui) resizeCurrentPopupPanel(g *gocui.Gui) error {
	v := g.CurrentView()
	if gui.isPopupPanel(v.Name()) {
		return gui.resizePopupPanel(v)
	}
	return nil
}

func (gui *Gui) resizePopupPanel(v *gocui.View) error {
	// If the confirmation panel is already displayed, just resize the width,
	// otherwise continue
	content := v.Buffer()
	x0, y0, x1, y1 := gui.getConfirmationPanelDimensions(v.Wrap, content)
	vx0, vy0, vx1, vy1 := v.Dimensions()
	if vx0 == x0 && vy0 == y0 && vx1 == x1 && vy1 == y1 {
		return nil
	}
	_, err := gui.g.SetView(v.Name(), x0, y0, x1, y1, 0)
	return err
}

func (gui *Gui) renderPanelOptions() error {
	currentView := gui.g.CurrentView()
	switch currentView.Name() {
	case "menu":
		return gui.renderMenuOptions()
	case "confirmation":
		return gui.renderConfirmationOptions()
	}
	return gui.renderGlobalOptions()
}

func (gui *Gui) isPopupPanel(viewName string) bool {
	return lo.Contains(gui.popupViewNames(), viewName)
}

func (gui *Gui) popupPanelFocused() bool {
	return gui.isPopupPanel(gui.currentViewName())
}

func (gui *Gui) clearMainView() {
	mainView := gui.Views.Main
	mainView.Clear()
	_ = mainView.SetOrigin(0, 0)
	_ = mainView.SetCursor(0, 0)
}

func (gui *Gui) HandleClick(v *gocui.View, itemCount int, selectedLine *int, handleSelect func() error) error {
	wrappedHandleSelect := func(g *gocui.Gui, v *gocui.View) error {
		return handleSelect()
	}
	return gui.handleClickAux(v, itemCount, selectedLine, wrappedHandleSelect)
}

func (gui *Gui) handleClickAux(v *gocui.View, itemCount int, selectedLine *int, handleSelect func(*gocui.Gui, *gocui.View) error) error {
	if gui.popupPanelFocused() && v != nil && !gui.isPopupPanel(v.Name()) {
		return nil
	}

	_, cy := v.Cursor()
	_, oy := v.Origin()

	newSelectedLine := cy + oy

	if newSelectedLine < 0 {
		newSelectedLine = 0
	}

	if newSelectedLine > itemCount-1 {
		newSelectedLine = itemCount - 1
	}

	*selectedLine = newSelectedLine

	if gui.currentViewName() != v.Name() {
		if err := gui.switchFocus(v); err != nil {
			return err
		}
	}

	return handleSelect(gui.g, v)
}

func (gui *Gui) nextScreenMode() error {
	if gui.currentViewName() == "main" {
		gui.State.ScreenMode = prevIntInCycle([]WindowMaximisation{SCREEN_NORMAL, SCREEN_HALF, SCREEN_FULL}, gui.State.ScreenMode)

		return nil
	}

	gui.State.ScreenMode = nextIntInCycle([]WindowMaximisation{SCREEN_NORMAL, SCREEN_HALF, SCREEN_FULL}, gui.State.ScreenMode)

	return nil
}

func (gui *Gui) prevScreenMode() error {
	if gui.currentViewName() == "main" {
		gui.State.ScreenMode = nextIntInCycle([]WindowMaximisation{SCREEN_NORMAL, SCREEN_HALF, SCREEN_FULL}, gui.State.ScreenMode)

		return nil
	}

	gui.State.ScreenMode = prevIntInCycle([]WindowMaximisation{SCREEN_NORMAL, SCREEN_HALF, SCREEN_FULL}, gui.State.ScreenMode)

	return nil
}

const (
	// the bounds the side panel width adjustments are clamped to
	minSidePanelWidth = 0.1
	maxSidePanelWidth = 0.9
	// how much each adjustment of the side panel width changes the width by
	sidePanelWidthDelta = 0.05
	// how long a toast message stays on screen
	toastDuration = 2 * time.Second
)

func (gui *Gui) decreaseSidePanelWidth() error {
	return gui.adjustSidePanelWidth(-sidePanelWidthDelta)
}

func (gui *Gui) increaseSidePanelWidth() error {
	return gui.adjustSidePanelWidth(sidePanelWidthDelta)
}

func (gui *Gui) adjustSidePanelWidth(delta float64) error {
	if gui.State.ScreenMode == SCREEN_FULL {
		// the focused panel takes up the whole screen so there's no width to adjust
		return nil
	}

	current := gui.State.SidePanelWidth
	if gui.State.ScreenMode == SCREEN_HALF {
		current = gui.State.HalfScreenModeSidePanelWidth
	}

	// Round away the floating point error of adding the delta, which would
	// otherwise accumulate with every step. Four decimals keep the default
	// width of 0.3333 intact.
	width := math.Round((current+delta)*10000) / 10000
	if width < minSidePanelWidth {
		width = minSidePanelWidth
	}
	if width > maxSidePanelWidth {
		width = maxSidePanelWidth
	}

	if gui.State.ScreenMode == SCREEN_HALF {
		gui.State.HalfScreenModeSidePanelWidth = width
		gui.persistSidePanelWidth(config.MachineStateKeyHalfScreenModeSidePanelWidth, width)
	} else {
		gui.State.SidePanelWidth = width
		gui.persistSidePanelWidth(config.MachineStateKeySidePanelWidth, width)
	}

	gui.showToast(utils.ResolvePlaceholderString(
		gui.Tr.SidePanelWidthChanged,
		map[string]string{"width": strconv.FormatFloat(width, 'f', 2, 64)},
	))

	return nil
}

func (gui *Gui) resetSidePanelWidth() error {
	if gui.State.ScreenMode == SCREEN_FULL {
		return nil
	}

	if gui.State.ScreenMode == SCREEN_HALF {
		gui.State.HalfScreenModeSidePanelWidth = gui.Config.UserConfig.Gui.HalfScreenModeSidePanelWidth
		gui.clearPersistedSidePanelWidth(config.MachineStateKeyHalfScreenModeSidePanelWidth)
	} else {
		gui.State.SidePanelWidth = gui.Config.UserConfig.Gui.SidePanelWidth
		gui.clearPersistedSidePanelWidth(config.MachineStateKeySidePanelWidth)
	}

	gui.showToast(gui.Tr.SidePanelWidthReset)

	return nil
}

// persistSidePanelWidth saves the adjusted width machine-locally so that it
// survives restarting lazydocker
func (gui *Gui) persistSidePanelWidth(key string, width float64) {
	if gui.machineState == nil {
		return
	}

	if err := gui.machineState.SetFloat(key, width); err != nil {
		gui.Log.Warnf("could not save %s to machine state: %v", key, err)
	}
}

// clearPersistedSidePanelWidth removes the machine-local width override, so
// that the configured width applies again
func (gui *Gui) clearPersistedSidePanelWidth(key string) {
	if gui.machineState == nil {
		return
	}

	if err := gui.machineState.Delete(key); err != nil {
		gui.Log.Warnf("could not remove %s from machine state: %v", key, err)
	}
}

// closeMachineState closes the machine state database, if we have one
func (gui *Gui) closeMachineState() {
	if gui.machineState == nil {
		return
	}

	if err := gui.machineState.Close(); err != nil {
		gui.Log.Warnf("could not close machine state database: %v", err)
	}
}

// showToast shows a transient message in the app status section of the bottom
// line, taking it away again after toastDuration
func (gui *Gui) showToast(message string) {
	gui.statusManager.addToast(message)
	if err := gui.renderString(gui.g, "appStatus", message); err != nil {
		gui.Log.Warn(err)
	}

	go func() {
		time.Sleep(toastDuration)
		gui.statusManager.removeStatus(message)
		if err := gui.renderString(gui.g, "appStatus", gui.statusManager.getStatusString()); err != nil {
			gui.Log.Warn(err)
		}
	}()
}

func nextIntInCycle(sl []WindowMaximisation, current WindowMaximisation) WindowMaximisation {
	for i, val := range sl {
		if val == current {
			if i == len(sl)-1 {
				return sl[0]
			}
			return sl[i+1]
		}
	}
	return sl[0]
}

func prevIntInCycle(sl []WindowMaximisation, current WindowMaximisation) WindowMaximisation {
	for i, val := range sl {
		if val == current {
			if i > 0 {
				return sl[i-1]
			}
			return sl[len(sl)-1]
		}
	}
	return sl[len(sl)-1]
}

func (gui *Gui) CurrentView() *gocui.View {
	return gui.g.CurrentView()
}

func (gui *Gui) currentSidePanel() (panels.ISideListPanel, bool) {
	viewName := gui.currentViewName()

	for _, sidePanel := range gui.allSidePanels() {
		if sidePanel.GetView().Name() == viewName {
			return sidePanel, true
		}
	}

	return nil, false
}

// returns the current list panel. If no list panel is focused, returns false.
func (gui *Gui) currentListPanel() (panels.ISideListPanel, bool) {
	viewName := gui.currentViewName()

	for _, sidePanel := range gui.allListPanels() {
		if sidePanel.GetView().Name() == viewName {
			return sidePanel, true
		}
	}

	return nil, false
}

func (gui *Gui) allSidePanels() []panels.ISideListPanel {
	sidePanels := []panels.ISideListPanel{
		gui.Panels.Projects,
		gui.Panels.Services,
		gui.Panels.Containers,
		gui.Panels.Images,
		gui.Panels.Volumes,
		gui.Panels.Networks,
	}
	if gui.Panels.Registries != nil {
		sidePanels = append(sidePanels, gui.Panels.Registries)
	}
	return sidePanels
}

func (gui *Gui) allListPanels() []panels.ISideListPanel {
	return append(gui.allSidePanels(), gui.Panels.Menu)
}

func (gui *Gui) IsCurrentView(view *gocui.View) bool {
	return view == gui.CurrentView()
}
