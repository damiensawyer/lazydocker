package i18n

// TranslationSet is a set of localised strings for a given language
type TranslationSet struct {
	NotEnoughSpace                             string
	ProjectTitle                               string
	MainTitle                                  string
	GlobalTitle                                string
	Navigate                                   string
	Menu                                       string
	MenuTitle                                  string
	Execute                                    string
	Scroll                                     string
	Close                                      string
	Quit                                       string
	ErrorTitle                                 string
	NoViewMachingNewLineFocusedSwitchStatement string
	OpenConfig                                 string
	EditConfig                                 string
	ConfirmQuit                                string
	ConfirmUpProject                           string
	ErrorOccurred                              string
	ConnectionFailed                           string
	UnattachableContainerError                 string
	WaitingForContainerInfo                    string
	CannotAttachStoppedContainerError          string
	CannotAccessDockerSocketError              string
	CannotKillChildError                       string

	Donate                      string
	Cancel                      string
	CustomCommandTitle          string
	BulkCommandTitle            string
	Remove                      string
	HideStopped                 string
	ForceRemove                 string
	RemoveWithVolumes           string
	MustForceToRemoveContainer  string
	Confirm                     string
	Return                      string
	FocusMain                   string
	LcFilter                    string
	StopContainer               string
	RestartingStatus            string
	StartingStatus              string
	StoppingStatus              string
	UppingProjectStatus         string
	UppingServiceStatus         string
	PausingStatus               string
	RemovingStatus              string
	DowningStatus               string
	RunningCustomCommandStatus  string
	RunningBulkCommandStatus    string
	RemoveService               string
	UpService                   string
	Stop                        string
	Pause                       string
	Restart                     string
	Down                        string
	DownWithVolumes             string
	Start                       string
	Rebuild                     string
	Recreate                    string
	PreviousContext             string
	NextContext                 string
	Attach                      string
	ViewLogs                    string
	UpProject                   string
	DownProject                 string
	ServicesTitle               string
	ContainersTitle             string
	StandaloneContainersTitle   string
	TopTitle                    string
	ImagesTitle                 string
	VolumesTitle                string
	NetworksTitle               string
	NoContainers                string
	NoContainer                 string
	NoImages                    string
	NoVolumes                   string
	NoNetworks                  string
	NoServices                  string
	RemoveImage                 string
	RemoveVolume                string
	RemoveNetwork               string
	RemoveWithoutPrune          string
	RemoveWithoutPruneWithForce string
	RemoveWithForce             string
	PruneImages                 string
	PruneContainers             string
	PruneVolumes                string
	PruneNetworks               string
	ConfirmPruneContainers      string
	ConfirmStopContainers       string
	ConfirmRemoveContainers     string
	ConfirmPruneImages          string
	ConfirmPruneVolumes         string
	ConfirmPruneNetworks        string
	PruningStatus               string
	StopService                 string
	PressEnterToReturn          string
	DetachFromContainerShortCut string
	StopAllContainers           string
	RemoveAllContainers         string
	ViewRestartOptions          string
	ExecShell                   string
	RunCustomCommand            string
	ViewBulkCommands            string
	FilterList                  string
	OpenInBrowser               string
	SortContainersByState       string

	LogsTitle                   string
	ConfigTitle                 string
	EnvTitle                    string
	DockerComposeConfigTitle    string
	StatsTitle                  string
	CreditsTitle                string
	ContainerConfigTitle        string
	ContainerEnvTitle           string
	NothingToDisplay            string
	NoContainerForService       string
	CannotDisplayEnvVariables   string
	CannotManageNonLocalService string

	No  string
	Yes string

	LcNextScreenMode         string
	LcPrevScreenMode         string
	LcDecreaseSidePanelWidth string
	LcIncreaseSidePanelWidth string
	LcResetSidePanelWidth    string
	SidePanelWidthChanged    string
	SidePanelWidthReset      string
	FilterPrompt             string

	FocusProjects   string
	FocusServices   string
	FocusContainers string
	FocusImages     string
	FocusVolumes    string
	FocusNetworks   string

	// L3.1 — Registries side panel
	RegistriesTitle               string
	NoRegistries                  string
	FocusRegistries               string
	RefreshRegistries             string
	RegistriesSummaryTitle        string
	RegistriesSummaryRegistry     string
	RegistriesSummaryUsername     string
	RegistriesSummarySourceFile   string
	RegistriesSummaryStatus       string
	RegistriesSummaryResponseTime string
	RegistriesAutoDetectedSuffix  string
	RegistriesStatusChecking      string
	RegistriesStatusOnline        string
	RegistriesStatusOffline       string
	RegistriesStatusUnauthorized  string

	// L3.2 — Registry connection CRUD flows
	AddRegistryConnection             string
	EditRegistryConnection            string
	DeleteRegistryConnection          string
	RegistryKindMenuTitle             string
	RegistryKindGeneric               string
	RegistryKindDockerHub             string
	RegistryKindGHCR                  string
	RegistryURLPromptTitle            string
	RegistryUsernamePromptTitle       string
	RegistrySecretPromptTitle         string
	RegistrySecretEditPromptTitle     string
	RegistryEmptyURLError             string
	DeleteRegistryConnectionTitle     string
	ConfirmDeleteRegistryConnection   string
	CannotDeleteAutoDetectedRegistry  string
	CannotEditAutoDetectedRegistry    string
	RegistryExternalCredentialTitle   string
	RegistryExternalCredentialMessage string

	// L5.1 — Tag details renderers
	TagSummaryTag                string
	TagSummaryDigest             string
	TagSummaryMediaType          string
	TagSummaryIndex              string
	TagSummaryPlatforms          string
	TagSummarySelectedPlatform   string
	TagSummaryTotalSize          string
	TagSummaryLayerCount         string
	TagSummaryCreated            string
	TagSummaryNoPlatformSelected string
	TagSelectPlatformPrompt      string
	TagSelectPlatform            string
	TagLayersHeaderIndex         string
	TagLayersHeaderMediaType     string
	TagLayersHeaderSize          string
	TagLayersHeaderDigest        string
	TagNoLayers                  string
	TagHistoryHeaderCreated      string
	TagHistoryHeaderCommand      string
	TagHistoryHeaderEmpty        string
	TagHistoryHeaderComment      string
	TagHistoryEmptyMarker        string
	TagNoHistory                 string
	TagConfigUser                string
	TagConfigWorkingDir          string
	TagConfigEntrypoint          string
	TagConfigCmd                 string
	TagConfigEnv                 string
	TagNoConfig                  string
	TagNoLabels                  string
	TagRootFSType                string
	TagRootFSDiffIDs             string
	TagNoRootFS                  string
}

