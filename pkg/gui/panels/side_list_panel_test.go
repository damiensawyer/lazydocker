package panels

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazydocker/pkg/tasks"
	"github.com/stretchr/testify/assert"
)

type testItem struct {
	id   string
	name string
}

type fakeGui struct {
	g             *gocui.Gui
	mainView      *gocui.View
	currentView   *gocui.View
	filterNeedle  string
	ignoreStrings []string
}

func newFakeGui(t *testing.T) (*fakeGui, *gocui.View) {
	t.Helper()

	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("creating headless gui: %v", err)
	}
	t.Cleanup(g.Close)

	listView, err := g.SetView("list", 0, 0, 40, 10, 0)
	if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		t.Fatalf("creating list view: %v", err)
	}
	mainView, err := g.SetView("main", 41, 0, 79, 23, 0)
	if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		t.Fatalf("creating main view: %v", err)
	}

	fg := &fakeGui{
		g:           g,
		mainView:    mainView,
		currentView: listView,
	}
	return fg, listView
}

func (f *fakeGui) HandleClick(v *gocui.View, itemCount int, selectedLine *int, handleSelect func() error) error {
	return handleSelect()
}

func (f *fakeGui) NewSimpleRenderStringTask(getContent func() string) tasks.TaskFunc {
	return func(ctx context.Context) {}
}

func (f *fakeGui) FocusY(selectedLine int, itemCount int, view *gocui.View) {}

func (f *fakeGui) ShouldRefresh(contextKey string) bool {
	return false
}

func (f *fakeGui) GetMainView() *gocui.View {
	return f.mainView
}

func (f *fakeGui) IsCurrentView(v *gocui.View) bool {
	return v == f.currentView
}

func (f *fakeGui) FilterString(view *gocui.View) string {
	return f.filterNeedle
}

func (f *fakeGui) IgnoreStrings() []string {
	return f.ignoreStrings
}

func (f *fakeGui) Update(fn func() error) {
	_ = fn()
}

func (f *fakeGui) QueueTask(fn func(ctx context.Context)) error {
	return nil
}

func newTestPanel(t *testing.T, multiSelect bool, items []*testItem) (*SideListPanel[*testItem], *fakeGui, *gocui.View) {
	t.Helper()

	fg, view := newFakeGui(t)
	view.Title = "Tags"

	panel := &SideListPanel[*testItem]{
		ListPanel: ListPanel[*testItem]{
			List: NewFilteredList[*testItem](),
			View: view,
		},
		Gui:         fg,
		MultiSelect: multiSelect,
		GetItemKey: func(item *testItem) string {
			return item.id
		},
		GetTableCells: func(item *testItem) []string {
			return []string{item.name}
		},
	}

	panel.SetItems(items)
	assert.NoError(t, panel.RerenderList())
	return panel, fg, view
}

func TestSideListPanel_MultiSelectFalse_LeavesBehaviourUnchanged(t *testing.T) {
	items := []*testItem{
		{id: "1", name: "alpha"},
		{id: "2", name: "beta"},
		{id: "3", name: "gamma"},
	}

	panel, _, view := newTestPanel(t, false, items)
	view.Title = "Images"
	assert.NoError(t, panel.RerenderList())

	assert.False(t, panel.IsMultiSelect())
	assert.False(t, panel.HasSelection())
	assert.Equal(t, 0, panel.SelectedCount())
	assert.Equal(t, "Images", view.Title)

	// Rendered output has no marker column when MultiSelect is false.
	assert.Equal(t, "alpha\nbeta\ngamma", strings.TrimRight(view.Buffer(), "\n"))

	// Toggling and range-selecting are no-ops when MultiSelect is false.
	assert.NoError(t, panel.ToggleSelected())
	assert.NoError(t, panel.StartRangeSelect())
	assert.NoError(t, panel.HandleNextLine())
	assert.NoError(t, panel.ClearSelection())

	assert.False(t, panel.HasSelection())
	assert.Equal(t, 0, panel.SelectedCount())
	assert.False(t, panel.IsRangeSelectActive())
	assert.Equal(t, "Images", view.Title)
	assert.Equal(t, "alpha\nbeta\ngamma", strings.TrimRight(view.Buffer(), "\n"))

	// SelectedItems falls back to the current cursor item (index 1 after HandleNextLine).
	assert.Equal(t, []*testItem{items[1]}, panel.SelectedItems())
}

