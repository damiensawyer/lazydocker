package gui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazydocker/pkg/commands"
	"github.com/jesseduffield/lazydocker/pkg/config"
	"github.com/jesseduffield/lazydocker/pkg/i18n"
	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/jesseduffield/lazydocker/pkg/registry/connections"
	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
	"github.com/jesseduffield/lazydocker/pkg/registry/dockerhub"
	"github.com/jesseduffield/lazydocker/pkg/registry/ghcr"
	"github.com/jesseduffield/lazydocker/pkg/registry/registrytest"
	"github.com/jesseduffield/lazydocker/pkg/tasks"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// newHeadlessRegistriesTestGui builds a Gui just sufficient to exercise the
// Registries side panel without a real terminal: a headless gocui, every
// view the panel code touches, and the full side-panel set (so the focus and
// layout paths run for real). The discover/ping hooks are NOT set here;
// each test installs fakes so nothing ever touches ~/.docker, a credential
// helper, or the network.
func newHeadlessRegistriesTestGui(t *testing.T) *Gui {
	t.Helper()

	log := logrus.NewEntry(logrus.New())
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 120, Height: 40})
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

	tr := i18n.NewTranslationSet(log, "en")
	userConfig := config.GetDefaultConfig()

	gui := &Gui{
		g:             g,
		Log:           log,
		Tr:            tr,
		Config:        &config.AppConfig{UserConfig: &userConfig},
		DockerCommand: &commands.DockerCommand{Config: &config.AppConfig{}}, // only field reads (IsProjectScoped) on these paths
		taskManager:   tasks.NewTaskManager(log, tr),
		State: guiState{
			Panels: &panelStates{Main: &mainPanelState{}},
		},
		Views: Views{
			Project:      newView("project"),
			Services:     newView("services"),
			Containers:   newView("containers"),
			Images:       newView("images"),
			Volumes:      newView("volumes"),
			Networks:     newView("networks"),
			Registries:   newView("registries"),
			Main:         newView("main"),
			Menu:         newView("menu"),
			Options:      newView("options"),
			Confirmation: newView("confirmation"),
			Filter:       newView("filter"),
		},
	}
	gui.setPanels()
	gui.Views.Registries.Title = gui.Tr.RegistriesTitle
	gui.registriesState.updateUIFn = func(f func(*gocui.Gui) error) {
		_ = f(gui.g)
	}
	return gui
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The D-09 contract: constructing the gui, registering keybindings, laying
// out the side panels and rendering the Registries panel's list must not
// read the docker/podman config, exec a credential helper, or ping anything.
// All of that starts when the panel first gets focus — and only then.
func TestRegistriesPanelDoesNothingUntilFirstFocus(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)

	var storeCalls, discoverCalls, pingCalls atomic.Int64
	gui.registriesState.storeLoadFn = func() ([]connections.Connection, error) {
		storeCalls.Add(1)
		return nil, nil
	}
	gui.registriesDiscoverFn = func() ([]credentials.AuthSource, error) {
		discoverCalls.Add(1)
		return []credentials.AuthSource{
			{Registry: "docker.io", Username: "user", Source: "/home/u/.docker/config.json"},
			{Registry: "localhost:5000"},
		}, nil
	}
	gui.registriesPingFn = func(ctx context.Context, host string) (registry.PingResult, error) {
		pingCalls.Add(1)
		return registry.PingResult{Status: registry.StatusOnline, Duration: 5 * time.Millisecond}, nil
	}

	// everything the startup/render path does for the panel
	_ = gui.GetInitialKeybindings()
	_ = gui.sideViewNames()
	if err := gui.Panels.Registries.RerenderList(); err != nil {
		t.Fatalf("RerenderList: %s", err)
	}

	if got := storeCalls.Load(); got != 0 {
		t.Fatalf("Store.List called %d time(s) before the panel was focused; want 0 (D-09)", got)
	}
	if got := discoverCalls.Load(); got != 0 {
		t.Fatalf("Discover called %d time(s) before the panel was focused; want 0 (D-09)", got)
	}
	if got := pingCalls.Load(); got != 0 {
		t.Fatalf("ping called %d time(s) before the panel was focused; want 0 (D-09)", got)
	}

	// first focus: one store read, one discovery, then one ping per auth source
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus to registries: %s", err)
	}
	waitUntil(t, 5*time.Second, func() bool { return storeCalls.Load() == 1 },
		"first focus must read the saved-connections store exactly once")
	waitUntil(t, 5*time.Second, func() bool { return discoverCalls.Load() == 1 },
		"first focus must run credential discovery exactly once")
	waitUntil(t, 5*time.Second, func() bool { return pingCalls.Load() == 2 },
		"one ping per discovered auth source must run after first focus")

	// later refocuses must not re-run the initial load (the refresh key does)
	if err := gui.switchFocus(gui.Views.Networks); err != nil {
		t.Fatalf("switchFocus away: %s", err)
	}
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus back: %s", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := storeCalls.Load(); got != 1 {
		t.Fatalf("refocus re-read saved connections (%d calls); only the refresh key should", got)
	}
	if got := discoverCalls.Load(); got != 1 {
		t.Fatalf("refocus re-ran discovery (%d calls); only the refresh key should", got)
	}

	// pressing 'r' (handleRegistriesRefresh) must re-run store read, discovery and ping
	if err := gui.handleRegistriesRefresh(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesRefresh: %s", err)
	}
	waitUntil(t, 5*time.Second, func() bool { return storeCalls.Load() == 2 },
		"refresh key must re-read the saved-connections store")
	waitUntil(t, 5*time.Second, func() bool { return discoverCalls.Load() == 2 },
		"refresh key must re-run credential discovery")
	waitUntil(t, 5*time.Second, func() bool { return pingCalls.Load() == 4 },
		"refresh key must re-ping every discovered auth source")
}

// The S-03 contract: a refresh opens a new generation; discovery sets and
// ping results from superseded generations are dropped when they land.
func TestRegistriesRefreshDropsSupersededGenerations(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)

	gen1 := gui.registriesState.beginRefresh()
	gen2 := gui.registriesState.beginRefresh()

	staleItem := &registryItem{Kind: registryKindRegistry, Host: "stale.example", Status: registryItemStatusChecking}
	freshItem := &registryItem{Kind: registryKindRegistry, Host: "fresh.example", Status: registryItemStatusChecking}

	gui.applyRegistriesRefresh(gen1, []*registryItem{staleItem})
	if got := gui.Panels.Registries.List.Len(); got != 0 {
		t.Fatalf("stale generation installed %d item(s); want 0 (dropped)", got)
	}

	gui.applyRegistriesRefresh(gen2, []*registryItem{freshItem})
	if got := gui.Panels.Registries.List.Len(); got != 1 {
		t.Fatalf("current generation installed %d item(s); want 1", got)
	}
	if got, ok := gui.Panels.Registries.List.TryGet(0); !ok || got != freshItem {
		t.Fatal("current generation must install its own item set")
	}

	// a ping from the superseded generation must not write to its dead item
	gui.applyRegistriesPing(gen1, staleItem,
		registry.PingResult{Status: registry.StatusOnline, Duration: time.Second}, nil)
	if staleItem.Status != registryItemStatusChecking || staleItem.Duration != 0 {
		t.Fatal("a superseded generation's ping result must be dropped")
	}

	// a current-generation ping lands on the row
	gui.applyRegistriesPing(gen2, freshItem,
		registry.PingResult{Status: registry.StatusOnline, Duration: 30 * time.Millisecond}, nil)
	if freshItem.Status != registryItemStatusOnline || freshItem.Duration != 30*time.Millisecond {
		t.Fatalf("current ping result not recorded: status=%q duration=%v", freshItem.Status, freshItem.Duration)
	}

	// an unauthorized verdict with a resolution error keeps the verdict and
	// surfaces the (secret-free) error message on the row
	gui.applyRegistriesPing(gen2, freshItem,
		registry.PingResult{Status: registry.StatusUnauthorized, Duration: 10 * time.Millisecond},
		errors.New(`credential helper "desktop" failed: exit status 1`))
	if freshItem.Status != registryItemStatusUnauthorized {
		t.Fatalf("status = %q; want unauthorized (kept alongside the error)", freshItem.Status)
	}
	if !strings.Contains(freshItem.Err, `credential helper "desktop" failed`) {
		t.Fatalf("error message not surfaced on the row: %q", freshItem.Err)
	}
}

