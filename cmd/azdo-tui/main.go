package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Elpulgo/azdo/internal/app"
	"github.com/Elpulgo/azdo/internal/azdevops"
	"github.com/Elpulgo/azdo/internal/cli"
	"github.com/Elpulgo/azdo/internal/config"
	"github.com/Elpulgo/azdo/internal/demo"
	"github.com/Elpulgo/azdo/internal/github"
	"github.com/Elpulgo/azdo/internal/provider"
	"github.com/Elpulgo/azdo/internal/state"
	"github.com/Elpulgo/azdo/internal/ui/components"
	"github.com/Elpulgo/azdo/internal/ui/orgselect"
	"github.com/Elpulgo/azdo/internal/ui/patinput"
	"github.com/Elpulgo/azdo/internal/ui/providerselect"
	"github.com/Elpulgo/azdo/internal/ui/setupwizard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Build-time variables injected via ldflags by goreleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	action := cli.ParseArgs(args)

	switch action {
	case cli.ActionHelp:
		return runHelp()
	case cli.ActionVersion:
		return runVersion()
	case cli.ActionAuth:
		return runAuth()
	case cli.ActionDemo:
		return demo.Run(version, commit)
	default:
		return runTUI()
	}
}

func runHelp() error {
	configPath, _ := config.GetPath()
	if configPath == "" {
		configPath = "~/.config/azdo-tui/config.yaml"
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("99"))
	fmt.Println(titleStyle.Render(strings.Join(components.LogoArt, "\n")))

	fmt.Printf(`azdo - A TUI for Azure DevOps and GitHub (%s)

Usage:
  azdo              Start the TUI application
  azdo auth         Set or update credentials for Azure DevOps (PAT) or GitHub
  azdo demo         Launch with mock data (for screenshots/demos)
  azdo --help       Show this help message
  azdo --version    Show version information

Configuration:
  Config file:     %s
  Token storage:   System keyring (service: azdo-tui)
  Azure fallback:  AZDO_PAT environment variable
  GitHub fallback: GITHUB_TOKEN environment variable

Required Azure DevOps PAT scopes:
  Build        (Read)         - pipelines, build logs
  Code         (Read & Write) - pull requests, voting, comments
  Work Items   (Read & Write) - queries, comments, state changes

Required GitHub token scopes:
  Classic PAT:   repo          (private repos) or public_repo (public only)
                 notifications (Notifications tab: list, mark read, mark done)
  Fine-grained:  Metadata      (read)
                 Contents      (read)
                 Issues        (read & write)
                 Pull requests (read & write)
                 Actions       (read)
  Note: resolving PR comment threads requires a classic 'repo' PAT;
        fine-grained tokens are commonly rejected for that operation.
  Note: the GitHub share of the Notifications tab requires a CLASSIC
        token. GitHub's notifications API supports no fine-grained
        permission, so there is nothing to grant a fine-grained token
        here. The Azure DevOps share of the tab is unaffected.

Keyboard shortcuts (in TUI):
  Navigation:
    ↑/k          Move up
    ↓/j          Move down
    pgup/pgdn    Page up / down
    enter        View details / expand
    esc          Go back

  Tabs:
    1/2/3/4/5    Switch tabs (Notifications, Pull Requests, Work Items,
                 Pipelines, Metrics) — only enabled tabs are numbered
    ←/→          Previous / next tab

  Actions:
    f            Search / filter
    m            Toggle my items (PRs / work items)
    A            Toggle as reviewer (PRs)
    T            Filter by tag (work items)
    r            Refresh data
    v            Vote on PR (detail view)
    s            Change work item state (detail view)
    c            Add comment (work item detail)
    o            Open in browser (PR / work item / pipeline detail)
    t            Select theme
    ?            Toggle help overlay
    q            Quit

  Code Review (PR diff):
    c            Create new comment
    p            Reply to nearest thread
    x            Resolve nearest thread
    n / N        Jump to next / previous comment

  Log Viewer (pipelines):
    g            Go to top
    G            Go to bottom

For more information, visit: https://github.com/Elpulgo/azdo
`, version, configPath)

	return nil
}

