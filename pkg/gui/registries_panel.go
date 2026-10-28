package gui

import (
	"context"
	"errors"
	"time"

	"github.com/fatih/color"
	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazydocker/pkg/gui/panels"
	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/jesseduffield/lazydocker/pkg/registry/connections"
	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
	"github.com/jesseduffield/lazydocker/pkg/tasks"
	"github.com/jesseduffield/lazydocker/pkg/utils"
	"github.com/sasha-s/go-deadlock"
)

// The Registries side panel (L3.1): registry connections discovered from the
// user's docker/podman auth config, pinged asynchronously with a status and
// response duration per row. The panel exists next to Images/Volumes/Networks
// but, per D-09, does nothing — no config read, no credential-helper exec, no
// network — until it first gets focus (onRegistriesPanelFocused, called from
// switchFocusAux).

// registriesPingTimeout bounds one registry ping, including the credential
// resolution that precedes it (a credential helper can block on a keychain
// prompt; the row must not stay "checking" forever).
const registriesPingTimeout = 20 * time.Second

// registriesDrillLevel captures one level of the registries drill stack (D-08).
// When at the root (registries level), drillStack is empty. When drilled into
// a registry, drillStack has one level storing the parent registry item,
// the path title, the previous items and cursor position, and any error from
// listing repositories.
type registriesDrillLevel struct {
	kind        registryItemKind
	title       string
	item        *registryItem
	items       []*registryItem
	selectedIdx int
	err         string
}

// registriesState is the mutable state behind the Registries panel's async
// refresh (S-03): a generation counter that invalidates in-flight work.
type registriesState struct {
	mu deadlock.Mutex
	// loaded: the first refresh has been kicked off (which happens at first
	// focus, not at startup — D-09).
	loaded bool
	// generation: bumped by every refresh; discovery sets and ping results
	// from an older generation are dropped when they land.
	generation uint64
	// store overrides connections.NewStore(gui.Config.ConfigDir) when set
	// (used by tests to inject a temporary ConfigDir and DockerConfigDir).
	store *connections.Store
	// storeLoadFn, when set, overrides reading saved connections in tests
	// (e.g. to verify D-09 lazy store reads).
	storeLoadFn func() ([]connections.Connection, error)
	// drillStack holds the breadcrumb trail when drilled down into a registry.
	drillStack []*registriesDrillLevel
	// repoSource overrides the default repository source in tests.
	repoSource repositorySource
	// updateUIFn overrides gui.g.Update in tests (e.g. headless tests without an event loop).
	updateUIFn func(func(*gocui.Gui) error)
}

func (s *registriesState) currentDrillLevel() *registriesDrillLevel {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.drillStack) == 0 {
		return nil
	}
	return s.drillStack[len(s.drillStack)-1]
}

func (s *registriesState) isDrilledDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.drillStack) > 0
}

func (s *registriesState) bumpGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	return s.generation
}

func (gui *Gui) isRegistriesDrilledDown() bool {
	return gui.registriesState.isDrilledDown()
}

// beginRefresh marks the panel loaded and opens a new generation. Synchronous
// on purpose: the caller must know its generation before any goroutine runs.
func (s *registriesState) beginRefresh() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = true
	s.generation++
	return s.generation
}

// isCurrent reports whether gen is still the newest generation.
func (s *registriesState) isCurrent(gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation == gen
}

