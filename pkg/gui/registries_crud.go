package gui

import (
	"errors"
	"strings"

	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazydocker/pkg/gui/types"
	"github.com/jesseduffield/lazydocker/pkg/registry/connections"
	"github.com/jesseduffield/lazydocker/pkg/registry/credentials"
	"github.com/jesseduffield/lazydocker/pkg/utils"
)

// Connection CRUD flows for the Registries side panel (L3.2, D-06, S-02, S-04):
//   - 'n' (handleAddRegistryConnection): menu for kind -> URL prompt ->
//     username prompt -> masked secret prompt -> Store.Create
//   - 'e' (handleEditRegistryConnection): same chain prefilled for a saved
//     row (empty secret keeps the existing credential) -> Store.Update
//   - 'd' (handleDeleteRegistryConnection): confirmation panel -> Store.Delete
//     (refused for auto-detected rows)
//
// Secrets are read raw from the masked prompt buffer (preserving spaces,
// stripping only a trailing line ending), passed straight to Store.Create /
// Store.Update, and immediately dropped. A secret is never logged or placed
// in an error message.

func defaultURLForKind(kind connections.Kind) string {
	switch kind {
	case connections.KindDockerHub:
		return "docker.io"
	case connections.KindGHCR:
		return "ghcr.io"
	default:
		return ""
	}
}

// rawPromptContent reads the confirmation view's buffer without trimming
// leading or trailing whitespace (unlike gui.trimmedContent), stripping only a
// trailing line ending (\r\n or \n) added by gocui's line buffer.
func rawPromptContent(v *gocui.View) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSuffix(v.Buffer(), "\n")
	return strings.TrimSuffix(s, "\r")
}

func (gui *Gui) showRegistryToast(message string) {
	if gui.statusManager != nil && gui.Views.AppStatus != nil {
		gui.showToast(message)
	}
}

func (gui *Gui) handleAddRegistryConnection(g *gocui.Gui, v *gocui.View) error {
	if gui.isRegistriesDrilledDown() {
		return nil
	}
	return gui.openRegistryKindMenu(connections.KindRegistry, func(kind connections.Kind) error {
		initialURL := defaultURLForKind(kind)
		return gui.promptRegistryConnectionChain(
			kind,
			initialURL,
			"",
			gui.Tr.RegistrySecretPromptTitle,
			false,
			func(conn connections.Connection, secret credentials.Secret) error {
				return gui.saveNewRegistryConnection(conn, secret)
			},
		)
	})
}

func (gui *Gui) handleEditRegistryConnection(g *gocui.Gui, v *gocui.View) error {
	if gui.Panels.Registries == nil || gui.isRegistriesDrilledDown() {
		return nil
	}
	item, err := gui.Panels.Registries.GetSelectedItem()
	if err != nil || item == nil || item.Kind != registryKindRegistry {
		return nil
	}
	if item.AutoDetected {
		gui.showRegistryToast(gui.Tr.CannotEditAutoDetectedRegistry)
		return gui.createErrorPanel(gui.Tr.CannotEditAutoDetectedRegistry)
	}

	existing := item.Connection
	if !existing.Kind.Valid() {
		existing.Kind = connections.KindRegistry
	}
	if existing.URL == "" {
		existing.URL = item.Host
	}
	if existing.Username == "" {
		existing.Username = item.Username
	}
	previousURL := existing.URL

	return gui.openRegistryKindMenu(existing.Kind, func(chosenKind connections.Kind) error {
		prefillURL := existing.URL
		if chosenKind != existing.Kind {
			if def := defaultURLForKind(chosenKind); def != "" {
				prefillURL = def
			}
		}
		return gui.promptRegistryConnectionChain(
			chosenKind,
			prefillURL,
			existing.Username,
			gui.Tr.RegistrySecretEditPromptTitle,
			existing.Insecure,
			func(conn connections.Connection, secret credentials.Secret) error {
				return gui.saveUpdatedRegistryConnection(previousURL, conn, secret)
			},
		)
	})
}

func (gui *Gui) handleDeleteRegistryConnection(g *gocui.Gui, v *gocui.View) error {
	if gui.Panels.Registries == nil || gui.isRegistriesDrilledDown() {
		return nil
	}
	item, err := gui.Panels.Registries.GetSelectedItem()
	if err != nil || item == nil || item.Kind != registryKindRegistry {
		return nil
	}
	if item.AutoDetected {
		gui.showRegistryToast(gui.Tr.CannotDeleteAutoDetectedRegistry)
		return gui.createErrorPanel(gui.Tr.CannotDeleteAutoDetectedRegistry)
	}

	target := item.Connection.URL
	if target == "" {
		target = item.Host
	}
	prompt := utils.ResolvePlaceholderString(
		gui.Tr.ConfirmDeleteRegistryConnection,
		map[string]string{"registry": item.Host},
	)
	return gui.createConfirmationPanel(
		gui.Tr.DeleteRegistryConnectionTitle,
		prompt,
		func(g *gocui.Gui, v *gocui.View) error {
			if err := gui.registriesStore().Delete(target); err != nil {
				return gui.createErrorPanel(err.Error())
			}
			gui.refreshRegistries()
			return nil
		},
		nil,
	)
}