// The seam between the credential layer and the HTTP layer (D-06/D-09):
// every field copied, none transformed, none invented.
func TestCredentialFromSecretMapsEveryField(t *testing.T) {
	sec := credentials.Secret{
		Username:      "user",
		Password:      "pass",
		IdentityToken: "identity",
		RegistryToken: "registry",
	}
	cred := credentialFromSecret(sec)
	if cred == nil {
		t.Fatal("credentialFromSecret returned nil")
	}
	if cred.Username != "user" || cred.Password != "pass" ||
		cred.IdentityToken != "identity" || cred.RegistryToken != "registry" {
		t.Fatalf("field mapping wrong: %s", cred)
	}

	empty := credentialFromSecret(credentials.Secret{})
	if empty.Username != "" || empty.Password != "" ||
		empty.IdentityToken != "" || empty.RegistryToken != "" {
		t.Fatalf("empty secret must map to an empty credential, got %s", empty)
	}
}

func TestPingHostForRegistry(t *testing.T) {
	scenarios := []struct {
		registry string
		want     string
	}{
		// Docker Hub: auth files key on the canonical name, the API lives on
		// registry-1.docker.io
		{registry: "docker.io", want: "registry-1.docker.io"},
		{registry: "index.docker.io", want: "registry-1.docker.io"},
		{registry: "registry-1.docker.io", want: "registry-1.docker.io"},
		{registry: "https://index.docker.io/v1/", want: "registry-1.docker.io"},
		// everything else pings itself
		{registry: "ghcr.io", want: "ghcr.io"},
		{registry: "localhost:5000", want: "localhost:5000"},
		{registry: "registry.example.com:8443", want: "registry.example.com:8443"},
	}
	for _, s := range scenarios {
		if got := pingHostForRegistry(s.registry); got != s.want {
			t.Errorf("pingHostForRegistry(%q) = %q; want %q", s.registry, got, s.want)
		}
	}
}

// The real ping path against the in-process OCI fixture: a resolved secret
// becomes a basic-auth credential and reaches the registry (the fixture
// records only the username — never the password).
func TestPingRegistryWithResolvedCredential(t *testing.T) {
	srv := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBasic,
		Username: "user",
		Password: "pass",
	})
	defer srv.Close()

	res, err := pingRegistryWith(context.Background(), srv.Host(),
		func(context.Context, string) (credentials.Secret, error) {
			return credentials.Secret{Username: "user", Password: "pass"}, nil
		})
	if err != nil {
		t.Fatalf("ping: %s", err)
	}
	if res.Status != registry.StatusOnline {
		t.Fatalf("status = %q; want online", res.Status)
	}
	if res.Duration <= 0 {
		t.Fatal("ping duration not recorded")
	}

	sawCredentialedPing := false
	for _, r := range srv.Requests() {
		if r.Path == "/v2/" && r.Authorization == "basic" && r.BasicUsername == "user" {
			sawCredentialedPing = true
		}
	}
	if !sawCredentialedPing {
		t.Fatal("the resolved secret never reached the registry as basic auth")
	}
}

// No credentials for a registry (ErrNotFound) means an anonymous ping, not a
// skipped one.
func TestPingRegistryAnonymouslyWhenNoCredentials(t *testing.T) {
	srv := registrytest.New(registrytest.Config{Auth: registrytest.AuthNone})
	defer srv.Close()

	res, err := pingRegistryWith(context.Background(), srv.Host(),
		func(context.Context, string) (credentials.Secret, error) {
			return credentials.Secret{}, credentials.ErrNotFound
		})
	if err != nil {
		t.Fatalf("ping: %s", err)
	}
	if res.Status != registry.StatusOnline {
		t.Fatalf("status = %q; want online (anonymous)", res.Status)
	}
	for _, r := range srv.Requests() {
		if r.Path == "/v2/" && r.Authorization != "" {
			t.Fatalf("anonymous ping sent an Authorization header (%q)", r.Authorization)
		}
	}
}

// A failed resolution (helper exec error) does not skip the ping: the
// registry's own verdict stands, and the secret-free resolution error is
// surfaced alongside an unauthorized verdict, which it most likely explains.
func TestPingRegistrySurfacesResolutionErrorWithUnauthorized(t *testing.T) {
	srv := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBasic,
		Username: "user",
		Password: "pass",
	})
	defer srv.Close()

	res, err := pingRegistryWith(context.Background(), srv.Host(),
		func(context.Context, string) (credentials.Secret, error) {
			return credentials.Secret{}, errors.New("keychain locked")
		})
	if res.Status != registry.StatusUnauthorized {
		t.Fatalf("status = %q; want unauthorized", res.Status)
	}
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("resolution error not surfaced: %v", err)
	}
}

// A rejected credential reports unauthorized without an error of its own —
// the verdict is the message.
func TestPingRegistryWithRejectedCredential(t *testing.T) {
	srv := registrytest.New(registrytest.Config{
		Auth:     registrytest.AuthBasic,
		Username: "user",
		Password: "pass",
	})
	defer srv.Close()

	res, err := pingRegistryWith(context.Background(), srv.Host(),
		func(context.Context, string) (credentials.Secret, error) {
			return credentials.Secret{Username: "user", Password: "wrong"}, nil
		})
	if err != nil {
		t.Fatalf("a rejected credential is a verdict, not an error: %s", err)
	}
	if res.Status != registry.StatusUnauthorized {
		t.Fatalf("status = %q; want unauthorized", res.Status)
	}
}

func TestRegistriesPanelVisibilityFollowsConfig(t *testing.T) {
	if !config.GetDefaultConfig().Registries.Enabled {
		t.Fatal("the Registries panel must be enabled by default — this fork exists for it")
	}

	gui := newHeadlessRegistriesTestGui(t)
	if gui.Panels.Registries.IsHidden() {
		t.Fatal("panel must be visible with the default config")
	}
	if !lo.Contains(gui.sideViewNames(), "registries") {
		t.Fatal("registries view missing from the visible side panels")
	}

	gui.Config.UserConfig.Registries.Enabled = false
	if !gui.Panels.Registries.IsHidden() {
		t.Fatal("registries.enabled = false must hide the panel")
	}
	if lo.Contains(gui.sideViewNames(), "registries") {
		t.Fatal("a hidden panel must leave the side-panel layout")
	}

	// a hidden panel must stay inert even if focus somehow reaches it
	var calls atomic.Int64
	gui.registriesDiscoverFn = func() ([]credentials.AuthSource, error) {
		calls.Add(1)
		return nil, nil
	}
	gui.onRegistriesPanelFocused()
	if got := calls.Load(); got != 0 {
		t.Fatal("a disabled panel must not start discovery (D-09)")
	}
}