func (gui *Gui) getRegistriesPanel() *panels.SideListPanel[*registryItem] {
	return &panels.SideListPanel[*registryItem]{
		ContextState: &panels.ContextState[*registryItem]{
			GetMainTabs: func() []panels.MainTab[*registryItem] {
				return []panels.MainTab[*registryItem]{
					{
						Key:    "summary",
						Title:  gui.Tr.RegistriesSummaryTitle,
						Render: gui.renderRegistrySummary,
					},
				}
			},
			// Status and duration are part of the cache key so a landed ping
			// re-renders the summary of the selected row (the periodic
			// checkForContextChange calls HandleSelect every second).
			GetItemContextCacheKey: func(item *registryItem) string {
				if item.Kind == registryKindRepository {
					return "repo-" + item.Host + "-" + item.Repository + "-" + item.Visibility + "-" + item.Err
				}
				return "registries-" + item.Host + "-" + item.Username + "-" + string(item.Status) + "-" + item.Duration.String() + "-" + item.Err
			},
		},
		ListPanel: panels.ListPanel[*registryItem]{
			List: panels.NewFilteredList[*registryItem](),
			View: gui.Views.Registries,
		},
		NoItemsMessage: gui.Tr.NoRegistries,
		Gui:            gui.intoInterface(),
		Sort: func(a, b *registryItem) bool {
			if a.Kind == registryKindRepository && b.Kind == registryKindRepository {
				return a.Repository < b.Repository
			}
			return a.Host < b.Host
		},
		GetTableCells: gui.getRegistriesDisplayStrings,
		GetItemKey: func(item *registryItem) string {
			if item.Kind == registryKindRepository {
				return item.Host + "/" + item.Repository
			}
			return item.Host
		},
		// `registries: { enabled: false }` hides the panel entirely (zero
		// regression: users who never open the panel see stock lazydocker).
		Hide: func() bool {
			if gui.Config == nil || gui.Config.UserConfig == nil {
				return false
			}
			return !gui.Config.UserConfig.Registries.Enabled
		},
	}
}

func (gui *Gui) renderRegistrySummary(item *registryItem) tasks.TaskFunc {
	return gui.NewSimpleRenderStringTask(func() string { return gui.registriesSummaryStr(item) })
}

// registriesSummaryStr renders the selected registry's summary (the panel's
// one main tab). Only secret-free fields: username, source file, status,
// duration, and the registry/credentials packages' own (already sanitized)
// error messages. A secret is never held here, let alone rendered.
func (gui *Gui) registriesSummaryStr(item *registryItem) string {
	if item == nil {
		return ""
	}
	if item.Kind == registryKindRepository {
		return gui.repositorySummaryStr(item)
	}
	padding := 15
	output := ""
	output += utils.WithPadding(gui.Tr.RegistriesSummaryRegistry, padding) + gui.registriesHostDisplay(item) + "\n"
	output += utils.WithPadding(gui.Tr.RegistriesSummaryUsername, padding) + item.Username + "\n"
	output += utils.WithPadding(gui.Tr.RegistriesSummarySourceFile, padding) + item.SourceFile + "\n"
	output += utils.WithPadding(gui.Tr.RegistriesSummaryStatus, padding) + utils.Decolorise(gui.registriesStatusDisplay(item.Status)) + "\n"
	if d := registriesDurationDisplay(item.Duration); d != "" {
		output += utils.WithPadding(gui.Tr.RegistriesSummaryResponseTime, padding) + d + "\n"
	}
	if item.Err != "" {
		output += "\n" + item.Err + "\n"
	}
	return output
}

func (gui *Gui) repositorySummaryStr(item *registryItem) string {
	padding := 15
	output := ""
	output += utils.WithPadding(gui.Tr.RegistriesSummaryRegistry, padding) + item.Host + "\n"
	output += utils.WithPadding(gui.Tr.RegistriesSummaryRepository, padding) + item.Repository + "\n"
	if item.Visibility != "" {
		output += utils.WithPadding(gui.Tr.RegistriesSummaryVisibility, padding) + item.Visibility + "\n"
	}
	if item.Err != "" {
		output += "\n" + item.Err + "\n"
	}
	return output
}

func (gui *Gui) getRegistriesDisplayStrings(item *registryItem) []string {
	if item.Kind == registryKindRepository {
		if item.Visibility != "" {
			return []string{
				item.Repository,
				item.Visibility,
			}
		}
		return []string{
			item.Repository,
		}
	}
	return []string{
		gui.registriesStatusDisplay(item.Status),
		gui.registriesHostDisplay(item),
		item.Username,
	}
}

// registriesHostDisplay renders the host with the auto-detected marker;
// auto-detected rows come from the user's auth config and are not editable
// (saved connections, until they land, are every row's origin).
func (gui *Gui) registriesHostDisplay(item *registryItem) string {
	if item.AutoDetected {
		return item.Host + " " + gui.Tr.RegistriesAutoDetectedSuffix
	}
	return item.Host
}