func (gui *Gui) openRegistryKindMenu(selectedKind connections.Kind, onSelect func(connections.Kind) error) error {
	kinds := []struct {
		kind  connections.Kind
		label string
	}{
		{kind: connections.KindRegistry, label: gui.Tr.RegistryKindGeneric},
		{kind: connections.KindDockerHub, label: gui.Tr.RegistryKindDockerHub},
		{kind: connections.KindGHCR, label: gui.Tr.RegistryKindGHCR},
	}

	selectedIdx := 0
	menuItems := make([]*types.MenuItem, 0, len(kinds))
	for i, entry := range kinds {
		k := entry.kind
		if k == selectedKind {
			selectedIdx = i
		}
		menuItems = append(menuItems, &types.MenuItem{
			LabelColumns: []string{entry.label},
			OnPress: func() error {
				return onSelect(k)
			},
		})
	}

	if err := gui.Menu(CreateMenuOptions{
		Title: gui.Tr.RegistryKindMenuTitle,
		Items: menuItems,
	}); err != nil {
		return err
	}
	if selectedIdx > 0 {
		gui.Panels.Menu.SetSelectedLineIdx(selectedIdx)
		_ = gui.Panels.Menu.RerenderList()
	}
	return nil
}

func (gui *Gui) promptRegistryConnectionChain(
	kind connections.Kind,
	initialURL string,
	initialUsername string,
	secretPromptTitle string,
	insecure bool,
	onComplete func(connections.Connection, credentials.Secret) error,
) error {
	return gui.createPrefilledPromptPanel(gui.Tr.RegistryURLPromptTitle, initialURL, func(g *gocui.Gui, v *gocui.View) error {
		rawURL := gui.trimmedContent(v)
		if rawURL == "" {
			rawURL = defaultURLForKind(kind)
		}
		if rawURL == "" || credentials.NormalizeRegistry(rawURL) == "" {
			return gui.createErrorPanel(gui.Tr.RegistryEmptyURLError)
		}

		return gui.createPrefilledPromptPanel(gui.Tr.RegistryUsernamePromptTitle, initialUsername, func(g *gocui.Gui, v *gocui.View) error {
			username := gui.trimmedContent(v)

			return gui.createMaskedPromptPanel(secretPromptTitle, func(g *gocui.Gui, v *gocui.View) error {
				rawSecret := rawPromptContent(v)
				// Immediately wipe the confirmation buffer so the secret does
				// not linger in memory even if the next panel is non-editable.
				v.ClearTextArea()

				conn := connections.Connection{
					Kind:     kind,
					URL:      rawURL,
					Username: username,
					Insecure: insecure,
				}
				var secret credentials.Secret
				if rawSecret != "" {
					secret = credentials.Secret{
						Username: username,
						Password: rawSecret,
					}
				}
				rawSecret = ""

				err := onComplete(conn, secret)
				secret = credentials.Secret{}
				return err
			})
		})
	})
}

func (gui *Gui) saveNewRegistryConnection(conn connections.Connection, secret credentials.Secret) error {
	store := gui.registriesStore()
	err := store.Create(conn, secret)
	secret = credentials.Secret{}

	if errors.Is(err, connections.ErrExternalCredentialExists) {
		if retryErr := store.Create(conn, credentials.Secret{}); retryErr != nil {
			return gui.createErrorPanel(retryErr.Error())
		}
		gui.refreshRegistries()
		gui.showRegistryToast(gui.Tr.RegistryExternalCredentialMessage)
		return gui.createConfirmationPanel(
			gui.Tr.RegistryExternalCredentialTitle,
			gui.Tr.RegistryExternalCredentialMessage,
			nil,
			nil,
		)
	}
	if err != nil {
		return gui.createErrorPanel(err.Error())
	}

	gui.refreshRegistries()
	return nil
}

func (gui *Gui) saveUpdatedRegistryConnection(previousURL string, conn connections.Connection, secret credentials.Secret) error {
	store := gui.registriesStore()
	err := store.Update(previousURL, conn, secret)
	secret = credentials.Secret{}

	if errors.Is(err, connections.ErrExternalCredentialExists) {
		if retryErr := store.Update(previousURL, conn, credentials.Secret{}); retryErr != nil {
			return gui.createErrorPanel(retryErr.Error())
		}
		gui.refreshRegistries()
		gui.showRegistryToast(gui.Tr.RegistryExternalCredentialMessage)
		return gui.createConfirmationPanel(
			gui.Tr.RegistryExternalCredentialTitle,
			gui.Tr.RegistryExternalCredentialMessage,
			nil,
			nil,
		)
	}
	if err != nil {
		return gui.createErrorPanel(err.Error())
	}

	gui.refreshRegistries()
	return nil
}