func TestRegistriesKeybindingsRegistered(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)

	var hasFocusKey, hasRefreshKey, hasAddKey, hasEditKey, hasDeleteKey bool
	for _, b := range gui.GetInitialKeybindings() {
		if b.Key == '7' && b.Description == gui.Tr.FocusRegistries {
			hasFocusKey = true
		}
		if b.ViewName == "registries" && b.Key == 'r' && b.Description == gui.Tr.RefreshRegistries {
			hasRefreshKey = true
		}
		if b.ViewName == "registries" && b.Key == 'n' && b.Description == gui.Tr.AddRegistryConnection {
			hasAddKey = true
		}
		if b.ViewName == "registries" && b.Key == 'e' && b.Description == gui.Tr.EditRegistryConnection {
			hasEditKey = true
		}
		if b.ViewName == "registries" && b.Key == 'd' && b.Description == gui.Tr.DeleteRegistryConnection {
			hasDeleteKey = true
		}
	}
	if !hasFocusKey {
		t.Fatal("no '7' binding to focus the registries panel")
	}
	if !hasRefreshKey {
		t.Fatal("no 'r' refresh binding on the registries panel")
	}
	if !hasAddKey {
		t.Fatal("no 'n' add binding on the registries panel")
	}
	if !hasEditKey {
		t.Fatal("no 'e' edit binding on the registries panel")
	}
	if !hasDeleteKey {
		t.Fatal("no 'd' delete binding on the registries panel")
	}
}

// The rows the panel renders: status column, host with the auto-detected
// marker, username. No secrets — the item type cannot even hold one.
func TestGetRegistriesDisplayStrings(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)

	item := &registryItem{
		Kind:         registryKindRegistry,
		Host:         "ghcr.io",
		Username:     "user",
		AutoDetected: true,
		Status:       registryItemStatusOnline,
	}
	cells := gui.getRegistriesDisplayStrings(item)
	if len(cells) != 3 {
		t.Fatalf("got %d cells; want 3 (status, host, username)", len(cells))
	}
	if !strings.Contains(cells[0], gui.Tr.RegistriesStatusOnline) {
		t.Fatalf("status cell = %q; want it to contain %q", cells[0], gui.Tr.RegistriesStatusOnline)
	}
	if want := "ghcr.io " + gui.Tr.RegistriesAutoDetectedSuffix; cells[1] != want {
		t.Fatalf("host cell = %q; want %q", cells[1], want)
	}
	if cells[2] != "user" {
		t.Fatalf("username cell = %q; want %q", cells[2], "user")
	}

	// the summary must name registry, username, source file and status, and
	// must never contain a secret field (there is none to leak, but the
	// contract is worth pinning)
	summary := gui.registriesSummaryStr(item)
	for _, want := range []string{"ghcr.io", "user", gui.Tr.RegistriesStatusOnline} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

// setupTestConnectionStore attaches a saved-connections Store backed by
// temporary ConfigDir and DockerConfigDir so tests never touch ~/.docker or
// ~/.config/lazydocker.
func setupTestConnectionStore(t *testing.T, gui *Gui) (*connections.Store, string, string) {
	// Headless: no event loop runs, so popups must open synchronously.
	gui.uiUpdate = func(f func(*gocui.Gui) error) { _ = f(gui.g) }
	t.Helper()
	configDir := t.TempDir()
	dockerDir := t.TempDir()
	gui.Config.ConfigDir = configDir
	store := connections.NewStoreWithOptions(connections.Options{
		ConfigDir:       configDir,
		DockerConfigDir: dockerDir,
		DiscoverOptions: credentials.DiscoverOptions{
			DockerConfigDir: dockerDir,
			HomeDir:         t.TempDir(),
		},
	})
	gui.registriesState.store = store
	gui.registriesDiscoverFn = func() ([]credentials.AuthSource, error) {
		return nil, nil
	}
	gui.registriesPingFn = func(ctx context.Context, host string) (registry.PingResult, error) {
		return registry.PingResult{Status: registry.StatusOnline, Duration: time.Millisecond}, nil
	}
	return store, configDir, dockerDir
}

func confirmCurrentPopup(t *testing.T, gui *Gui) {
	t.Helper()
	fn := gui.popupBindings.confirm
	if fn == nil {
		t.Fatal("no active confirmation callback bound on the confirmation view")
	}
	if err := fn(gui.g, gui.Views.Confirmation); err != nil {
		t.Fatalf("confirming popup: %s", err)
	}
}

func setPromptInput(v *gocui.View, text string) {
	v.ClearTextArea()
	_, _ = v.Write([]byte(text))
}

// Saved connections merged with auto-detected sources: a host present in both
// appears once as saved (AutoDetected: false, no "(auto)" suffix).
func TestMergeRegistryItemsSavedAndAutoDetected(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)

	saved := []connections.Connection{
		{Kind: connections.KindDockerHub, URL: "https://index.docker.io/v1/", Username: "saved-hub"},
		{Kind: connections.KindGHCR, URL: "ghcr.io", Username: ""},
		{Kind: connections.KindRegistry, URL: "registry.internal:5000", Username: "ops"},
	}
	sources := []credentials.AuthSource{
		{Registry: "docker.io", Username: "auto-hub", Source: "/tmp/docker/config.json"},
		{Registry: "ghcr.io", Username: "octocat", Source: "/tmp/docker/config.json"},
		{Registry: "quay.io", Username: "quayuser", Source: "/tmp/containers/auth.json"},
	}

	items := mergeRegistryItems(saved, "/tmp/lazydocker/registries.yml", sources)
	if len(items) != 4 {
		t.Fatalf("merged %d items; want 4 (docker.io, ghcr.io, registry.internal:5000, quay.io)", len(items))
	}

	byHost := make(map[string]*registryItem, len(items))
	for _, it := range items {
		if _, dup := byHost[it.Host]; dup {
			t.Fatalf("duplicate host %q in merged items", it.Host)
		}
		byHost[it.Host] = it
	}

	// docker.io is in both: shows once as saved, keeps saved username, no (auto) suffix
	hub := byHost["docker.io"]
	if hub == nil {
		t.Fatal("missing docker.io")
	}
	if hub.AutoDetected {
		t.Fatal("docker.io is in saved connections; AutoDetected must be false")
	}
	if hub.Username != "saved-hub" {
		t.Fatalf("docker.io username = %q; want %q", hub.Username, "saved-hub")
	}
	if got := gui.registriesHostDisplay(hub); got != "docker.io" {
		t.Fatalf("saved row display = %q; want %q (no auto suffix)", got, "docker.io")
	}

	// ghcr.io is in both with empty saved username: stays saved, inherits discovered username for display
	ghcr := byHost["ghcr.io"]
	if ghcr == nil {
		t.Fatal("missing ghcr.io")
	}
	if ghcr.AutoDetected {
		t.Fatal("ghcr.io is in saved connections; AutoDetected must be false")
	}
	if ghcr.Username != "octocat" {
		t.Fatalf("ghcr.io username = %q; want %q (inherited from auto source)", ghcr.Username, "octocat")
	}
	if got := gui.registriesHostDisplay(ghcr); got != "ghcr.io" {
		t.Fatalf("saved ghcr display = %q; want %q (no auto suffix)", got, "ghcr.io")
	}

	// quay.io is only in auto-detected: AutoDetected is true, has (auto) suffix
	quay := byHost["quay.io"]
	if quay == nil {
		t.Fatal("missing quay.io")
	}
	if !quay.AutoDetected {
		t.Fatal("quay.io is auto-detected only; AutoDetected must be true")
	}
	if want := "quay.io " + gui.Tr.RegistriesAutoDetectedSuffix; gui.registriesHostDisplay(quay) != want {
		t.Fatalf("auto row display = %q; want %q", gui.registriesHostDisplay(quay), want)
	}
}