func (gui *Gui) registriesStatusDisplay(status registryItemStatus) string {
	switch status {
	case registryItemStatusOnline:
		return utils.ColoredString(gui.Tr.RegistriesStatusOnline, color.FgGreen)
	case registryItemStatusOffline:
		return utils.ColoredString(gui.Tr.RegistriesStatusOffline, color.FgRed)
	case registryItemStatusUnauthorized:
		return utils.ColoredString(gui.Tr.RegistriesStatusUnauthorized, color.FgYellow)
	default:
		return gui.Tr.RegistriesStatusChecking
	}
}

func registriesDurationDisplay(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.Round(time.Millisecond).String()
}

// onRegistriesPanelFocused is the D-09 gate. It runs whenever the Registries
// view gains focus; on the first focus it kicks the initial refresh, and
// never does anything before that. Later refocuses do not re-ping — the
// panel's refresh key does.
func (gui *Gui) onRegistriesPanelFocused() {
	if gui.Panels.Registries == nil || gui.Panels.Registries.IsHidden() {
		return
	}

	gui.registriesState.mu.Lock()
	alreadyLoaded := gui.registriesState.loaded
	gui.registriesState.mu.Unlock()

	if alreadyLoaded {
		return
	}
	gui.refreshRegistries()
}

// handleRegistriesRefresh is the panel's refresh keybinding ('r'): re-discover
// auth sources and re-ping every registry, or reload repositories if drilled down,
// dropping any in-flight results from the previous round.
func (gui *Gui) handleRegistriesRefresh(g *gocui.Gui, v *gocui.View) error {
	if gui.isRegistriesDrilledDown() {
		level := gui.registriesState.currentDrillLevel()
		if level != nil && level.item != nil {
			gen := gui.registriesState.bumpGeneration()
			gui.Panels.Registries.NoItemsMessage = gui.Tr.NoRepositories
			gui.Panels.Registries.SetItems([]*registryItem{})
			_ = gui.Panels.Registries.RerenderList()
			gui.loadRepositories(gen, level.item)
			return nil
		}
	}
	gui.refreshRegistries()
	return nil
}

func (gui *Gui) handleRegistriesEnter(g *gocui.Gui, v *gocui.View) error {
	if gui.Panels.Registries == nil {
		return nil
	}
	item, err := gui.Panels.Registries.GetSelectedItem()
	if err != nil || item == nil {
		return nil
	}

	if item.Kind == registryKindRegistry {
		return gui.drillIntoRegistry(item)
	}

	return gui.handleEnterMain(g, v)
}

func (gui *Gui) drillIntoRegistry(item *registryItem) error {
	if gui.State.Filter.active {
		_ = gui.clearFilter()
	}

	prevItems := gui.Panels.Registries.List.GetAllItems()
	prevSelectedIdx := gui.Panels.Registries.SelectedIdx

	gui.registriesState.mu.Lock()
	gui.registriesState.generation++
	gen := gui.registriesState.generation
	level := &registriesDrillLevel{
		kind:        registryKindRepository,
		title:       gui.Tr.RegistriesTitle + " › " + item.Host,
		item:        item,
		items:       prevItems,
		selectedIdx: prevSelectedIdx,
	}
	gui.registriesState.drillStack = append(gui.registriesState.drillStack, level)
	gui.registriesState.mu.Unlock()

	if gui.Views.Registries != nil {
		gui.Views.Registries.Title = level.title
	}
	gui.Panels.Registries.NoItemsMessage = gui.Tr.NoRepositories
	gui.Panels.Registries.SetItems([]*registryItem{})
	_ = gui.Panels.Registries.RerenderList()

	gui.loadRepositories(gen, item)
	return nil
}

func (gui *Gui) handleRegistriesEscape(g *gocui.Gui, v *gocui.View) error {
	if gui.State.Filter.active {
		return gui.clearFilter()
	}
	if gui.isRegistriesDrilledDown() {
		return gui.popRegistriesDrill()
	}
	return nil
}

