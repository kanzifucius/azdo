package app

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Elpulgo/azdo/internal/azdevops"
	"github.com/Elpulgo/azdo/internal/config"
	"github.com/Elpulgo/azdo/internal/provider"
	"github.com/Elpulgo/azdo/internal/state"
	"github.com/Elpulgo/azdo/internal/ui/components"
)

// repoFilterProvider embeds provider.Provider so only the list methods the PR
// and pipelines panes call need implementing; it records every ListOpts.
type repoFilterProvider struct {
	provider.Provider
	prOpts  []provider.ListOpts
	runOpts []provider.ListOpts
}

func (p *repoFilterProvider) Kind() provider.Kind  { return provider.KindAzure }
func (p *repoFilterProvider) IsMultiProject() bool { return false }
func (p *repoFilterProvider) Scopes() []string     { return []string{"proj"} }

func (p *repoFilterProvider) ListPullRequests(top int, opts provider.ListOpts) ([]provider.PullRequest, error) {
	p.prOpts = append(p.prOpts, opts)
	return nil, nil
}

func (p *repoFilterProvider) ListMyPullRequests(top int, opts provider.ListOpts) ([]provider.PullRequest, error) {
	p.prOpts = append(p.prOpts, opts)
	return nil, nil
}

func (p *repoFilterProvider) ListPullRequestsAsReviewer(top int, opts provider.ListOpts) ([]provider.PullRequest, error) {
	p.prOpts = append(p.prOpts, opts)
	return nil, nil
}

func (p *repoFilterProvider) ListPipelineRuns(top int, opts provider.ListOpts) ([]provider.PipelineRun, error) {
	p.runOpts = append(p.runOpts, opts)
	return nil, nil
}

func (p *repoFilterProvider) ListWorkItems(int, provider.ListOpts) ([]provider.WorkItem, error) {
	return nil, nil
}

func repoFilterConfig() *config.Config {
	return &config.Config{
		Organization:    "testorg",
		Projects:        []string{"proj", "other"},
		DisplayNames:    map[string]string{"proj": "My Project"},
		RepoFilters:     map[string][]string{"proj": {"frontend", "infra-*"}},
		PollingInterval: 60,
		Theme:           "dark",
	}
}

func unfilteredConfig() *config.Config {
	return &config.Config{
		Organization:    "testorg",
		Projects:        []string{"proj"},
		PollingInterval: 60,
		Theme:           "dark",
	}
}

func newRepoFilterModel(t *testing.T, cfg *config.Config) (Model, *repoFilterProvider) {
	t.Helper()
	p := &repoFilterProvider{}
	var client *azdevops.MultiClient
	m := NewModel(p, client, cfg, "dev", "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	return updated.(Model), p
}

func pressKey(t *testing.T, m Model, r rune) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return updated.(Model), cmd
}

// runCmd executes cmd, descending into batches recursively so nested
// fetches run. Never hand it the whole app Init: that batch includes the
// polling timer (tea.Every) which would block for the full interval.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmd(c)
		}
	}
}

// runPaneFetches executes the active pane's Init and one poller fetch, the
// data-fetching subset of Model.Init.
func runPaneFetches(m Model) {
	runCmd(m.initTabCmd(m.activeTab))
	runCmd(m.poller.FetchPipelineRuns())
}

func TestRepoFilter_UnconfiguredSetupIsUnchanged(t *testing.T) {
	m, p := newRepoFilterModel(t, unfilteredConfig())
	if m.repoFilterConfigured {
		t.Fatal("repoFilterConfigured must be false without a repos key")
	}
	if label, active := m.filterLabelForTab(); active || label != "" {
		t.Errorf("no pill expected on the PR tab, got %q", label)
	}
	if strings.Contains(m.pullRequestsKeybindings(), "repos") || strings.Contains(m.pipelinesKeybindings(), "repos") {
		t.Error("footer must not advertise R without a configured filter")
	}

	m, cmd := pressKey(t, m, 'R')
	if cmd != nil || m.repoFilterOff {
		t.Error("R must be a no-op without a configured filter")
	}
	runPaneFetches(m)
	for _, o := range append(p.prOpts, p.runOpts...) {
		if o.AllRepos {
			t.Error("fetches must send AllRepos=false when nothing is configured")
		}
	}

	m, _ = pressKey(t, m, '?')
	view := m.View()
	if strings.Contains(view, "Toggle repository filter") || strings.Contains(view, "Repo filter:") {
		t.Error("help modal must not mention the repository filter when none is configured")
	}
}

