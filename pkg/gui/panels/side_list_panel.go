package panels

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/go-errors/errors"
	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazydocker/pkg/tasks"
	"github.com/jesseduffield/lazydocker/pkg/utils"
	"github.com/samber/lo"
)

const (
	selectedRowMarker   = "[x]"
	unselectedRowMarker = "[ ]"
)

type ISideListPanel interface {
	SetMainTabIndex(int)
	HandleSelect() error
	GetView() *gocui.View
	Refocus()
	RerenderList() error
	IsFilterDisabled() bool
	IsHidden() bool
	HandleNextLine() error
	HandlePrevLine() error
	HandleClick() error
	HandlePrevMainTab() error
	HandleNextMainTab() error
	ToggleSelected() error
	StartRangeSelect() error
	ClearSelection() error
	HasSelection() bool
	SelectedCount() int
	IsMultiSelect() bool
	IsRangeSelectActive() bool
}

// list panel at the side of the screen that renders content to the main panel
type SideListPanel[T comparable] struct {
	ContextState *ContextState[T]

	ListPanel[T]

	// message to render in the main view if there are no items in the panel
	// and it has focus. Leave empty if you don't want to render anything
	NoItemsMessage string

	// a representation of the gui
	Gui IGui

	// this Filter is applied on top of additional default filters
	Filter func(T) bool
	Sort   func(a, b T) bool

	// a callback to invoke when the item is clicked
	OnClick func(T) error

	// a callback to invoke when a new item is selected (e.g. keyboard navigation)
	OnSelect func(T) error

	// returns the cells that we render to the view in a table format. The cells will
	// be rendered with padding.
	GetTableCells func(T) []string

	// function to be called after re-rendering list. Can be nil
	OnRerender func() error

	// set this to true if you don't want to allow manual filtering via '/'
	DisableFilter bool

	// This can be nil if you want to always show the panel
	Hide func() bool

	// MultiSelect enables opt-in multi-selection (space to toggle, v for range
	// select, Esc / SetItems to clear). When false (the default), the panel
	// behaves as a single-selection list with no marker column or title suffix.
	MultiSelect bool

	// GetItemKey returns a stable identity string for an item so that selection
	// survives filtering, sorting, and re-rendering. If nil, falls back to
	// ContextState.GetItemContextCacheKey (when available) and then fmt.Sprint.
	GetItemKey func(T) string

	selectedKeys         map[string]bool
	preRangeSelectedKeys map[string]bool
	rangeSelectActive    bool
	rangeAnchorIdx       int
	lastAppliedRangeIdx  int
	lastFilterString     string
}

var _ ISideListPanel = &SideListPanel[int]{}

type IGui interface {
	HandleClick(v *gocui.View, itemCount int, selectedLine *int, handleSelect func() error) error
	NewSimpleRenderStringTask(getContent func() string) tasks.TaskFunc
	FocusY(selectedLine int, itemCount int, view *gocui.View)
	ShouldRefresh(contextKey string) bool
	GetMainView() *gocui.View
	IsCurrentView(*gocui.View) bool
	FilterString(view *gocui.View) string
	IgnoreStrings() []string
	Update(func() error)

	QueueTask(f func(ctx context.Context)) error
}

func (self *SideListPanel[T]) HandleClick() error {
	itemCount := self.List.Len()
	handleSelect := self.HandleSelect
	selectedLine := &self.SelectedIdx

	if self.Gui != nil {
		if err := self.Gui.HandleClick(self.View, itemCount, selectedLine, handleSelect); err != nil {
			return err
		}
	}

	if self.MultiSelect && self.rangeSelectActive {
		self.syncRangeSelection()
		if err := self.RerenderList(); err != nil {
			return err
		}
	}

	if self.OnClick != nil {
		selectedItem, err := self.GetSelectedItem()
		if err == nil {
			return self.OnClick(selectedItem)
		}
	}

	return nil
}

func (self *SideListPanel[T]) GetView() *gocui.View {
	return self.View
}

func (self *SideListPanel[T]) HandleSelect() error {
	item, err := self.GetSelectedItem()
	if err != nil {
		if err.Error() != self.NoItemsMessage {
			return err
		}

		if self.NoItemsMessage != "" && self.Gui != nil {
			self.Gui.NewSimpleRenderStringTask(func() string { return self.NoItemsMessage })
		}

		return nil
	}

	self.Refocus()

	if self.OnSelect != nil {
		if err := self.OnSelect(item); err != nil {
			return err
		}
	}

	return self.renderContext(item)
}