func (gui *Gui) popRegistriesDrill() error {
	gui.registriesState.mu.Lock()
	n := len(gui.registriesState.drillStack)
	if n == 0 {
		gui.registriesState.mu.Unlock()
		return nil
	}
	popped := gui.registriesState.drillStack[n-1]
	gui.registriesState.drillStack = gui.registriesState.drillStack[:n-1]
	gui.registriesState.generation++
	remaining := len(gui.registriesState.drillStack)
	var parentLevel *registriesDrillLevel
	if remaining > 0 {
		parentLevel = gui.registriesState.drillStack[remaining-1]
	}
	gui.registriesState.mu.Unlock()

	if remaining == 0 {
		if gui.Views.Registries != nil {
			gui.Views.Registries.Title = gui.Tr.RegistriesTitle
		}
		gui.Panels.Registries.NoItemsMessage = gui.Tr.NoRegistries
	} else {
		if gui.Views.Registries != nil {
			gui.Views.Registries.Title = parentLevel.title
		}
		gui.Panels.Registries.NoItemsMessage = gui.Tr.NoRepositories
	}

	gui.Panels.Registries.SetItems(popped.items)
	gui.Panels.Registries.SetSelectedLineIdx(popped.selectedIdx)
	if gui.IsCurrentView(gui.Views.Registries) {
		_ = gui.Panels.Registries.HandleSelect()
	}
	_ = gui.Panels.Registries.RerenderList()
	return nil
}

func (gui *Gui) updateUI(f func(*gocui.Gui) error) {
	gui.registriesState.mu.Lock()
	fn := gui.registriesState.updateUIFn
	gui.registriesState.mu.Unlock()

	if fn != nil {
		fn(f)
		return
	}
	if gui.g == nil {
		_ = f(nil)
		return
	}
	gui.g.Update(f)
}

func (gui *Gui) loadRepositories(gen uint64, item *registryItem) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		source := gui.getRepositorySource()
		repos, err := source.ListRepositories(ctx, item)

		gui.updateUI(func(*gocui.Gui) error {
			gui.applyRepositories(gen, item, repos, err)
			return nil
		})
	}()
}

func (gui *Gui) applyRepositories(gen uint64, parentItem *registryItem, repos []registryRepository, err error) {
	if !gui.registriesState.isCurrent(gen) {
		return
	}
	level := gui.registriesState.currentDrillLevel()
	if level == nil || level.item != parentItem {
		return
	}

	if err != nil {
		level.err = err.Error()
		gui.Panels.Registries.NoItemsMessage = err.Error()
		gui.Panels.Registries.SetItems([]*registryItem{})
		_ = gui.QueueTask(gui.NewSimpleRenderStringTask(func() string {
			return err.Error()
		}))
		_ = gui.Panels.Registries.RerenderList()
		return
	}

	level.err = ""
	items := make([]*registryItem, len(repos))
	for i, r := range repos {
		items[i] = &registryItem{
			Kind:       registryKindRepository,
			Host:       parentItem.Host,
			Repository: r.Name,
			Visibility: r.Visibility,
		}
	}

	gui.Panels.Registries.NoItemsMessage = gui.Tr.NoRepositories
	gui.Panels.Registries.SetItems(items)
	if len(items) == 0 {
		_ = gui.QueueTask(gui.NewSimpleRenderStringTask(func() string {
			return gui.Tr.NoRepositories
		}))
	} else if gui.IsCurrentView(gui.Views.Registries) {
		_ = gui.Panels.Registries.HandleSelect()
	}
	_ = gui.Panels.Registries.RerenderList()
}

// refreshRegistries re-reads saved connections and auto-detected auth sources
// and re-pings every registry. Store reads, discovery and pings run off the UI
// thread; results land on the UI thread via gui.g.Update and are dropped when
// a newer refresh has since been issued (S-03).
func (gui *Gui) refreshRegistries() {
	if gui.Panels.Registries == nil || gui.g == nil {
		return
	}
	gen := gui.registriesState.beginRefresh()

	go func() {
		saved, storePath, savedErr := gui.registriesLoadSaved()
		if savedErr != nil {
			gui.Log.Errorf("registries: loading saved connections: %s", savedErr)
		}
		sources, discoverErr := gui.registriesDiscover()
		if discoverErr != nil {
			// credentials.Discover errors are secret-free by construction.
			// When no sources were returned, the panel keeps its current rows
			// and the user can retry with 'r'.
			gui.Log.Errorf("registries: discovering auth sources: %s", discoverErr)
		}
		if (savedErr != nil || discoverErr != nil) && len(saved) == 0 && len(sources) == 0 {
			return
		}

		items := mergeRegistryItems(saved, storePath, sources)

		gui.g.Update(func(*gocui.Gui) error {
			gui.applyRegistriesRefresh(gen, items)
			return nil
		})

		for _, item := range items {
			go gui.pingRegistriesItem(gen, item)
		}
	}()
}