func TestSideListPanel_ToggleSelection_TitleCountAndMarkerRendering(t *testing.T) {
	items := []*testItem{
		{id: "1", name: "v1.0.0"},
		{id: "2", name: "v1.1.0"},
		{id: "3", name: "v2.0.0"},
	}

	panel, _, view := newTestPanel(t, true, items)

	assert.Equal(t, "Tags", view.Title)
	assert.Equal(t, 0, panel.SelectedCount())
	assert.False(t, panel.HasSelection())
	assert.Equal(t, "[ ] v1.0.0\n[ ] v1.1.0\n[ ] v2.0.0", strings.TrimRight(view.Buffer(), "\n"))

	// Toggle first item on.
	assert.NoError(t, panel.ToggleSelected())
	assert.True(t, panel.HasSelection())
	assert.Equal(t, 1, panel.SelectedCount())
	assert.True(t, panel.IsSelected(items[0]))
	assert.False(t, panel.IsSelected(items[1]))
	assert.Equal(t, "Tags (1 selected)", view.Title)
	assert.Equal(t, []*testItem{items[0]}, panel.SelectedItems())
	assert.Equal(t, "[x] v1.0.0\n[ ] v1.1.0\n[ ] v2.0.0", strings.TrimRight(view.Buffer(), "\n"))

	// Move to third item and toggle on.
	assert.NoError(t, panel.HandleNextLine())
	assert.NoError(t, panel.HandleNextLine())
	assert.NoError(t, panel.ToggleSelected())
	assert.Equal(t, 2, panel.SelectedCount())
	assert.Equal(t, "Tags (2 selected)", view.Title)
	assert.Equal(t, []*testItem{items[0], items[2]}, panel.SelectedItems())
	assert.Equal(t, "[x] v1.0.0\n[ ] v1.1.0\n[x] v2.0.0", strings.TrimRight(view.Buffer(), "\n"))

	// Toggle third item back off.
	assert.NoError(t, panel.ToggleSelected())
	assert.Equal(t, 1, panel.SelectedCount())
	assert.Equal(t, "Tags (1 selected)", view.Title)
	assert.Equal(t, []*testItem{items[0]}, panel.SelectedItems())

	// Move back to first item and toggle off -> title returns to base title.
	assert.NoError(t, panel.HandlePrevLine())
	assert.NoError(t, panel.HandlePrevLine())
	assert.NoError(t, panel.ToggleSelected())
	assert.Equal(t, 0, panel.SelectedCount())
	assert.False(t, panel.HasSelection())
	assert.Equal(t, "Tags", view.Title)
	assert.Equal(t, "[ ] v1.0.0\n[ ] v1.1.0\n[ ] v2.0.0", strings.TrimRight(view.Buffer(), "\n"))
}

func TestSideListPanel_RangeSelect(t *testing.T) {
	items := []*testItem{
		{id: "a", name: "tag-a"},
		{id: "b", name: "tag-b"},
		{id: "c", name: "tag-c"},
		{id: "d", name: "tag-d"},
		{id: "e", name: "tag-e"},
	}

	panel, _, view := newTestPanel(t, true, items)

	// Start range select at index 1 ("tag-b") and move down to index 3 ("tag-d").
	assert.NoError(t, panel.HandleNextLine())
	assert.NoError(t, panel.StartRangeSelect())
	assert.True(t, panel.IsRangeSelectActive())
	assert.Equal(t, []*testItem{items[1]}, panel.SelectedItems())
	assert.Equal(t, "Tags (1 selected)", view.Title)

	assert.NoError(t, panel.HandleNextLine())
	assert.NoError(t, panel.HandleNextLine())
	assert.Equal(t, []*testItem{items[1], items[2], items[3]}, panel.SelectedItems())
	assert.Equal(t, 3, panel.SelectedCount())
	assert.Equal(t, "Tags (3 selected)", view.Title)
	assert.Equal(t, "[ ] tag-a\n[x] tag-b\n[x] tag-c\n[x] tag-d\n[ ] tag-e", strings.TrimRight(view.Buffer(), "\n"))

	// Moving back up while range select is active shrinks the range.
	assert.NoError(t, panel.HandlePrevLine())
	assert.Equal(t, []*testItem{items[1], items[2]}, panel.SelectedItems())
	assert.Equal(t, 2, panel.SelectedCount())
	assert.Equal(t, "Tags (2 selected)", view.Title)

	// Calling StartRangeSelect again ends range-select mode and keeps the selection.
	assert.NoError(t, panel.StartRangeSelect())
	assert.False(t, panel.IsRangeSelectActive())
	assert.NoError(t, panel.HandleNextLine())
	assert.NoError(t, panel.HandleNextLine())
	assert.Equal(t, []*testItem{items[1], items[2]}, panel.SelectedItems())

	// Clear and test upward range select (anchor at index 4, move up to index 2)
	// combined with a pre-selected item at index 0.
	assert.NoError(t, panel.ClearSelection())
	panel.SetSelectedLineIdx(0)
	assert.NoError(t, panel.ToggleSelected())
	panel.SetSelectedLineIdx(4)
	assert.NoError(t, panel.StartRangeSelect())
	assert.NoError(t, panel.HandlePrevLine())
	assert.NoError(t, panel.HandlePrevLine())

	assert.Equal(t, []*testItem{items[0], items[2], items[3], items[4]}, panel.SelectedItems())
	assert.Equal(t, 4, panel.SelectedCount())
	assert.Equal(t, "Tags (4 selected)", view.Title)
}