func (self *SideListPanel[T]) renderContext(item T) error {
	if self.ContextState == nil || self.Gui == nil {
		return nil
	}
	if self.ContextState.GetMainTabs == nil || len(self.ContextState.GetMainTabs()) == 0 {
		return nil
	}

	key := self.ContextState.GetCurrentContextKey(item)
	if !self.Gui.ShouldRefresh(key) {
		return nil
	}

	mainView := self.Gui.GetMainView()
	mainView.Tabs = self.ContextState.GetMainTabTitles()
	mainView.TabIndex = self.ContextState.mainTabIdx

	task := self.ContextState.GetCurrentMainTab().Render(item)

	return self.Gui.QueueTask(task)
}

func (self *SideListPanel[T]) GetSelectedItem() (T, error) {
	var zero T

	if self.List == nil {
		return zero, errors.New(self.NoItemsMessage)
	}

	item, ok := self.List.TryGet(self.SelectedIdx)
	if !ok {
		// could probably have a better error here
		return zero, errors.New(self.NoItemsMessage)
	}

	return item, nil
}

func (self *SideListPanel[T]) SetSelectedLineIdx(value int) {
	self.ListPanel.SetSelectedLineIdx(value)
	if self.MultiSelect && self.rangeSelectActive {
		self.syncRangeSelection()
	}
}

func (self *SideListPanel[T]) SelectNextLine() {
	self.ListPanel.SelectNextLine()
	if self.MultiSelect && self.rangeSelectActive {
		self.syncRangeSelection()
	}
}

func (self *SideListPanel[T]) SelectPrevLine() {
	self.ListPanel.SelectPrevLine()
	if self.MultiSelect && self.rangeSelectActive {
		self.syncRangeSelection()
	}
}

func (self *SideListPanel[T]) HandleNextLine() error {
	self.SelectNextLine()
	if self.MultiSelect && self.rangeSelectActive {
		if err := self.RerenderList(); err != nil {
			return err
		}
	}

	return self.HandleSelect()
}

func (self *SideListPanel[T]) HandlePrevLine() error {
	self.SelectPrevLine()
	if self.MultiSelect && self.rangeSelectActive {
		if err := self.RerenderList(); err != nil {
			return err
		}
	}

	return self.HandleSelect()
}

func (self *SideListPanel[T]) HandleNextMainTab() error {
	if self.ContextState == nil {
		return nil
	}

	self.ContextState.HandleNextMainTab()

	return self.HandleSelect()
}

func (self *SideListPanel[T]) HandlePrevMainTab() error {
	if self.ContextState == nil {
		return nil
	}

	self.ContextState.HandlePrevMainTab()

	return self.HandleSelect()
}

func (self *SideListPanel[T]) Refocus() {
	if self.Gui == nil {
		return
	}
	self.Gui.FocusY(self.SelectedIdx, self.List.Len(), self.View)
}

func (self *SideListPanel[T]) SetItems(items []T) {
	hadSelection := len(self.selectedKeys) > 0 || self.rangeSelectActive
	self.resetSelectionState()
	if self.MultiSelect {
		self.updateTitle()
	} else if hadSelection && self.View != nil {
		self.View.Title = stripSelectedCountSuffix(self.View.Title)
	}
	self.List.SetItems(items)
	self.FilterAndSort()
}

func (self *SideListPanel[T]) tableCells(item T) []string {
	if self.GetTableCells == nil {
		return []string{fmt.Sprint(item)}
	}
	return self.GetTableCells(item)
}

func (self *SideListPanel[T]) getTableCellsForRender(item T) []string {
	cells := self.tableCells(item)
	if !self.MultiSelect {
		return cells
	}
	marker := unselectedRowMarker
	if self.IsSelected(item) {
		marker = selectedRowMarker
	}
	return append([]string{marker}, cells...)
}

func (self *SideListPanel[T]) FilterAndSort() {
	var filterString string
	var ignoreStrings []string
	if self.Gui != nil {
		filterString = self.Gui.FilterString(self.View)
		ignoreStrings = self.Gui.IgnoreStrings()
	}

	if self.MultiSelect && filterString != self.lastFilterString {
		if self.rangeSelectActive {
			self.syncRangeSelection()
			self.rangeSelectActive = false
			self.preRangeSelectedKeys = nil
			if self.View != nil {
				self.View.CancelRangeSelect()
			}
		}
		self.lastFilterString = filterString
	}

	self.List.Filter(func(item T, index int) bool {
		if self.Filter != nil && !self.Filter(item) {
			return false
		}

		cells := self.tableCells(item)
		if lo.SomeBy(ignoreStrings, func(ignore string) bool {
			return lo.SomeBy(cells, func(searchString string) bool {
				return strings.Contains(searchString, ignore)
			})
		}) {
			return false
		}

		if filterString != "" {
			return lo.SomeBy(cells, func(searchString string) bool {
				return strings.Contains(searchString, filterString)
			})
		}

		return true
	})

	self.List.Sort(self.Sort)

	self.clampSelectedLineIdx()
}