// Opus L3.1 review fix: after applyRegistriesRefresh, if the registries panel
// is the current view, HandleSelect must run immediately so the main view does
// not stay empty until the cursor moves.
func TestApplyRegistriesRefreshSelectsCurrentView(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	gui.registriesDiscoverFn = func() ([]credentials.AuthSource, error) {
		return nil, nil
	}
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus: %s", err)
	}
	gui.State.Panels.Main.ObjectKey = ""

	gen := gui.registriesState.beginRefresh()
	item := &registryItem{
		Kind:     registryKindRegistry,
		Host:     "ghcr.io",
		Username: "alice",
		Status:   registryItemStatusOnline,
	}
	gui.applyRegistriesRefresh(gen, []*registryItem{item})

	if !strings.Contains(gui.State.Panels.Main.ObjectKey, "registries-ghcr.io-") {
		t.Fatalf("Main.ObjectKey = %q after applyRegistriesRefresh on focused registries view; want HandleSelect to populate it immediately", gui.State.Panels.Main.ObjectKey)
	}
}

// Opus L3.4 review fix: prepareConfirmationPanel and createMaskedPromptPanel
// must reset cursor and origin to (0, 0) so input in a masked prompt never
// starts at a stale column left by a previous (e.g. prefilled) prompt.
func TestMaskedPromptResetsStaleCursorAndOrigin(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)

	// Open a prefilled prompt first so the confirmation view has a non-zero cursor
	if err := gui.createPrefilledPromptPanel("Username:", "long-prefilled-username", nil); err != nil {
		t.Fatalf("createPrefilledPromptPanel: %s", err)
	}
	_ = gui.Views.Confirmation.SetCursor(10, 0)
	_ = gui.Views.Confirmation.SetOrigin(4, 0)

	if err := gui.createMaskedPromptPanel("Password:", nil); err != nil {
		t.Fatalf("createMaskedPromptPanel: %s", err)
	}

	cx, cy := gui.Views.Confirmation.Cursor()
	ox, oy := gui.Views.Confirmation.Origin()
	if cx != 0 || cy != 0 {
		t.Fatalf("masked prompt Cursor = (%d, %d); want (0, 0)", cx, cy)
	}
	if ox != 0 || oy != 0 {
		t.Fatalf("masked prompt Origin = (%d, %d); want (0, 0)", ox, oy)
	}
	if got := gui.Views.Confirmation.Buffer(); got != "" {
		t.Fatalf("masked prompt Buffer = %q; want empty", got)
	}
	if got := gui.Views.Confirmation.Mask; got != '*' {
		t.Fatalf("masked prompt Mask = %q; want '*'", got)
	}
}

// Add connection flow: menu for kind -> prefilled URL -> username -> masked
// secret (read RAW, preserving leading/trailing spaces, minus only trailing
// newline) -> Store.Create.
func TestAddRegistryConnectionFlow(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	store, _, _ := setupTestConnectionStore(t, gui)

	// 1. Press 'n' on the registries panel -> kind menu opens
	if err := gui.handleAddRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleAddRegistryConnection: %s", err)
	}
	if !gui.Views.Menu.Visible {
		t.Fatal("kind menu should be visible after 'n'")
	}

	// Select GHCR (index 2: 0=registry, 1=dockerhub, 2=ghcr)
	gui.Panels.Menu.SetSelectedLineIdx(2)
	if err := gui.handleMenuPress(); err != nil {
		t.Fatalf("handleMenuPress: %s", err)
	}

	// 2. URL prompt opens, prefilled with "ghcr.io"
	if !gui.Views.Confirmation.Visible || gui.Views.Confirmation.Title != gui.Tr.RegistryURLPromptTitle {
		t.Fatalf("expected URL prompt, got visible=%v title=%q", gui.Views.Confirmation.Visible, gui.Views.Confirmation.Title)
	}
	if got := gui.trimmedContent(gui.Views.Confirmation); got != "ghcr.io" {
		t.Fatalf("GHCR URL prompt prefill = %q; want %q", got, "ghcr.io")
	}
	confirmCurrentPopup(t, gui)

	// 3. Username prompt opens
	if gui.Views.Confirmation.Title != gui.Tr.RegistryUsernamePromptTitle {
		t.Fatalf("expected username prompt, got title=%q", gui.Views.Confirmation.Title)
	}
	setPromptInput(gui.Views.Confirmation, "octocat")
	confirmCurrentPopup(t, gui)

	// 4. Masked secret prompt opens with Mask='*' and cursor/origin at (0, 0)
	if gui.Views.Confirmation.Title != gui.Tr.RegistrySecretPromptTitle {
		t.Fatalf("expected secret prompt, got title=%q", gui.Views.Confirmation.Title)
	}
	if gui.Views.Confirmation.Mask != '*' {
		t.Fatalf("secret prompt Mask = %q; want '*'", gui.Views.Confirmation.Mask)
	}
	cx, cy := gui.Views.Confirmation.Cursor()
	if cx != 0 || cy != 0 {
		t.Fatalf("secret prompt Cursor = (%d, %d); want (0, 0)", cx, cy)
	}

	// Secret with significant leading/trailing spaces and a trailing newline
	rawSecretWithSpaces := "  ghp_secret_with_spaces  "
	setPromptInput(gui.Views.Confirmation, rawSecretWithSpaces+"\n")
	confirmCurrentPopup(t, gui)

	// Verify connection saved in registries.yml without secret
	saved, err := store.List()
	if err != nil {
		t.Fatalf("store.List: %s", err)
	}
	if len(saved) != 1 {
		t.Fatalf("saved connections = %d; want 1", len(saved))
	}
	if saved[0].Kind != connections.KindGHCR || saved[0].URL != "ghcr.io" || saved[0].Username != "octocat" {
		t.Fatalf("unexpected saved connection: %+v", saved[0])
	}
	if saved[0].SecretOwner != connections.SecretOwnerLazydocker {
		t.Fatalf("SecretOwner = %q; want %q", saved[0].SecretOwner, connections.SecretOwnerLazydocker)
	}

	// Verify the secret was stored RAW (preserving leading & trailing spaces)
	resolved, err := store.ResolveSecret(context.Background(), saved[0])
	if err != nil {
		t.Fatalf("ResolveSecret: %s", err)
	}
	if resolved.Password != rawSecretWithSpaces {
		t.Fatalf("stored secret = %q; want %q (leading/trailing spaces must be preserved, only trailing newline stripped)", resolved.Password, rawSecretWithSpaces)
	}
}