func TestRepoFilter_ToggleOnPRTab_RefetchesPersistsAndUpdatesPill(t *testing.T) {
	cfg := repoFilterConfig()
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	store, err := state.NewStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	store.SetDebounce(5 * time.Millisecond)

	m, p := newRepoFilterModel(t, cfg)
	m.SetStateStore(store)
	if !m.repoFilterConfigured || m.activeTab != TabPullRequests {
		t.Fatalf("precondition: configured=%v tab=%v", m.repoFilterConfigured, m.activeTab)
	}

	if label, active := m.filterLabelForTab(); !active || label != "Repos" {
		t.Errorf("pill = %q (active=%v), want Repos", label, active)
	}
	if !strings.Contains(m.pullRequestsKeybindings(), "repos") || !strings.Contains(m.pipelinesKeybindings(), "repos") {
		t.Error("footer should advertise R repos when a filter is configured")
	}

	m, cmd := pressKey(t, m, 'R')
	if !m.repoFilterOff {
		t.Fatal("R should switch the filter off")
	}
	if cmd == nil {
		t.Fatal("R must refetch the PR pane")
	}
	runCmd(cmd)
	if len(p.prOpts) != 1 || !p.prOpts[0].AllRepos {
		t.Fatalf("refetch must send AllRepos=true, got %+v", p.prOpts)
	}
	if label, active := m.filterLabelForTab(); active || label != "" {
		t.Errorf("pill must disappear while the filter is off, got %q", label)
	}
	if !m.pullRequestsView.AllRepos() || !m.pipelinesView.AllRepos() {
		t.Error("both panes must receive the toggle")
	}
	runCmd(m.poller.FetchPipelineRuns())
	if len(p.runOpts) != 1 || !p.runOpts[0].AllRepos {
		t.Errorf("poller must carry the toggle, got %+v", p.runOpts)
	}

	if !store.State().RepoFilterOff {
		t.Fatal("toggle must be persisted to the store")
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.RepoFilterOff {
		t.Error("repo_filter_off must survive a round trip through state.yaml")
	}

	// Second press switches it back on and persists false.
	m, cmd = pressKey(t, m, 'R')
	runCmd(cmd)
	if m.repoFilterOff || store.State().RepoFilterOff {
		t.Error("second R must switch the filter back on and persist that")
	}
	if last := p.prOpts[len(p.prOpts)-1]; last.AllRepos {
		t.Error("refetch after switching back on must send AllRepos=false")
	}
	if label, active := m.filterLabelForTab(); !active || label != "Repos" {
		t.Errorf("pill should return, got %q", label)
	}
}

func TestRepoFilter_PillComposesWithExistingLabels(t *testing.T) {
	m, _ := newRepoFilterModel(t, repoFilterConfig())

	m, _ = pressKey(t, m, 'm')
	if label, _ := m.filterLabelForTab(); label != "My PRs + Repos" {
		t.Errorf("PR tab pill = %q, want %q", label, "My PRs + Repos")
	}

	m, _ = pressKey(t, m, 'R')
	if label, _ := m.filterLabelForTab(); label != "My PRs" {
		t.Errorf("PR tab pill with filter off = %q, want %q", label, "My PRs")
	}
}

func TestRepoFilter_ToggleOnPipelinesTab_UsesPoller(t *testing.T) {
	m, p := newRepoFilterModel(t, repoFilterConfig())
	m.activeTab = TabPipelines

	if label, active := m.filterLabelForTab(); !active || label != "Repos" {
		t.Errorf("pipelines pill = %q, want Repos", label)
	}

	m, cmd := pressKey(t, m, 'R')
	if cmd == nil {
		t.Fatal("R on the pipelines tab must trigger a poller fetch")
	}
	runCmd(cmd)
	if len(p.runOpts) != 1 || !p.runOpts[0].AllRepos {
		t.Errorf("poller fetch must send AllRepos=true, got %+v", p.runOpts)
	}
}

func TestRepoFilter_IgnoredOnOtherTabsAndInDetail(t *testing.T) {
	m, _ := newRepoFilterModel(t, repoFilterConfig())

	m.activeTab = TabWorkItems
	m, cmd := pressKey(t, m, 'R')
	if cmd != nil || m.repoFilterOff {
		t.Error("R must not act on the work items tab")
	}

	m.activeTab = TabPullRequests
	if !m.canToggleRepoFilter() {
		t.Fatal("precondition: PR list should accept R")
	}
}

func TestRepoFilter_ApplyStateSeedsToggle(t *testing.T) {
	m, p := newRepoFilterModel(t, repoFilterConfig())
	m.ApplyState(state.State{RepoFilterOff: true})
	if !m.repoFilterOff {
		t.Fatal("ApplyState must seed repoFilterOff")
	}
	runPaneFetches(m)
	for _, o := range append(p.prOpts, p.runOpts...) {
		if !o.AllRepos {
			t.Errorf("initial fetches after a restored toggle must send AllRepos=true, got %+v", o)
		}
	}
	if len(p.prOpts) == 0 || len(p.runOpts) == 0 {
		t.Fatalf("expected both panes to fetch on Init, got pr=%d runs=%d", len(p.prOpts), len(p.runOpts))
	}
}

func TestRepoFilter_HelpModalEntries(t *testing.T) {
	m, _ := newRepoFilterModel(t, repoFilterConfig())
	m, _ = pressKey(t, m, '?')
	view := m.View()
	if !strings.Contains(view, "Toggle repository filter (PRs / pipeline runs)") {
		t.Errorf("help modal should list the R binding, got:\n%s", view)
	}
	if !strings.Contains(view, "Repo filter: My Project → frontend, infra-*") {
		t.Errorf("help modal Info section should summarise the filter, got:\n%s", view)
	}
}

func TestRepoFilter_HelpEntrySurvivesPipelinesDisabled(t *testing.T) {
	cfg := repoFilterConfig()
	cfg.DisabledPanes = []string{"pipelines"}
	m, _ := newRepoFilterModel(t, cfg)
	m, _ = pressKey(t, m, '?')
	if !strings.Contains(m.View(), "Toggle repository filter") {
		t.Error("the R help entry must survive RemoveBindingsByDescription(\"pipelines\")")
	}
}

func TestRepoFilter_ThemeRebuildKeepsToggleAndHelp(t *testing.T) {
	cfg := config.NewWithPath("testorg", []string{"proj"}, 60, "dark", filepath.Join(t.TempDir(), "config.yaml"))
	cfg.Organizations[0].RepoFilters = map[string][]string{"proj": {"frontend"}}
	m, p := newRepoFilterModel(t, cfg)
	m, _ = pressKey(t, m, 'R')
	p.prOpts, p.runOpts = nil, nil

	updated, cmd := m.Update(components.ThemeSelectedMsg{ThemeName: "catppuccin"})
	m = updated.(Model)
	runCmd(cmd)
	if !m.pullRequestsView.AllRepos() || !m.pipelinesView.AllRepos() {
		t.Error("recreated panes must inherit the toggle")
	}
	for _, o := range append(p.prOpts, p.runOpts...) {
		if !o.AllRepos {
			t.Errorf("refetch after a theme change must honour the toggle, got %+v", o)
		}
	}
	m, _ = pressKey(t, m, '?')
	if !strings.Contains(m.View(), "Toggle repository filter") {
		t.Error("rebuilt help modal must list the R binding")
	}
}

func TestRepoFilter_WarningsBannerOnPRAndPipelinesOnly(t *testing.T) {
	m, _ := newRepoFilterModel(t, repoFilterConfig())
	m.SetRepoFilterWarnings([]string{`project "proj": repos entry "frontnd" matches no repository`})

	if !strings.Contains(m.View(), `repos entry "frontnd"`) {
		t.Error("PR tab should show the resolution warning banner")
	}
	m.activeTab = TabPipelines
	if !strings.Contains(m.View(), `repos entry "frontnd"`) {
		t.Error("pipelines tab should show the resolution warning banner")
	}
	m.activeTab = TabWorkItems
	if strings.Contains(m.View(), `repos entry "frontnd"`) {
		t.Error("work items tab must not show the repository-filter banner")
	}

	// The banner persists while the filter is off: the config problem does.
	m.activeTab = TabPullRequests
	m, _ = pressKey(t, m, 'R')
	if !strings.Contains(m.View(), `repos entry "frontnd"`) {
		t.Error("banner must stay visible while the filter is switched off")
	}
}

func TestPaneWithWarnings(t *testing.T) {
	if got := paneWithWarnings("BODY", nil); got != "BODY" {
		t.Errorf("paneWithWarnings(nil) = %q", got)
	}
	got := paneWithWarnings("BODY", []string{"w1", "w2"})
	if got != "w1\nw2\n\nBODY" {
		t.Errorf("paneWithWarnings = %q", got)
	}
}

func TestRepoFilterSummary(t *testing.T) {
	cfg := &config.Config{
		Organizations: []config.AzureOrgConfig{
			{Name: "one", Projects: []string{"a", "b"}, DisplayNames: map[string]string{"a": "Alpha"}, RepoFilters: map[string][]string{"a": {"x", "y-*"}}},
			{Name: "two", Projects: []string{"c"}, RepoFilters: map[string][]string{"c": {"billing"}}},
		},
	}
	got := repoFilterSummary(cfg)
	want := []string{"Alpha → x, y-*", "c → billing"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repoFilterSummary = %v, want %v", got, want)
	}
	if repoFilterSummary(unfilteredConfig()) != nil {
		t.Error("no filter → nil summary")
	}
}