func englishSet() TranslationSet {
	return TranslationSet{
		PruningStatus:              "pruning",
		RemovingStatus:             "removing",
		RestartingStatus:           "restarting",
		StartingStatus:             "starting",
		StoppingStatus:             "stopping",
		UppingServiceStatus:        "upping service",
		UppingProjectStatus:        "upping project",
		DowningStatus:              "downing",
		PausingStatus:              "pausing",
		RunningCustomCommandStatus: "running custom command",
		RunningBulkCommandStatus:   "running bulk command",

		NoViewMachingNewLineFocusedSwitchStatement: "No view matching newLineFocused switch statement",

		ErrorOccurred:                     "An error occurred! Please create an issue at https://github.com/jesseduffield/lazydocker/issues",
		ConnectionFailed:                  "connection to docker client failed. You may need to restart the docker client",
		UnattachableContainerError:        "Container does not support attaching. You must either run the service with the '-it' flag or use `stdin_open: true, tty: true` in the docker-compose.yml file",
		WaitingForContainerInfo:           "Cannot proceed until docker gives us more information about the container. Please retry in a few moments.",
		CannotAttachStoppedContainerError: "You cannot attach to a stopped container, you need to start it first (which you can actually do with the 'r' key) (yes I'm too lazy to do this automatically for you) (pretty cool that I get to communicate one-on-one with you in the form of an error message though)",
		CannotAccessDockerSocketError:     "Can't access docker socket at: unix:///var/run/docker.sock\nRun lazydocker as root or read https://docs.docker.com/install/linux/linux-postinstall/",
		CannotKillChildError:              "Waited three seconds for child process to stop. There may be an orphan process that continues to run on your system.",

		Donate:  "Donate",
		Confirm: "Confirm",

		Return:                      "return",
		FocusMain:                   "focus main panel",
		LcFilter:                    "filter list",
		Navigate:                    "navigate",
		Execute:                     "execute",
		Close:                       "close",
		Quit:                        "quit",
		Menu:                        "menu",
		MenuTitle:                   "Menu",
		Scroll:                      "scroll",
		OpenConfig:                  "open lazydocker config",
		EditConfig:                  "edit lazydocker config",
		Cancel:                      "cancel",
		Remove:                      "remove",
		HideStopped:                 "hide/show stopped containers",
		ForceRemove:                 "force remove",
		RemoveWithVolumes:           "remove with volumes",
		RemoveService:               "remove containers",
		UpService:                   "up service",
		Stop:                        "stop",
		Pause:                       "pause",
		Restart:                     "restart",
		Down:                        "down project",
		DownWithVolumes:             "down project with volumes",
		Start:                       "start",
		Rebuild:                     "rebuild",
		Recreate:                    "recreate",
		PreviousContext:             "previous tab",
		NextContext:                 "next tab",
		Attach:                      "attach",
		ViewLogs:                    "view logs",
		UpProject:                   "up project",
		DownProject:                 "down project",
		RemoveImage:                 "remove image",
		RemoveVolume:                "remove volume",
		RemoveNetwork:               "remove network",
		RemoveWithoutPrune:          "remove without deleting untagged parents",
		RemoveWithoutPruneWithForce: "remove (forced) without deleting untagged parents",
		RemoveWithForce:             "remove (forced)",
		PruneContainers:             "prune exited containers",
		PruneVolumes:                "prune unused volumes",
		PruneNetworks:               "prune unused networks",
		PruneImages:                 "prune unused images",
		StopAllContainers:           "stop all containers",
		RemoveAllContainers:         "remove all containers (forced)",
		ViewRestartOptions:          "view restart options",
		ExecShell:                   "exec shell",
		RunCustomCommand:            "run predefined custom command",
		ViewBulkCommands:            "view bulk commands",
		FilterList:                  "filter list",
		OpenInBrowser:               "open in browser (first port is http)",
		SortContainersByState:       "sort containers by state",

		GlobalTitle:                 "Global",
		MainTitle:                   "Main",
		ProjectTitle:                "Project",
		ServicesTitle:               "Services",
		ContainersTitle:             "Containers",
		StandaloneContainersTitle:   "Standalone Containers",
		ImagesTitle:                 "Images",
		VolumesTitle:                "Volumes",
		NetworksTitle:               "Networks",
		CustomCommandTitle:          "Custom Command:",
		BulkCommandTitle:            "Bulk Command:",
		ErrorTitle:                  "Error",
		LogsTitle:                   "Logs",
		ConfigTitle:                 "Config",
		EnvTitle:                    "Env",
		DockerComposeConfigTitle:    "Docker-Compose Config",
		TopTitle:                    "Top",
		StatsTitle:                  "Stats",
		CreditsTitle:                "About",
		ContainerConfigTitle:        "Container Config",
		ContainerEnvTitle:           "Container Env",
		NothingToDisplay:            "Nothing to display",
		NoContainerForService:       "No logs to show; service is not associated with a container",
		CannotDisplayEnvVariables:   "Something went wrong while displaying environment variables",
		CannotManageNonLocalService: "This service belongs to a different compose project. Run lazydocker from that project's directory to manage it.",

		NoContainers: "No containers",
		NoContainer:  "No container",
		NoImages:     "No images",
		NoVolumes:    "No volumes",
		NoNetworks:   "No networks",
		NoServices:   "No services",

		ConfirmQuit:                 "Are you sure you want to quit?",
		ConfirmUpProject:            "Are you sure you want to 'up' your docker compose project?",
		MustForceToRemoveContainer:  "You cannot remove a running container unless you force it. Do you want to force it?",
		NotEnoughSpace:              "Not enough space to render panels",
		ConfirmPruneImages:          "Are you sure you want to prune all unused images?",
		ConfirmPruneContainers:      "Are you sure you want to prune all stopped containers?",
		ConfirmStopContainers:       "Are you sure you want to stop all containers?",
		ConfirmRemoveContainers:     "Are you sure you want to remove all containers?",
		ConfirmPruneVolumes:         "Are you sure you want to prune all unused volumes?",
		ConfirmPruneNetworks:        "Are you sure you want to prune all unused networks?",
		StopService:                 "Are you sure you want to stop this service's containers?",
		StopContainer:               "Are you sure you want to stop this container?",
		PressEnterToReturn:          "Press enter to return to lazydocker (this prompt can be disabled in your config by setting `gui.returnImmediately: true`)",
		DetachFromContainerShortCut: "By default, to detach from the container press ctrl-p then ctrl-q",

		No:  "no",
		Yes: "yes",

		LcNextScreenMode:         "next screen mode (normal/half/fullscreen)",
		LcPrevScreenMode:         "prev screen mode",
		LcDecreaseSidePanelWidth: "decrease side panel width",
		LcIncreaseSidePanelWidth: "increase side panel width",
		LcResetSidePanelWidth:    "reset side panel width",
		SidePanelWidthChanged:    "side panel width: {{width}}",
		SidePanelWidthReset:      "side panel width reset",
		FilterPrompt:             "filter",

		FocusProjects:   "focus projects panel",
		FocusServices:   "focus services panel",
		FocusContainers: "focus containers panel",
		FocusImages:     "focus images panel",
		FocusVolumes:    "focus volumes panel",
		FocusNetworks:   "focus networks panel",

		// L3.1 — Registries side panel
		RegistriesTitle:               "Registries",
		NoRegistries:                  "No registries",
		FocusRegistries:               "focus registries panel",
		RefreshRegistries:             "refresh registries",
		RegistriesSummaryTitle:        "Summary",
		RegistriesSummaryRegistry:     "Registry:",
		RegistriesSummaryUsername:     "Username:",
		RegistriesSummarySourceFile:   "Source file:",
		RegistriesSummaryStatus:       "Status:",
		RegistriesSummaryResponseTime: "Response time:",
		RegistriesAutoDetectedSuffix:  "(auto)",
		RegistriesStatusChecking:      "checking",
		RegistriesStatusOnline:        "online",
		RegistriesStatusOffline:       "offline",
		RegistriesStatusUnauthorized:  "unauthorized",

		// L3.2 — Registry connection CRUD flows
		AddRegistryConnection:             "add registry connection",
		EditRegistryConnection:            "edit registry connection",
		DeleteRegistryConnection:          "delete registry connection",
		RegistryKindMenuTitle:             "Registry Type",
		RegistryKindGeneric:               "Generic Registry (OCI / Docker v2)",
		RegistryKindDockerHub:             "Docker Hub (docker.io)",
		RegistryKindGHCR:                  "GitHub Container Registry (ghcr.io)",
		RegistryURLPromptTitle:            "Registry URL:",
		RegistryUsernamePromptTitle:       "Username (optional):",
		RegistrySecretPromptTitle:         "Password / token (optional):",
		RegistrySecretEditPromptTitle:     "Password / token (leave empty to keep existing):",
		RegistryEmptyURLError:             "Registry URL cannot be empty.",
		DeleteRegistryConnectionTitle:     "Delete Registry Connection",
		ConfirmDeleteRegistryConnection:   "Are you sure you want to delete the saved connection for {{registry}}?",
		CannotDeleteAutoDetectedRegistry:  "Auto-detected registry logins come from your Docker/Podman config and cannot be deleted here.",
		CannotEditAutoDetectedRegistry:    "Auto-detected registry logins come from your Docker/Podman config and cannot be edited here.",
		RegistryExternalCredentialTitle:   "Existing Login Detected",
		RegistryExternalCredentialMessage: "An existing Docker login was found for this registry and will be used as-is.",

		// L5.1 — Tag details renderers
		TagSummaryTag:                "Tag:",
		TagSummaryDigest:             "Digest:",
		TagSummaryMediaType:          "Media type:",
		TagSummaryIndex:              "Index:",
		TagSummaryPlatforms:          "Platforms:",
		TagSummarySelectedPlatform:   "Selected platform:",
		TagSummaryTotalSize:          "Total size:",
		TagSummaryLayerCount:         "Layers:",
		TagSummaryCreated:            "Created:",
		TagSummaryNoPlatformSelected: "(none - select a platform)",
		TagSelectPlatformPrompt:      "Select a platform to view details.",
		TagSelectPlatform:            "Select a platform to view details.",
		TagLayersHeaderIndex:         "#",
		TagLayersHeaderMediaType:     "MEDIA TYPE",
		TagLayersHeaderSize:          "SIZE",
		TagLayersHeaderDigest:        "DIGEST",
		TagNoLayers:                  "No layers available",
		TagHistoryHeaderCreated:      "CREATED",
		TagHistoryHeaderCommand:      "COMMAND",
		TagHistoryHeaderEmpty:        "EMPTY",
		TagHistoryHeaderComment:      "COMMENT",
		TagHistoryEmptyMarker:        "<empty>",
		TagNoHistory:                 "No history available",
		TagConfigUser:                "User:",
		TagConfigWorkingDir:          "Working dir:",
		TagConfigEntrypoint:          "Entrypoint:",
		TagConfigCmd:                 "Cmd:",
		TagConfigEnv:                 "Env:",
		TagNoConfig:                  "No configuration available",
		TagNoLabels:                  "No labels",
		TagRootFSType:                "Type:",
		TagRootFSDiffIDs:             "Diff IDs:",
		TagNoRootFS:                  "No RootFS information available",
	}
}

// EnglishSet returns a copy of the default English translation set.
func EnglishSet() *TranslationSet {
	s := englishSet()
	return &s
}