// mergeRegistryItems merges saved connections from registries.yml with
// auto-detected auth sources. A host present in both appears once as a saved
// connection (AutoDetected: false, no "(auto)" suffix). If a saved connection
// left Username empty and a matching auto-detected source has a username, the
// auto-detected username is used for display.
func mergeRegistryItems(saved []connections.Connection, storePath string, sources []credentials.AuthSource) []*registryItem {
	items := make([]*registryItem, 0, len(saved)+len(sources))
	savedByHost := make(map[string]*registryItem, len(saved))

	for _, rawConn := range saved {
		conn := rawConn.Normalize()
		host := conn.Host()
		if host == "" {
			continue
		}
		if _, exists := savedByHost[host]; exists {
			continue
		}
		item := &registryItem{
			Kind:         registryKindRegistry,
			Host:         host,
			Username:     conn.Username,
			SourceFile:   storePath,
			AutoDetected: false,
			Connection:   conn,
			Status:       registryItemStatusChecking,
		}
		savedByHost[host] = item
		items = append(items, item)
	}

	seenAuto := make(map[string]bool, len(sources))
	for _, src := range sources {
		host := credentials.NormalizeRegistry(src.Registry)
		if host == "" {
			continue
		}
		if savedItem, exists := savedByHost[host]; exists {
			if savedItem.Username == "" && src.Username != "" {
				savedItem.Username = src.Username
			}
			continue
		}
		if seenAuto[host] {
			continue
		}
		seenAuto[host] = true
		items = append(items, &registryItem{
			Kind:         registryKindRegistry,
			Host:         host,
			Username:     src.Username,
			SourceFile:   src.Source,
			AutoDetected: true,
			Status:       registryItemStatusChecking,
		})
	}

	return items
}

// applyRegistriesRefresh installs a freshly discovered item set, unless a
// newer refresh has superseded this generation. Runs on the UI thread.
func (gui *Gui) applyRegistriesRefresh(gen uint64, items []*registryItem) {
	if !gui.registriesState.isCurrent(gen) {
		return
	}
	gui.Panels.Registries.SetItems(items)
	if gui.IsCurrentView(gui.Views.Registries) {
		_ = gui.Panels.Registries.HandleSelect()
	}
	_ = gui.Panels.Registries.RerenderList()
}

// pingRegistriesItem resolves credentials for one registry — only now, and
// only for that registry, because a credential helper can block on a keychain
// prompt (D-09) — then builds a client and pings it. The result lands on the
// UI thread and is dropped when a newer refresh has since been issued.
func (gui *Gui) pingRegistriesItem(gen uint64, item *registryItem) {
	ctx, cancel := context.WithTimeout(context.Background(), registriesPingTimeout)
	defer cancel()

	res, err := gui.registriesPing(ctx, item.Host)

	gui.g.Update(func(*gocui.Gui) error {
		gui.applyRegistriesPing(gen, item, res, err)
		return nil
	})
}

// applyRegistriesPing records one ping result on its row, unless a newer
// refresh has superseded this generation (that refresh also installed fresh
// item pointers, so an old pointer must never be written to). Runs on the UI
// thread. Errors shown here are the registry/credentials packages' own,
// already secret-free, messages.
func (gui *Gui) applyRegistriesPing(gen uint64, item *registryItem, res registry.PingResult, err error) {
	if !gui.registriesState.isCurrent(gen) {
		return
	}

	item.Duration = res.Duration
	item.Err = ""
	if err != nil {
		item.Err = err.Error()
	}
	if res.Status != "" {
		item.Status = registryItemStatus(res.Status)
	} else {
		item.Status = registryItemStatusOffline
	}

	if gui.IsCurrentView(gui.Views.Registries) {
		_ = gui.Panels.Registries.HandleSelect()
	}
	_ = gui.Panels.Registries.RerenderList()
}