func runVersion() error {
	fmt.Printf("azdo version %s (commit: %s, built: %s)\n", version, commit, date)
	return nil
}

func runAuth() error {
	store := config.NewKeyringStore()

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("99"))
	fmt.Println(titleStyle.Render(strings.Join(components.LogoArt, "\n")))
	fmt.Println()

	// Prompt the user to pick a provider.
	selModel := providerselect.NewModel()
	selProg := tea.NewProgram(selModel)
	selResult, err := selProg.Run()
	if err != nil {
		return fmt.Errorf("failed to run provider selection: %w", err)
	}
	finalSel, ok := selResult.(providerselect.Model)
	if !ok {
		return fmt.Errorf("unexpected model type from provider selection")
	}
	if finalSel.Cancelled() {
		return nil
	}

	switch finalSel.Selected() {
	case providerselect.ProviderAzure:
		return runAuthAzure(store)
	case providerselect.ProviderGitHub:
		return runAuthGitHub(store)
	default:
		return fmt.Errorf("unknown provider selected")
	}
}

// runAuthAzure is the Azure DevOps PAT auth flow. When the config lists more
// than one organisation, the user picks which organisation the PAT belongs
// to and it is stored under pat:<org>; otherwise the legacy shared keyring
// entry is used.
func runAuthAzure(store *config.KeyringStore) error {
	orgName, err := resolveAuthOrganization()
	if err != nil {
		return err
	}

	var isUpdate bool
	if orgName != "" {
		_, err = store.GetPATForOrg(orgName)
		// Treat a hit on the per-org entry (not merely the shared fallback)
		// as an update of that org's PAT. GetPATForOrg always falls through
		// to the shared entry, so check the dedicated key via a round-trip
		// store after a successful Set — for the prompt label, "update" when
		// any token would resolve is fine and matches prior single-org UX.
		isUpdate = err == nil
	} else {
		_, err = store.GetPAT()
		isUpdate = err == nil
	}

	if orgName != "" {
		if isUpdate {
			fmt.Printf("Azure DevOps PAT Update (%s)\n", orgName)
			fmt.Println("This will replace the Personal Access Token for this organisation in the system keyring.")
		} else {
			fmt.Printf("Azure DevOps PAT Setup (%s)\n", orgName)
			fmt.Println("This will store a Personal Access Token for this organisation in the system keyring.")
		}
	} else if isUpdate {
		fmt.Println("Azure DevOps PAT Update")
		fmt.Println("This will replace your existing Personal Access Token in the system keyring.")
	} else {
		fmt.Println("Azure DevOps PAT Setup")
		fmt.Println("This will store your Personal Access Token in the system keyring.")
	}
	fmt.Println()
	fmt.Println(patinput.PermissionInfoPlain())
	fmt.Println()

	pat, err := promptForPATWithMode(store, isUpdate, orgName)
	if err != nil {
		return fmt.Errorf("failed to set PAT: %w", err)
	}

	if pat != "" {
		if orgName != "" {
			fmt.Printf("\nPAT for %s saved successfully to system keyring.\n", orgName)
		} else {
			fmt.Println("\nPAT saved successfully to system keyring.")
		}
	}

	return nil
}

// resolveAuthOrganization returns the organisation name to store a PAT under.
// Multi-organisation configs prompt for a choice; single-organisation configs
// return that organisation's name (stored under pat:<org> so it still works
// when a second org is added later); no Azure config returns "" (legacy
// shared keyring entry).
func resolveAuthOrganization() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return "", nil
		}
		return "", err
	}
	if !cfg.HasAzure() {
		return "", nil
	}
	names := make([]string, 0, len(cfg.Organizations))
	for _, org := range cfg.Organizations {
		if org.Name != "" {
			names = append(names, org.Name)
		}
	}
	if len(names) == 0 {
		return cfg.Organization, nil
	}
	if len(names) == 1 {
		return names[0], nil
	}

	selModel := orgselect.NewModel(names)
	selProg := tea.NewProgram(selModel)
	selResult, err := selProg.Run()
	if err != nil {
		return "", fmt.Errorf("failed to run organisation selection: %w", err)
	}
	finalSel, ok := selResult.(orgselect.Model)
	if !ok {
		return "", fmt.Errorf("unexpected model type from organisation selection")
	}
	if finalSel.Cancelled() || !finalSel.Chosen() {
		return "", fmt.Errorf("organisation selection cancelled")
	}
	return finalSel.Selected(), nil
}

