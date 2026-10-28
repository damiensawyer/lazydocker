package gui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazydocker/pkg/commands"
	"github.com/jesseduffield/lazydocker/pkg/config"
	"github.com/jesseduffield/lazydocker/pkg/i18n"
	"github.com/jesseduffield/lazydocker/pkg/registry"
	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
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
		},
	}
	gui.setPanels()
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

	var discoverCalls, pingCalls atomic.Int64
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

	if got := discoverCalls.Load(); got != 0 {
		t.Fatalf("Discover called %d time(s) before the panel was focused; want 0 (D-09)", got)
	}
	if got := pingCalls.Load(); got != 0 {
		t.Fatalf("ping called %d time(s) before the panel was focused; want 0 (D-09)", got)
	}

	// first focus: one discovery, then one ping per auth source
	if err := gui.switchFocus(gui.Views.Registries); err != nil {
		t.Fatalf("switchFocus to registries: %s", err)
	}
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
	if got := discoverCalls.Load(); got != 1 {
		t.Fatalf("refocus re-ran discovery (%d calls); only the refresh key should", got)
	}

	// pressing 'r' (handleRegistriesRefresh) must re-run discovery and ping
	if err := gui.handleRegistriesRefresh(gui.g, gui.Views.Registries); err != nil {
		t.Fatalf("handleRegistriesRefresh: %s", err)
	}
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

	var hasFocusKey, hasRefreshKey bool
	for _, b := range gui.GetInitialKeybindings() {
		if b.Key == '7' && b.Description == gui.Tr.FocusRegistries {
			hasFocusKey = true
		}
		if b.ViewName == "registries" && b.Key == 'r' && b.Description == gui.Tr.RefreshRegistries {
			hasRefreshKey = true
		}
	}
	if !hasFocusKey {
		t.Fatal("no '7' binding to focus the registries panel")
	}
	if !hasRefreshKey {
		t.Fatal("no 'r' refresh binding on the registries panel")
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