func (self *SideListPanel[T]) RerenderList() error {
	if self.MultiSelect && self.rangeSelectActive {
		self.syncRangeSelection()
	}

	self.FilterAndSort()

	if self.MultiSelect {
		self.updateTitle()
	}

	if self.Gui == nil {
		return nil
	}

	self.Gui.Update(func() error {
		if self.MultiSelect {
			self.updateTitle()
		}
		if self.View != nil {
			self.View.Clear()
			table := lo.Map(self.List.GetItems(), func(item T, index int) []string {
				return self.getTableCellsForRender(item)
			})
			renderedTable, err := utils.RenderTable(table)
			if err != nil {
				return err
			}
			fmt.Fprint(self.View, renderedTable)
		}

		if self.OnRerender != nil {
			if err := self.OnRerender(); err != nil {
				return err
			}
		}

		if self.Gui.IsCurrentView(self.View) {
			return self.HandleSelect()
		}
		return nil
	})

	return nil
}

func (self *SideListPanel[T]) SetMainTabIndex(index int) {
	if self.ContextState == nil {
		return
	}

	self.ContextState.SetMainTabIndex(index)
}

func (self *SideListPanel[T]) IsFilterDisabled() bool {
	return self.DisableFilter
}

func (self *SideListPanel[T]) IsHidden() bool {
	if self.Hide == nil {
		return false
	}

	return self.Hide()
}

func (self *SideListPanel[T]) IsMultiSelect() bool {
	return self.MultiSelect
}

func (self *SideListPanel[T]) IsRangeSelectActive() bool {
	return self.MultiSelect && self.rangeSelectActive
}

func (self *SideListPanel[T]) itemKey(item T) string {
	if self.GetItemKey != nil {
		return self.GetItemKey(item)
	}
	if self.ContextState != nil && self.ContextState.GetItemContextCacheKey != nil {
		return self.ContextState.GetItemContextCacheKey(item)
	}
	return fmt.Sprint(item)
}

func (self *SideListPanel[T]) IsSelected(item T) bool {
	if !self.MultiSelect {
		return false
	}
	if self.rangeSelectActive {
		self.syncRangeSelection()
	}
	if len(self.selectedKeys) == 0 {
		return false
	}
	return self.selectedKeys[self.itemKey(item)]
}

func (self *SideListPanel[T]) ToggleSelected() error {
	if !self.MultiSelect {
		return nil
	}
	if self.rangeSelectActive {
		self.syncRangeSelection()
		movedDuringRange := self.SelectedIdx != self.rangeAnchorIdx
		self.rangeSelectActive = false
		self.preRangeSelectedKeys = nil
		if self.View != nil {
			self.View.CancelRangeSelect()
		}
		if movedDuringRange {
			self.updateTitle()
			return self.RerenderList()
		}
	}

	item, err := self.GetSelectedItem()
	if err != nil {
		return nil
	}

	if self.selectedKeys == nil {
		self.selectedKeys = make(map[string]bool)
	}
	key := self.itemKey(item)
	if self.selectedKeys[key] {
		delete(self.selectedKeys, key)
	} else {
		self.selectedKeys[key] = true
	}

	self.updateTitle()
	return self.RerenderList()
}

func (self *SideListPanel[T]) StartRangeSelect() error {
	if !self.MultiSelect {
		return nil
	}
	if self.List == nil || self.List.Len() == 0 {
		return nil
	}
	if self.rangeSelectActive {
		self.syncRangeSelection()
		self.rangeSelectActive = false
		self.preRangeSelectedKeys = nil
		if self.View != nil {
			self.View.CancelRangeSelect()
		}
		self.updateTitle()
		return self.RerenderList()
	}

	self.clampSelectedLineIdx()
	self.rangeSelectActive = true
	self.rangeAnchorIdx = self.SelectedIdx
	self.lastAppliedRangeIdx = -1
	self.preRangeSelectedKeys = copyKeyMap(self.selectedKeys)
	if self.View != nil {
		self.View.SetRangeSelectStart(self.rangeAnchorIdx)
	}
	self.syncRangeSelection()
	return self.RerenderList()
}