// runAuthGitHub is the GitHub token auth flow.
func runAuthGitHub(store *config.KeyringStore) error {
	_, err := store.GetGitHubToken()
	isUpdate := err == nil

	if isUpdate {
		fmt.Println("GitHub Token Update")
		fmt.Println("This will replace your existing GitHub token in the system keyring (service: azdo-tui).")
	} else {
		fmt.Println("GitHub Token Setup")
		fmt.Println("This will store your GitHub token in the system keyring (service: azdo-tui).")
		fmt.Println("Tip: GITHUB_TOKEN environment variable is also accepted as a fallback.")
	}
	fmt.Println()
	fmt.Println(`Required token scopes:
  Classic PAT:  repo            (private repos) or public_repo (public only)
                notifications   (Notifications tab: list, mark read, mark done)
  Fine-grained: Metadata        (read)
                Contents        (read)
                Issues          (read & write)
                Pull requests   (read & write)
                Actions         (read)
  Note: the GitHub share of the Notifications tab requires a CLASSIC token —
  GitHub's notifications API supports no fine-grained permission. The Azure
  DevOps share of the tab is unaffected.`)
	fmt.Println()

	var model patinput.Model
	if isUpdate {
		model = patinput.NewGitHubModelForUpdate()
	} else {
		model = patinput.NewGitHubModel()
	}
	p := tea.NewProgram(model)

	m, err := p.Run()
	if err != nil {
		return fmt.Errorf("failed to run GitHub token input: %w", err)
	}

	finalModel, ok := m.(patinput.Model)
	if !ok {
		return fmt.Errorf("unexpected model type from GitHub token input")
	}

	token := finalModel.GetPAT()
	if token == "" {
		return nil
	}

	if err := store.SetGitHubToken(token); err != nil {
		return fmt.Errorf("failed to save GitHub token: %w", err)
	}

	fmt.Println("\nGitHub token saved successfully to system keyring.")
	return nil
}