// Edit connection flow: refused for auto-detected rows; for saved rows,
// prefilled chain where empty secret keeps existing credential and non-empty
// secret updates it.
func TestEditRegistryConnectionFlow(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	store, _, _ := setupTestConnectionStore(t, gui)

	// 1. Editing an auto-detected row is refused
	autoItem := &registryItem{
		Kind:         registryKindRegistry,
		Host:         "docker.io",
		Username:     "autouser",
		AutoDetected: true,
	}
	gui.Panels.Registries.SetItems([]*registryItem{autoItem})
	if err := gui.handleEditRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleEditRegistryConnection on auto row: %s", err)
	}
	if gui.Views.Menu.Visible {
		t.Fatal("edit menu must not open for an auto-detected row")
	}
	if !strings.Contains(gui.trimmedContent(gui.Views.Confirmation), gui.Tr.CannotEditAutoDetectedRegistry) {
		t.Fatalf("expected CannotEditAutoDetectedRegistry error panel, got %q", gui.trimmedContent(gui.Views.Confirmation))
	}
	_ = gui.closeConfirmationPrompt()

	// 2. Seed a saved connection with an initial secret
	initialConn := connections.Connection{
		Kind:     connections.KindRegistry,
		URL:      "registry.example.com:5000",
		Username: "alice",
	}
	if err := store.Create(initialConn, credentials.Secret{Username: "alice", Password: "initial-password"}); err != nil {
		t.Fatalf("store.Create: %s", err)
	}

	savedItem := &registryItem{
		Kind:         registryKindRegistry,
		Host:         "registry.example.com:5000",
		Username:     "alice",
		AutoDetected: false,
		Connection:   initialConn,
	}
	gui.Panels.Registries.SetItems([]*registryItem{savedItem})

	// Press 'e' -> kind menu opens, confirm existing kind
	if err := gui.handleEditRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleEditRegistryConnection: %s", err)
	}
	if err := gui.handleMenuPress(); err != nil {
		t.Fatalf("handleMenuPress: %s", err)
	}

	// URL prompt is prefilled with existing URL
	if got := gui.trimmedContent(gui.Views.Confirmation); got != "registry.example.com:5000" {
		t.Fatalf("edit URL prefill = %q; want %q", got, "registry.example.com:5000")
	}
	confirmCurrentPopup(t, gui)

	// Username prompt is prefilled with "alice"; change it to "bob"
	if got := gui.trimmedContent(gui.Views.Confirmation); got != "alice" {
		t.Fatalf("edit username prefill = %q; want %q", got, "alice")
	}
	setPromptInput(gui.Views.Confirmation, "bob")
	confirmCurrentPopup(t, gui)

	// Secret prompt is empty; leave it empty to keep existing password
	if got := gui.Views.Confirmation.Buffer(); got != "" {
		t.Fatalf("edit secret prompt must start empty, got %q", got)
	}
	confirmCurrentPopup(t, gui)

	updated, err := store.Get("registry.example.com:5000")
	if err != nil {
		t.Fatalf("store.Get: %s", err)
	}
	if updated.Username != "bob" {
		t.Fatalf("updated Username = %q; want %q", updated.Username, "bob")
	}
	resolved, err := store.ResolveSecret(context.Background(), updated)
	if err != nil {
		t.Fatalf("ResolveSecret after empty-secret edit: %s", err)
	}
	if resolved.Password != "initial-password" {
		t.Fatalf("empty secret prompt must keep existing password, got %q", resolved.Password)
	}
}

// Delete connection flow: refused for auto-detected rows; confirmation panel
// then Store.Delete for saved rows.
func TestDeleteRegistryConnectionRefusesAutoAndDeletesSaved(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	store, _, _ := setupTestConnectionStore(t, gui)

	if err := store.Create(connections.Connection{
		Kind:     connections.KindGHCR,
		URL:      "ghcr.io",
		Username: "alice",
	}, credentials.Secret{Username: "alice", Password: "secret-to-delete"}); err != nil {
		t.Fatalf("store.Create: %s", err)
	}

	autoItem := &registryItem{
		Kind:         registryKindRegistry,
		Host:         "docker.io",
		Username:     "user",
		AutoDetected: true,
	}
	savedItem := &registryItem{
		Kind:         registryKindRegistry,
		Host:         "ghcr.io",
		Username:     "alice",
		AutoDetected: false,
		Connection:   connections.Connection{Kind: connections.KindGHCR, URL: "ghcr.io", Username: "alice"},
	}
	gui.Panels.Registries.SetItems([]*registryItem{autoItem, savedItem})
	gui.Panels.Registries.SetSelectedLineIdx(0) // docker.io (auto)

	// 1. Pressing 'd' on auto-detected row shows error and does NOT delete
	if err := gui.handleDeleteRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleDeleteRegistryConnection on auto row: %s", err)
	}
	if gui.Views.Confirmation.Title != gui.Tr.ErrorTitle {
		t.Fatalf("Confirmation.Title = %q; want %q (error panel, not delete confirmation)", gui.Views.Confirmation.Title, gui.Tr.ErrorTitle)
	}
	if !strings.Contains(gui.trimmedContent(gui.Views.Confirmation), gui.Tr.CannotDeleteAutoDetectedRegistry) {
		t.Fatalf("Confirmation content = %q; want it to contain %q", gui.trimmedContent(gui.Views.Confirmation), gui.Tr.CannotDeleteAutoDetectedRegistry)
	}
	_ = gui.closeConfirmationPrompt()

	// 2. Select saved row (ghcr.io) and press 'd' -> confirmation panel -> confirm -> deleted
	gui.Panels.Registries.SetSelectedLineIdx(1)
	if err := gui.handleDeleteRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleDeleteRegistryConnection on saved row: %s", err)
	}
	if gui.Views.Confirmation.Title != gui.Tr.DeleteRegistryConnectionTitle {
		t.Fatalf("Confirmation.Title = %q; want %q", gui.Views.Confirmation.Title, gui.Tr.DeleteRegistryConnectionTitle)
	}
	confirmCurrentPopup(t, gui)

	remaining, err := store.List()
	if err != nil {
		t.Fatalf("store.List: %s", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining saved connections = %d; want 0", len(remaining))
	}
}