func (self *SideListPanel[T]) ClearSelection() error {
	hadSelection := len(self.selectedKeys) > 0 || self.rangeSelectActive
	self.resetSelectionState()
	if !self.MultiSelect {
		if hadSelection && self.View != nil {
			self.View.Title = stripSelectedCountSuffix(self.View.Title)
		}
		return nil
	}
	self.updateTitle()
	return self.RerenderList()
}

func (self *SideListPanel[T]) SelectedCount() int {
	if !self.MultiSelect {
		return 0
	}
	return len(self.selectedItemsNoFallback())
}

func (self *SideListPanel[T]) HasSelection() bool {
	return self.MultiSelect && (self.SelectedCount() > 0 || self.rangeSelectActive)
}

// SelectedItems returns the multi-selected items in panel order when MultiSelect
// is enabled and at least one item is selected; otherwise it falls back to a
// single-element slice containing the current cursor item (or nil if empty).
func (self *SideListPanel[T]) SelectedItems() []T {
	if self.MultiSelect {
		if selected := self.selectedItemsNoFallback(); len(selected) > 0 {
			return selected
		}
	}

	item, err := self.GetSelectedItem()
	if err != nil {
		return nil
	}
	return []T{item}
}

func (self *SideListPanel[T]) selectedItemsNoFallback() []T {
	if !self.MultiSelect {
		return nil
	}
	if self.rangeSelectActive {
		self.syncRangeSelection()
	}
	if len(self.selectedKeys) == 0 || self.List == nil {
		return nil
	}

	allItems := self.List.GetAllItems()
	seen := make(map[string]bool, len(self.selectedKeys))
	var selected []T
	for _, item := range allItems {
		if self.Filter != nil && !self.Filter(item) {
			continue
		}
		key := self.itemKey(item)
		if self.selectedKeys[key] && !seen[key] {
			seen[key] = true
			selected = append(selected, item)
		}
	}
	if self.Sort != nil && len(selected) > 1 {
		sort.Slice(selected, func(i, j int) bool {
			return self.Sort(selected[i], selected[j])
		})
	}
	return selected
}

func (self *SideListPanel[T]) syncRangeSelection() {
	if !self.MultiSelect || !self.rangeSelectActive {
		return
	}
	if self.List == nil || self.List.Len() == 0 {
		return
	}
	if self.SelectedIdx == self.lastAppliedRangeIdx {
		return
	}

	self.lastAppliedRangeIdx = self.SelectedIdx
	next := copyKeyMap(self.preRangeSelectedKeys)
	if next == nil {
		next = make(map[string]bool)
	}

	start := self.rangeAnchorIdx
	end := self.SelectedIdx
	if start > end {
		start, end = end, start
	}
	if start < 0 {
		start = 0
	}
	if maxIdx := self.List.Len() - 1; end > maxIdx {
		end = maxIdx
	}

	for i := start; i <= end; i++ {
		if item, ok := self.List.TryGet(i); ok {
			next[self.itemKey(item)] = true
		}
	}
	self.selectedKeys = next
	self.updateTitle()
}

func (self *SideListPanel[T]) resetSelectionState() {
	self.selectedKeys = nil
	self.preRangeSelectedKeys = nil
	self.rangeSelectActive = false
	self.rangeAnchorIdx = -1
	self.lastAppliedRangeIdx = -1
	if self.View != nil {
		self.View.CancelRangeSelect()
	}
}

func (self *SideListPanel[T]) updateTitle() {
	if !self.MultiSelect || self.View == nil {
		return
	}
	base := stripSelectedCountSuffix(self.View.Title)
	count := len(self.selectedItemsNoFallback())
	if count == 0 {
		self.View.Title = base
		return
	}
	if base == "" {
		self.View.Title = fmt.Sprintf("(%d selected)", count)
		return
	}
	self.View.Title = fmt.Sprintf("%s (%d selected)", base, count)
}

func stripSelectedCountSuffix(title string) string {
	const suffix = " selected)"
	if !strings.HasSuffix(title, suffix) {
		return title
	}
	withoutClose := strings.TrimSuffix(title, suffix)
	openIdx := strings.LastIndex(withoutClose, "(")
	if openIdx < 0 {
		return title
	}
	digits := withoutClose[openIdx+1:]
	if len(digits) == 0 {
		return title
	}
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return title
		}
	}
	if openIdx > 0 && withoutClose[openIdx-1] == ' ' {
		return withoutClose[:openIdx-1]
	}
	if openIdx == 0 {
		return ""
	}
	return title
}

func copyKeyMap(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		if v {
			dst[k] = true
		}
	}
	return dst
}