func runTUI() error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			cfg, err = runSetupWizard()
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// Build the configured backends and assemble a CompositeProvider.
	store := config.NewKeyringStore()

	var backends []provider.Provider
	var azureMC *azdevops.MultiClient
	var azureNotifStores []*azdevops.TriageStore

	// --- Azure backends (one adapter per configured organisation) ---
	// Each organisation becomes its own provider.Provider inside the
	// CompositeProvider, reusing the existing multi-backend list fan-out and
	// scope→backend detail routing. Metrics stays bound to the first
	// organisation's MultiClient (documented follow-up to fan metrics out).
	if cfg.HasAzure() {
		multiOrg := cfg.IsMultiOrganization()
		lookbackDays, sourceToggles, minPollInterval := azureNotificationArgs(cfg.Notifications.Azure)

		for i, org := range cfg.Organizations {
			if org.Name == "" || len(org.Projects) == 0 {
				continue
			}

			pat, err := store.GetPATForOrg(org.Name)
			if err != nil {
				if errors.Is(err, config.ErrNotFound) {
					pat, err = promptForPATForOrg(store, org.Name)
					if err != nil {
						return fmt.Errorf("failed to set PAT for organisation %q: %w", org.Name, err)
					}
				} else {
					return fmt.Errorf("failed to get PAT for organisation %q: %w", org.Name, err)
				}
			}

			client, err := azdevops.NewMultiClient(org.Name, org.Projects, pat, org.DisplayNames)
			if err != nil {
				return fmt.Errorf("failed to create Azure DevOps client for organisation %q: %w", org.Name, err)
			}
			if i == 0 || azureMC == nil {
				azureMC = client
			}

			// NewAdapter alone (bare, no notifications store) still satisfies
			// provider.NotificationSource by method set — Go's structural typing
			// does not care that its notifStore field is left at its zero value.
			// That would make the Notifications tab appear for an Azure-only
			// config while List's nil-store guard makes every poll fail
			// (azdevops.Adapter.list's own doc comment, task 8 review, 🔴
			// finding 1). NewAdapterWithNotifications is what actually wires the
			// local read/done triage store notifications need.
			notifPath, err := azdevops.NotifStorePathFor(org.Name, multiOrg)
			if err != nil {
				return fmt.Errorf("resolve notifications state path for %q: %w", org.Name, err)
			}
			notifStore, err := azdevops.NewTriageStore(notifPath)
			if err != nil {
				return fmt.Errorf("load notifications state for %q: %w", org.Name, err)
			}
			azureNotifStores = append(azureNotifStores, notifStore)

			adapter := azdevops.NewAdapterWithNotifications(
				client, notifStore, lookbackDays, sourceToggles, minPollInterval)
			if multiOrg {
				adapter.SetQualifyScopeDisplay(true)
			}
			backends = append(backends, adapter)
		}
	}

	// --- GitHub backend (only when at least one repo is configured) ---
	if cfg.HasGitHub() {
		token, err := store.GetGitHubToken()
		if err != nil {
			return fmt.Errorf(
				"GitHub token not found: run 'azdo auth' or set the GITHUB_TOKEN environment variable: %w", err)
		}

		conv := github.LabelConvention{
			TypePrefix:     cfg.GitHub.TypePrefix,
			PriorityPrefix: cfg.GitHub.PriorityPrefix,
		}
		ghMC, err := github.NewMultiClient(cfg.GitHub.Repos, token, conv, nil)
		if err != nil {
			return fmt.Errorf("failed to create GitHub client: %w", err)
		}
		ghNC := github.NewNotificationsClient(token)

		// The done store persists mark-done tombstones locally because
		// GitHub's REST API can write done state but never read it back
		// (github.DoneEntry's doc comment) — without it, every `d`-marked
		// row resurrects on the next launch. Unlike azureNotifStore it needs
		// no shutdown Flush and no variable outliving this block: its writes
		// are synchronous (github.DoneStore's doc comment), so there is
		// never a pending debounced write to lose on exit.
		ghDonePath, err := github.DoneStorePath()
		if err != nil {
			return fmt.Errorf("resolve github notifications state path: %w", err)
		}
		ghDoneStore, err := github.NewDoneStore(ghDonePath)
		if err != nil {
			return fmt.Errorf("load github notifications state: %w", err)
		}
		backends = append(backends, github.NewAdapterWithNotifications(ghMC, ghNC, ghDoneStore))
	}

	// Defense-in-depth: config.Validate() already requires ≥1 backend, but guard
	// here as well so a future caller of runTUI without a prior Validate does not
	// silently produce a zero-backend composite.
	if len(backends) <= 0 {
		return fmt.Errorf("no provider configured: set up Azure DevOps or GitHub (run the setup wizard)")
	}

	composite := provider.NewCompositeProvider(backends...)

	// Load persisted state (last active tab, last-viewed PR/work item).
	// A missing file is treated as a clean slate — not an error.
	statePath, err := state.Path()
	if err != nil {
		return fmt.Errorf("resolve state path: %w", err)
	}
	stateStore, err := state.NewStore(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Create and run the TUI application.
	model := app.NewModel(composite, azureMC, cfg, version, commit)
	model.SetStateStore(stateStore)
	model.ApplyState(stateStore.State())
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Forward OS termination signals to Bubble Tea so the program unwinds
	// cleanly (alt-screen restored, state flushed) instead of being killed
	// mid-write. SIGINT is also handled by the in-app 'q'/Ctrl+C binding;
	// SIGTERM and SIGHUP are the ones that matter here.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigCh
		p.Send(tea.QuitMsg{})
	}()

	// Best-effort flush on any exit path — normal quit, signal-driven
	// quit, or a panic propagating up from the Tea program. A SIGKILL or
	// power loss is unrecoverable; the debounced writes during the
	// session bound the loss window.
	defer func() {
		signal.Stop(sigCh)
		if flushErr := stateStore.Flush(); flushErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to persist state: %v\n", flushErr)
		}
		for _, notifStore := range azureNotifStores {
			if notifStore == nil {
				continue
			}
			if flushErr := notifStore.Flush(); flushErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to persist notifications state: %v\n", flushErr)
			}
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI application error: %w", err)
	}

	return nil
}