// On ErrExternalCredentialExists: tell the user their existing docker login
// will be used, and save the connection without overwriting the secret.
func TestRegistryConnectionExternalCredentialExists(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	store, _, dockerDir := setupTestConnectionStore(t, gui)

	// Pre-populate docker config.json with an existing login for ghcr.io
	existingAuth := base64.StdEncoding.EncodeToString([]byte("existing-user:existing-token"))
	configJSON := fmt.Sprintf(`{"auths":{"ghcr.io":{"auth":%q}}}`, existingAuth)
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(configJSON), 0o600); err != nil {
		t.Fatalf("writing config.json: %s", err)
	}

	// Run Add flow for ghcr.io with a new secret
	if err := gui.handleAddRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleAddRegistryConnection: %s", err)
	}
	gui.Panels.Menu.SetSelectedLineIdx(2) // GHCR
	if err := gui.handleMenuPress(); err != nil {
		t.Fatalf("handleMenuPress: %s", err)
	}
	confirmCurrentPopup(t, gui) // URL ("ghcr.io")
	setPromptInput(gui.Views.Confirmation, "new-user")
	confirmCurrentPopup(t, gui) // Username
	setPromptInput(gui.Views.Confirmation, "attempted-overwrite-secret")
	confirmCurrentPopup(t, gui) // Secret -> triggers ErrExternalCredentialExists

	// User is informed that their existing docker login will be used
	if gui.Views.Confirmation.Title != gui.Tr.RegistryExternalCredentialTitle {
		t.Fatalf("Confirmation.Title = %q; want %q", gui.Views.Confirmation.Title, gui.Tr.RegistryExternalCredentialTitle)
	}
	if got := gui.trimmedContent(gui.Views.Confirmation); got != gui.Tr.RegistryExternalCredentialMessage {
		t.Fatalf("Confirmation message = %q; want %q", got, gui.Tr.RegistryExternalCredentialMessage)
	}

	// Connection is saved in registries.yml without SecretOwner
	saved, err := store.Get("ghcr.io")
	if err != nil {
		t.Fatalf("store.Get(ghcr.io): %s", err)
	}
	if saved.SecretOwner != "" {
		t.Fatalf("SecretOwner = %q; want empty (external credential must not be claimed)", saved.SecretOwner)
	}

	// Existing credential in config.json is untouched
	resolved, err := store.ResolveSecret(context.Background(), saved)
	if err != nil {
		t.Fatalf("ResolveSecret: %s", err)
	}
	if resolved.Password != "existing-token" {
		t.Fatalf("resolved password = %q; want %q (pre-existing login must stay intact)", resolved.Password, "existing-token")
	}
}

type logCaptureHook struct {
	mu      sync.Mutex
	entries []string
}

func (h *logCaptureHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *logCaptureHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	line, _ := entry.String()
	h.entries = append(h.entries, entry.Message, line, fmt.Sprintf("%+v", entry.Data))
	return nil
}

func (h *logCaptureHook) contains(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.entries {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Security contract: the secret typed into the masked prompt must never reach
// gui.Log, registries.yml, or remain in the confirmation view buffer.
func TestRegistryCRUDNeverLogsSecret(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	store, configDir, _ := setupTestConnectionStore(t, gui)

	hook := &logCaptureHook{}
	gui.Log.Logger.AddHook(hook)

	const sentinelSecret = "NEVER-LOG-THIS-SECRET-TOKEN-998877"

	// 1. Add connection with sentinelSecret
	if err := gui.handleAddRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleAddRegistryConnection: %s", err)
	}
	if err := gui.handleMenuPress(); err != nil { // Generic registry
		t.Fatalf("handleMenuPress: %s", err)
	}
	setPromptInput(gui.Views.Confirmation, "registry.private.local:5000")
	confirmCurrentPopup(t, gui)
	setPromptInput(gui.Views.Confirmation, "deployer")
	confirmCurrentPopup(t, gui)
	setPromptInput(gui.Views.Confirmation, sentinelSecret)
	confirmCurrentPopup(t, gui)

	// 2. Also trigger a duplicate-host failure path with the same sentinelSecret
	_ = gui.saveNewRegistryConnection(connections.Connection{
		Kind:     connections.KindRegistry,
		URL:      "registry.private.local:5000",
		Username: "deployer",
	}, credentials.Secret{Username: "deployer", Password: sentinelSecret})

	if hook.contains(sentinelSecret) {
		t.Fatal("secret leaked into gui.Log")
	}
	if strings.Contains(gui.Views.Confirmation.Buffer(), sentinelSecret) {
		t.Fatal("secret remained in confirmation view buffer after masked prompt completed")
	}
	ymlBytes, err := os.ReadFile(filepath.Join(configDir, connections.FileName))
	if err != nil {
		t.Fatalf("reading registries.yml: %s", err)
	}
	if strings.Contains(string(ymlBytes), sentinelSecret) {
		t.Fatal("secret leaked into registries.yml")
	}
	_ = store
}

type fakeRepoSource struct {
	fn func(ctx context.Context, item *registryItem) ([]registryRepository, error)
}

func (f *fakeRepoSource) ListRepositories(ctx context.Context, item *registryItem) ([]registryRepository, error) {
	if f.fn != nil {
		return f.fn(ctx, item)
	}
	return nil, nil
}

// L4.1: Enter on a registry drills into repositories, updating the panel title
// with the breadcrumb path ("Registries › <host>"). Esc on the drilled level
// pops back, restoring the previous list and cursor position.
func TestRegistriesDrillPushPopRestoresListAndCursor(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus: %s", err)
	}

	items := []*registryItem{
		{Kind: registryKindRegistry, Host: "docker.io", Username: "user1", Status: registryItemStatusOnline},
		{Kind: registryKindRegistry, Host: "ghcr.io", Username: "user2", Status: registryItemStatusOnline},
		{Kind: registryKindRegistry, Host: "quay.io", Username: "user3", Status: registryItemStatusOnline},
	}
	gui.Panels.Registries.SetItems(items)
	gui.Panels.Registries.SetSelectedLineIdx(1) // Select ghcr.io
	_ = gui.Panels.Registries.HandleSelect()

	if gui.Views.Registries.Title != gui.Tr.RegistriesTitle {
		t.Fatalf("Title = %q; want %q", gui.Views.Registries.Title, gui.Tr.RegistriesTitle)
	}
	if gui.Panels.Registries.SelectedIdx != 1 {
		t.Fatalf("SelectedIdx = %d; want 1", gui.Panels.Registries.SelectedIdx)
	}

	gui.registriesState.repoSource = &fakeRepoSource{
		fn: func(ctx context.Context, item *registryItem) ([]registryRepository, error) {
			if item.Host != "ghcr.io" {
				t.Fatalf("unexpected host %q", item.Host)
			}
			return []registryRepository{
				{Name: "octocat/hello-world", Visibility: "PUBLIC"},
				{Name: "octocat/private-pkg", Visibility: "PRIVATE"},
			}, nil
		},
	}

	// Press Enter to drill into ghcr.io
	if err := gui.handleRegistriesEnter(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesEnter: %s", err)
	}

	// Wait for repositories to be loaded and applied
	waitUntil(t, 2*time.Second, func() bool {
		return gui.Panels.Registries.List.Len() == 2
	}, "repositories not loaded")

	if !gui.isRegistriesDrilledDown() {
		t.Fatal("isRegistriesDrilledDown() = false; want true")
	}
	wantTitle := "Registries › ghcr.io"
	if gui.Views.Registries.Title != wantTitle {
		t.Fatalf("Title = %q; want %q", gui.Views.Registries.Title, wantTitle)
	}

	repo1, err := gui.Panels.Registries.GetSelectedItem()
	if err != nil || repo1.Repository != "octocat/hello-world" {
		t.Fatalf("GetSelectedItem() = %+v, %v; want octocat/hello-world", repo1, err)
	}
	if repo1.Visibility != "PUBLIC" {
		t.Fatalf("Visibility = %q; want PUBLIC", repo1.Visibility)
	}

	// Verify main view summary contains registry, repository, and visibility
	summary := gui.registriesSummaryStr(repo1)
	if !strings.Contains(summary, "ghcr.io") || !strings.Contains(summary, "octocat/hello-world") || !strings.Contains(summary, "PUBLIC") {
		t.Fatalf("summary = %q; missing expected fields", summary)
	}

	// Move cursor to index 1 (octocat/private-pkg)
	gui.Panels.Registries.SetSelectedLineIdx(1)
	_ = gui.Panels.Registries.HandleSelect()
	repo2, err := gui.Panels.Registries.GetSelectedItem()
	if err != nil || repo2.Repository != "octocat/private-pkg" {
		t.Fatalf("GetSelectedItem() = %+v, %v; want octocat/private-pkg", repo2, err)
	}

	// Press Esc to pop back
	if err := gui.handleRegistriesEscape(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesEscape: %s", err)
	}

	if gui.isRegistriesDrilledDown() {
		t.Fatal("isRegistriesDrilledDown() = true; want false after pop")
	}
	if gui.Views.Registries.Title != gui.Tr.RegistriesTitle {
		t.Fatalf("Title = %q; want %q", gui.Views.Registries.Title, gui.Tr.RegistriesTitle)
	}
	if got := gui.Panels.Registries.List.Len(); got != 3 {
		t.Fatalf("List.Len() = %d; want 3", got)
	}
	if gui.Panels.Registries.SelectedIdx != 1 {
		t.Fatalf("SelectedIdx = %d; want 1 (restored)", gui.Panels.Registries.SelectedIdx)
	}
	selectedItem, err := gui.Panels.Registries.GetSelectedItem()
	if err != nil || selectedItem.Host != "ghcr.io" {
		t.Fatalf("GetSelectedItem() = %+v, %v; want ghcr.io", selectedItem, err)
	}
}