// registriesStore returns the saved-connections Store for gui.Config.ConfigDir
// (or the test-injected override on registriesState).
func (gui *Gui) registriesStore() *connections.Store {
	gui.registriesState.mu.Lock()
	s := gui.registriesState.store
	gui.registriesState.mu.Unlock()
	if s != nil {
		return s
	}
	var configDir string
	if gui.Config != nil {
		configDir = gui.Config.ConfigDir
	}
	return connections.NewStore(configDir)
}

// registriesLoadSaved reads saved connections lazily (D-09: only called during
// refreshRegistries after first focus or manual refresh).
func (gui *Gui) registriesLoadSaved() ([]connections.Connection, string, error) {
	gui.registriesState.mu.Lock()
	loadFn := gui.registriesState.storeLoadFn
	s := gui.registriesState.store
	gui.registriesState.mu.Unlock()

	if loadFn != nil {
		conns, err := loadFn()
		storePath := ""
		if s != nil {
			storePath = s.Path()
		} else if gui.Config != nil && gui.Config.ConfigDir != "" {
			storePath = connections.NewStore(gui.Config.ConfigDir).Path()
		}
		return conns, storePath, err
	}

	if s == nil && (gui.Config == nil || gui.Config.ConfigDir == "") {
		return nil, "", nil
	}
	store := gui.registriesStore()
	conns, err := store.List()
	return conns, store.Path(), err
}

// registriesDiscover returns the auto-detected registry auth sources,
// delegating to the injectable hook so tests can count calls (D-09) and fake
// results without touching ~/.docker.
func (gui *Gui) registriesDiscover() ([]credentials.AuthSource, error) {
	if fn := gui.registriesDiscoverFn; fn != nil {
		return fn()
	}
	return credentials.Discover(credentials.DiscoverOptions{})
}

// registriesPing pings one registry host, delegating to the injectable hook
// so tests can fake verdicts without any network.
func (gui *Gui) registriesPing(ctx context.Context, host string) (registry.PingResult, error) {
	if fn := gui.registriesPingFn; fn != nil {
		return fn(ctx, host)
	}
	return pingRegistryWith(ctx, host, credentials.Resolve)
}

// pingHostForRegistry maps a canonical registry name to the host its /v2/
// ping should hit. Docker Hub is the one special case: auth files key on the
// canonical name docker.io, but the registry API lives on
// registry-1.docker.io.
func pingHostForRegistry(registryName string) string {
	if credentials.NormalizeRegistry(registryName) == "docker.io" {
		return "registry-1.docker.io"
	}
	return registryName
}

// pingRegistryWith is the real ping path, parameterised only by its resolve
// function so tests can drive it against the in-process registrytest fixture
// with a fake resolver instead of the real ~/.docker. Credential resolution
// happens here, lazily and per registry (D-09): a resolution failure does not
// skip the ping — the registry's own verdict matters more, and an
// unauthorized verdict is reported alongside the resolution error, which is
// the likelier cause. The secret travels from the resolver straight into the
// client's redacting Credential; it is never logged, rendered, or stored.
func pingRegistryWith(ctx context.Context, host string, resolve func(context.Context, string) (credentials.Secret, error)) (registry.PingResult, error) {
	var cred *registry.Credential
	var resolveErr error
	if sec, err := resolve(ctx, host); err == nil {
		cred = credentialFromSecret(sec)
	} else if !errors.Is(err, credentials.ErrNotFound) {
		resolveErr = err // secret-free by construction in pkg/registry/credentials
	}

	client, err := registry.NewClient(pingHostForRegistry(host), registry.Options{Credential: cred})
	if err != nil {
		return registry.PingResult{Status: registry.StatusOffline}, err
	}

	result, err := client.Ping(ctx)
	if err != nil {
		return result, err
	}
	if resolveErr != nil && result.Status == registry.StatusUnauthorized {
		// anonymous ping rejected and resolution failed: the resolution
		// error explains the unauthorized verdict better than the verdict
		// alone; surface it on the row (the verdict is kept too).
		return result, resolveErr
	}
	return result, nil
}