// azureNotificationArgs derives azdevops.NewAdapterWithNotifications's three
// notifications.azure-sourced arguments (lookback window, per-source
// toggles, self-throttle interval) from azure. It exists as its own
// function, separate from the constructor call in runTUI above, purely so a
// test can drive a non-default NotificationsAzureConfig through it without
// going through runTUI's keyring/network setup — see
// TestAzureNotificationArgs_NonDefaultValuesReachTheAdapter
// (cmd/azdo-tui/main_test.go). config.NotificationsAzureConfig already
// carries defaults, clamping and validation for LookbackDays and
// MinPollInterval (task 11); this function does no normalisation of its
// own, it only reshapes already-normalised fields into the types
// NewAdapterWithNotifications takes — MinPollInterval is seconds in config
// (matching notifications.azure.min_poll_interval's YAML units) and a
// time.Duration in the adapter.
func azureNotificationArgs(azure config.NotificationsAzureConfig) (lookbackDays int, toggles azdevops.NotificationSourceToggles, minPollInterval time.Duration) {
	return azure.LookbackDays,
		azdevops.NotificationSourceToggles{
			ReviewRequested: azure.Sources.ReviewRequested,
			Mentioned:       azure.Sources.Mentioned,
			Assigned:        azure.Sources.Assigned,
			CIFailed:        azure.Sources.CIFailed,
		},
		time.Duration(azure.MinPollInterval) * time.Second
}

// runSetupWizard launches the interactive setup wizard and saves the config.
func runSetupWizard() (*config.Config, error) {
	model := setupwizard.NewModel()
	p := tea.NewProgram(model)

	m, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("setup wizard error: %w", err)
	}

	finalModel, ok := m.(setupwizard.Model)
	if !ok {
		return nil, fmt.Errorf("unexpected model type from setup wizard")
	}

	if finalModel.Cancelled() {
		return nil, fmt.Errorf("setup cancelled")
	}

	cfg := finalModel.GetConfig()
	if cfg == nil {
		return nil, fmt.Errorf("setup wizard did not produce a configuration")
	}

	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	// Persist the GitHub token to the keyring when the wizard collected one.
	if tok := finalModel.GitHubToken(); tok != "" {
		ks := config.NewKeyringStore()
		if err := ks.SetGitHubToken(tok); err != nil {
			return nil, fmt.Errorf("failed to store GitHub token: %w", err)
		}
	}

	fmt.Println("\nConfiguration saved successfully!")
	return cfg, nil
}

// promptForPATForOrg displays a TUI to prompt for a PAT for one organisation
// (first-time setup for that org).
func promptForPATForOrg(store *config.KeyringStore, org string) (string, error) {
	if org != "" {
		fmt.Printf("Personal Access Token required for organisation %q\n\n", org)
	}
	return promptForPATWithMode(store, false, org)
}

// promptForPATWithMode displays a TUI to prompt the user for their PAT.
// If isUpdate is true, shows an "update" message instead of "first-time setup".
// When org is non-empty the token is stored under pat:<org>; otherwise under
// the legacy shared keyring entry.
func promptForPATWithMode(store *config.KeyringStore, isUpdate bool, org string) (string, error) {
	var model patinput.Model
	if isUpdate {
		model = patinput.NewModelForUpdate()
	} else {
		model = patinput.NewModel()
	}
	p := tea.NewProgram(model)

	m, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("failed to run PAT input: %w", err)
	}

	// Extract the final model
	finalModel, ok := m.(patinput.Model)
	if !ok {
		return "", fmt.Errorf("unexpected model type")
	}

	pat := finalModel.GetPAT()
	if pat == "" {
		return "", fmt.Errorf("PAT input cancelled or empty")
	}

	if org != "" {
		if err := store.SetPATForOrg(org, pat); err != nil {
			return "", fmt.Errorf("failed to save PAT to keyring: %w", err)
		}
	} else if err := store.SetPAT(pat); err != nil {
		return "", fmt.Errorf("failed to save PAT to keyring: %w", err)
	}

	return pat, nil
}