func TestSideListPanel_FilteringAndRerenderPersistence(t *testing.T) {
	items := []*testItem{
		{id: "id-1", name: "v1.0.0"},
		{id: "id-2", name: "v1.1.0"},
		{id: "id-3", name: "v2.0.0"},
		{id: "id-4", name: "latest"},
	}

	panel, fg, view := newTestPanel(t, true, items)

	// Select "v1.0.0" (index 0) and "v2.0.0" (index 2).
	assert.NoError(t, panel.ToggleSelected())
	panel.SetSelectedLineIdx(2)
	assert.NoError(t, panel.ToggleSelected())
	assert.Equal(t, []*testItem{items[0], items[2]}, panel.SelectedItems())

	// Apply filter "v1" -> only v1.0.0 and v1.1.0 are visible.
	fg.filterNeedle = "v1"
	assert.NoError(t, panel.RerenderList())
	assert.Equal(t, 2, panel.List.Len())
	assert.Equal(t, "[x] v1.0.0\n[ ] v1.1.0", strings.TrimRight(view.Buffer(), "\n"))

	// Selection of v2.0.0 survives even while filtered out, and toggling v1.1.0
	// while filtered adds to the selection.
	assert.Equal(t, 2, panel.SelectedCount())
	panel.SetSelectedLineIdx(1) // v1.1.0 in filtered list
	assert.NoError(t, panel.ToggleSelected())
	assert.Equal(t, 3, panel.SelectedCount())
	assert.Equal(t, "Tags (3 selected)", view.Title)

	// Clear filter -> all 3 selected items are preserved and marked.
	fg.filterNeedle = ""
	assert.NoError(t, panel.RerenderList())
	assert.Equal(t, []*testItem{items[0], items[1], items[2]}, panel.SelectedItems())
	assert.Equal(t, "[x] v1.0.0\n[x] v1.1.0\n[x] v2.0.0\n[ ] latest", strings.TrimRight(view.Buffer(), "\n"))

	// Selection also survives re-render when underlying pointers are refreshed
	// with the same stable item keys (e.g. via ContextState.GetItemContextCacheKey).
	panel.GetItemKey = nil
	panel.ContextState = &ContextState[*testItem]{
		GetMainTabs: func() []MainTab[*testItem] { return nil },
		GetItemContextCacheKey: func(item *testItem) string {
			return item.id
		},
	}
	refreshed := []*testItem{
		{id: "id-1", name: "v1.0.0"},
		{id: "id-2", name: "v1.1.0"},
		{id: "id-3", name: "v2.0.0"},
		{id: "id-4", name: "latest"},
	}
	panel.List.SetItems(refreshed)
	assert.NoError(t, panel.RerenderList())
	assert.Equal(t, []*testItem{refreshed[0], refreshed[1], refreshed[2]}, panel.SelectedItems())
}

func TestSideListPanel_SelectedItemsFallbackAndClear(t *testing.T) {
	items := []*testItem{
		{id: "1", name: "one"},
		{id: "2", name: "two"},
		{id: "3", name: "three"},
	}

	panel, _, view := newTestPanel(t, true, items)

	// Fallback to cursor item when nothing is selected.
	panel.SetSelectedLineIdx(1)
	assert.False(t, panel.HasSelection())
	assert.Equal(t, []*testItem{items[1]}, panel.SelectedItems())

	// When items are selected, SelectedItems returns only the selected items, not the cursor item.
	panel.SetSelectedLineIdx(0)
	assert.NoError(t, panel.ToggleSelected())
	panel.SetSelectedLineIdx(2)
	assert.Equal(t, []*testItem{items[0]}, panel.SelectedItems())

	// ClearSelection (bound to Esc) clears selection and restores fallback to cursor item.
	assert.NoError(t, panel.ClearSelection())
	assert.False(t, panel.HasSelection())
	assert.Equal(t, 0, panel.SelectedCount())
	assert.Equal(t, "Tags", view.Title)
	assert.Equal(t, []*testItem{items[2]}, panel.SelectedItems())
	assert.Equal(t, "[ ] one\n[ ] two\n[ ] three", strings.TrimRight(view.Buffer(), "\n"))

	// Replacing the list via SetItems also clears selection and resets the title suffix.
	assert.NoError(t, panel.ToggleSelected())
	assert.Equal(t, "Tags (1 selected)", view.Title)

	replacement := []*testItem{
		{id: "4", name: "four"},
		{id: "5", name: "five"},
	}
	panel.SetItems(replacement)
	assert.NoError(t, panel.RerenderList())
	assert.False(t, panel.HasSelection())
	assert.Equal(t, 0, panel.SelectedCount())
	assert.Equal(t, "Tags", view.Title)
	assert.Equal(t, []*testItem{replacement[1]}, panel.SelectedItems())

	// Empty list returns nil for SelectedItems.
	panel.SetItems(nil)
	assert.Nil(t, panel.SelectedItems())
}