// L4.1: A background load that lands after the user has left the level
// (e.g. popped back or navigated away) must be dropped and never clobber
// the current list.
func TestRegistriesDrillDropsStaleResultsForLeftLevel(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus: %s", err)
	}

	items := []*registryItem{
		{Kind: registryKindRegistry, Host: "docker.io", Username: "user1", Status: registryItemStatusOnline},
		{Kind: registryKindRegistry, Host: "ghcr.io", Username: "user2", Status: registryItemStatusOnline},
	}
	gui.Panels.Registries.SetItems(items)

	// Block the fake repo source
	blockCh := make(chan struct{})
	gui.registriesState.repoSource = &fakeRepoSource{
		fn: func(ctx context.Context, item *registryItem) ([]registryRepository, error) {
			<-blockCh
			return []registryRepository{
				{Name: "stale-repo", Visibility: "PUBLIC"},
			}, nil
		},
	}

	// Drill into docker.io (index 0)
	if err := gui.handleRegistriesEnter(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesEnter: %s", err)
	}

	if !gui.isRegistriesDrilledDown() {
		t.Fatal("expected drilled down")
	}

	// User pops back to root level before background load finishes
	if err := gui.handleRegistriesEscape(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesEscape: %s", err)
	}
	if gui.isRegistriesDrilledDown() {
		t.Fatal("expected not drilled down after Esc")
	}

	// Now unblock the background load
	close(blockCh)
	// Give any goroutine a chance to land
	time.Sleep(50 * time.Millisecond)

	// Panel items must STILL be the registries list, NOT the stale repositories!
	if got := gui.Panels.Registries.List.Len(); got != 2 {
		t.Fatalf("List.Len() = %d; want 2 (stale results must be dropped)", got)
	}
	item0, _ := gui.Panels.Registries.List.TryGet(0)
	if item0.Kind != registryKindRegistry || item0.Host != "docker.io" {
		t.Fatalf("item 0 = %+v; want registry docker.io", item0)
	}
}

// L4.1: If a filter is active at a drilled level, pressing Esc must clear the
// filter first; only a second Esc pops the drill level.
func TestRegistriesDrillEscWithFilterClearsFilterFirst(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus: %s", err)
	}

	gui.Panels.Registries.SetItems([]*registryItem{
		{Kind: registryKindRegistry, Host: "ghcr.io", Username: "user", Status: registryItemStatusOnline},
	})
	gui.registriesState.repoSource = &fakeRepoSource{
		fn: func(ctx context.Context, item *registryItem) ([]registryRepository, error) {
			return []registryRepository{
				{Name: "myorg/api-server", Visibility: "PUBLIC"},
				{Name: "myorg/worker", Visibility: "PRIVATE"},
				{Name: "other/docs", Visibility: "PUBLIC"},
			}, nil
		},
	}

	// Enter ghcr.io
	if err := gui.handleRegistriesEnter(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesEnter: %s", err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return gui.Panels.Registries.List.Len() == 3
	}, "repositories not loaded")

	// Activate lazydocker's filter on the panel
	gui.State.Filter.active = true
	gui.State.Filter.needle = "worker"
	gui.State.Filter.panel = gui.Panels.Registries
	gui.Panels.Registries.FilterAndSort()

	if !gui.State.Filter.active {
		t.Fatal("filter should be active")
	}
	if got := gui.Panels.Registries.List.Len(); got != 1 {
		t.Fatalf("filtered list len = %d; want 1", got)
	}

	// First Esc: must clear the filter and NOT pop the level
	if err := gui.handleRegistriesEscape(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("first handleRegistriesEscape: %s", err)
	}

	if gui.State.Filter.active {
		t.Fatal("filter should be inactive after first Esc")
	}
	if !gui.isRegistriesDrilledDown() {
		t.Fatal("isRegistriesDrilledDown() = false; first Esc must only clear the filter, not pop")
	}
	if got := gui.Panels.Registries.List.Len(); got != 3 {
		t.Fatalf("unfiltered list len = %d; want 3", got)
	}
	if gui.Views.Registries.Title != "Registries › ghcr.io" {
		t.Fatalf("Title = %q; want Registries › ghcr.io", gui.Views.Registries.Title)
	}

	// Second Esc: now that filter is inactive, it must pop back to registries
	if err := gui.handleRegistriesEscape(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("second handleRegistriesEscape: %s", err)
	}

	if gui.isRegistriesDrilledDown() {
		t.Fatal("isRegistriesDrilledDown() = true; second Esc must pop the level")
	}
	if gui.Views.Registries.Title != gui.Tr.RegistriesTitle {
		t.Fatalf("Title = %q; want %q", gui.Views.Registries.Title, gui.Tr.RegistriesTitle)
	}
	if got := gui.Panels.Registries.List.Len(); got != 1 {
		t.Fatalf("restored list len = %d; want 1", got)
	}
}

// L4.1: Repository source selection per registry kind:
//   - generic registry -> calls listGeneric
//   - docker.io -> calls listDockerHub with namespace = username (or anonymous if none)
//   - ghcr.io -> calls listGHCR with owner = username
func TestRegistriesSourceSelectionPerKind(t *testing.T) {
	ctx := context.Background()

	t.Run("generic registry", func(t *testing.T) {
		var calledGeneric bool
		src := &defaultRepositorySource{
			resolveCredential: func(ctx context.Context, item *registryItem) (*registry.Credential, error) {
				return &registry.Credential{Username: "u", Password: "p"}, nil
			},
			listGeneric: func(ctx context.Context, host string, opts registry.Options) ([]registryRepository, error) {
				calledGeneric = true
				if host != "registry.example.com" {
					t.Fatalf("host = %q; want registry.example.com", host)
				}
				if opts.Credential == nil || opts.Credential.Username != "u" {
					t.Fatalf("opts.Credential = %+v; want username u", opts.Credential)
				}
				return []registryRepository{{Name: "repo1", Visibility: ""}}, nil
			},
		}

		item := &registryItem{
			Kind: registryKindRegistry,
			Host: "registry.example.com",
		}
		repos, err := src.ListRepositories(ctx, item)
		if err != nil {
			t.Fatalf("ListRepositories: %s", err)
		}
		if !calledGeneric {
			t.Fatal("listGeneric was not called")
		}
		if len(repos) != 1 || repos[0].Name != "repo1" || repos[0].Visibility != "" {
			t.Fatalf("unexpected repos: %+v", repos)
		}
	})

	t.Run("docker.io with username", func(t *testing.T) {
		var calledHub bool
		src := &defaultRepositorySource{
			resolveCredential: func(ctx context.Context, item *registryItem) (*registry.Credential, error) {
				return &registry.Credential{Username: "myuser", Password: "pw"}, nil
			},
			listDockerHub: func(ctx context.Context, opts dockerhub.Options, namespace string) ([]registryRepository, error) {
				calledHub = true
				if namespace != "myuser" {
					t.Fatalf("namespace = %q; want myuser", namespace)
				}
				if opts.Credential == nil || opts.Credential.Password != "pw" {
					t.Fatalf("opts.Credential = %+v; want pw", opts.Credential)
				}
				return []registryRepository{{Name: "hub-repo", Visibility: "PUBLIC"}}, nil
			},
		}

		item := &registryItem{
			Kind:     registryKindRegistry,
			Host:     "docker.io",
			Username: "myuser",
		}
		repos, err := src.ListRepositories(ctx, item)
		if err != nil {
			t.Fatalf("ListRepositories: %s", err)
		}
		if !calledHub {
			t.Fatal("listDockerHub was not called")
		}
		if len(repos) != 1 || repos[0].Name != "hub-repo" || repos[0].Visibility != "PUBLIC" {
			t.Fatalf("unexpected repos: %+v", repos)
		}
	})

	t.Run("docker.io anonymous without username falls back to library", func(t *testing.T) {
		var calledHub bool
		src := &defaultRepositorySource{
			resolveCredential: func(ctx context.Context, item *registryItem) (*registry.Credential, error) {
				return nil, nil // anonymous
			},
			listDockerHub: func(ctx context.Context, opts dockerhub.Options, namespace string) ([]registryRepository, error) {
				calledHub = true
				if namespace != "library" {
					t.Fatalf("namespace = %q; want library for anonymous hub", namespace)
				}
				if opts.Credential != nil {
					t.Fatalf("opts.Credential = %+v; want nil for anonymous", opts.Credential)
				}
				return []registryRepository{{Name: "ubuntu", Visibility: "PUBLIC"}}, nil
			},
		}

		item := &registryItem{
			Kind: registryKindRegistry,
			Host: "docker.io",
		}
		repos, err := src.ListRepositories(ctx, item)
		if err != nil {
			t.Fatalf("ListRepositories: %s", err)
		}
		if !calledHub {
			t.Fatal("listDockerHub was not called")
		}
		if len(repos) != 1 || repos[0].Name != "ubuntu" {
			t.Fatalf("unexpected repos: %+v", repos)
		}
	})

	t.Run("ghcr.io with owner", func(t *testing.T) {
		var calledGHCR bool
		src := &defaultRepositorySource{
			resolveCredential: func(ctx context.Context, item *registryItem) (*registry.Credential, error) {
				return &registry.Credential{Username: "myorg", Password: "ghp_secret"}, nil
			},
			listGHCR: func(ctx context.Context, opts ghcr.Options, owner string) ([]registryRepository, error) {
				calledGHCR = true
				if owner != "myorg" {
					t.Fatalf("owner = %q; want myorg", owner)
				}
				if opts.Credential == nil || opts.Credential.Password != "ghp_secret" {
					t.Fatalf("opts.Credential = %+v; want secret", opts.Credential)
				}
				return []registryRepository{{Name: "myorg/pkg", Visibility: "PRIVATE"}}, nil
			},
		}

		item := &registryItem{
			Kind:     registryKindRegistry,
			Host:     "ghcr.io",
			Username: "myorg",
		}
		repos, err := src.ListRepositories(ctx, item)
		if err != nil {
			t.Fatalf("ListRepositories: %s", err)
		}
		if !calledGHCR {
			t.Fatal("listGHCR was not called")
		}
		if len(repos) != 1 || repos[0].Name != "myorg/pkg" || repos[0].Visibility != "PRIVATE" {
			t.Fatalf("unexpected repos: %+v", repos)
		}
	})
}

// L4.1: The n/e/d connection keys only act at the registry level; at the repository level
// they are inert (do nothing).
func TestRegistriesConnectionCRUDInertAtRepositoryLevel(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus: %s", err)
	}

	gui.Panels.Registries.SetItems([]*registryItem{
		{Kind: registryKindRegistry, Host: "docker.io", Username: "user", Status: registryItemStatusOnline},
	})
	gui.registriesState.repoSource = &fakeRepoSource{
		fn: func(ctx context.Context, item *registryItem) ([]registryRepository, error) {
			return []registryRepository{
				{Name: "myuser/repo", Visibility: "PUBLIC"},
			}, nil
		},
	}

	// Drill into docker.io
	if err := gui.handleRegistriesEnter(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesEnter: %s", err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return gui.Panels.Registries.List.Len() == 1
	}, "repositories not loaded")

	if !gui.isRegistriesDrilledDown() {
		t.Fatal("expected drilled down")
	}

	// 1. 'n' (Add) must be inert
	if err := gui.handleAddRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleAddRegistryConnection: %s", err)
	}
	if gui.Panels.Menu.List.Len() > 0 {
		t.Fatalf("menu opened for Add at repository level: Len=%d", gui.Panels.Menu.List.Len())
	}

	// 2. 'e' (Edit) must be inert
	if err := gui.handleEditRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleEditRegistryConnection: %s", err)
	}
	if gui.Views.Confirmation.Title != "" {
		t.Fatalf("confirmation view opened for Edit at repository level: %q", gui.Views.Confirmation.Title)
	}

	// 3. 'd' (Delete) must be inert
	if err := gui.handleDeleteRegistryConnection(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleDeleteRegistryConnection: %s", err)
	}
	if gui.Views.Confirmation.Title != "" {
		t.Fatalf("confirmation view opened for Delete at repository level: %q", gui.Views.Confirmation.Title)
	}
}

// L4.1: Errors from listing render in the main view for that level (secret-free messages).
func TestRegistriesDrillListingErrorRendersInMainView(t *testing.T) {
	gui := newHeadlessRegistriesTestGui(t)
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus: %s", err)
	}

	gui.Panels.Registries.SetItems([]*registryItem{
		{Kind: registryKindRegistry, Host: "ghcr.io", Username: "user", Status: registryItemStatusOnline},
	})
	gui.registriesState.repoSource = &fakeRepoSource{
		fn: func(ctx context.Context, item *registryItem) ([]registryRepository, error) {
			return nil, errors.New("ghcr: unauthorized to list packages")
		},
	}

	if err := gui.handleRegistriesEnter(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesEnter: %s", err)
	}

	waitUntil(t, 2*time.Second, func() bool {
		level := gui.registriesState.currentDrillLevel()
		return level != nil && level.err != ""
	}, "error not recorded on level")

	level := gui.registriesState.currentDrillLevel()
	if level.err != "ghcr: unauthorized to list packages" {
		t.Fatalf("level.err = %q; want unauthorized error", level.err)
	}
	if gui.Panels.Registries.NoItemsMessage != "ghcr: unauthorized to list packages" {
		t.Fatalf("NoItemsMessage = %q; want unauthorized error", gui.Panels.Registries.NoItemsMessage)
	}
}
