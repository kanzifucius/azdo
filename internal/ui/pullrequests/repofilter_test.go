package pullrequests

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Elpulgo/azdo/internal/provider"
)

// optsRecordingProvider embeds provider.Provider so only the list methods the
// pane calls need implementing; it records the ListOpts of each call.
type optsRecordingProvider struct {
	provider.Provider
	calls []string
	opts  []provider.ListOpts
}

func (p *optsRecordingProvider) IsMultiProject() bool { return false }

func (p *optsRecordingProvider) ListPullRequests(top int, opts provider.ListOpts) ([]provider.PullRequest, error) {
	p.calls = append(p.calls, "all")
	p.opts = append(p.opts, opts)
	return nil, nil
}

func (p *optsRecordingProvider) ListMyPullRequests(top int, opts provider.ListOpts) ([]provider.PullRequest, error) {
	p.calls = append(p.calls, "mine")
	p.opts = append(p.opts, opts)
	return nil, nil
}

func (p *optsRecordingProvider) ListPullRequestsAsReviewer(top int, opts provider.ListOpts) ([]provider.PullRequest, error) {
	p.calls = append(p.calls, "reviewer")
	p.opts = append(p.opts, opts)
	return nil, nil
}

// runCmd executes cmd, descending into tea.Batch so the fetch nested inside
// Init's batch actually runs.
func runCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
}

func TestModel_SetAllRepos_ReachesEveryFetchVariant(t *testing.T) {
	p := &optsRecordingProvider{}
	m := NewModel(p)

	// Default: honour the filter.
	runCmd(t, m.Init())
	if len(p.opts) != 1 || p.opts[0].AllRepos {
		t.Fatalf("default fetch must send AllRepos=false, got %+v", p.opts)
	}
	if m.AllRepos() {
		t.Fatal("AllRepos() must default to false")
	}

	m.SetAllRepos(true)
	if !m.AllRepos() {
		t.Fatal("AllRepos() should report the toggle")
	}

	// Refetch re-issues the base fetch with the toggle, and the listview's
	// own Fetch closure (used by the r key) sees the same shared state.
	m, cmd := m.Refetch()
	runCmd(t, cmd)
	runCmd(t, m.Init())
	if len(p.opts) != 3 || !p.opts[1].AllRepos || !p.opts[2].AllRepos {
		t.Fatalf("toggled fetches must send AllRepos=true, got %+v", p.opts)
	}

	// The "my PRs" and "as reviewer" dispatches inherit the toggle too.
	runCmd(t, fetchMyPullRequestsMulti(m.client, m.fetchOpts.listOpts()))
	runCmd(t, fetchPullRequestsAsReviewerMulti(m.client, m.fetchOpts.listOpts()))
	if p.calls[3] != "mine" || !p.opts[3].AllRepos || !p.opts[3].Mine {
		t.Errorf("my-PRs fetch should carry Mine and AllRepos, got %s %+v", p.calls[3], p.opts[3])
	}
	if p.calls[4] != "reviewer" || !p.opts[4].AllRepos {
		t.Errorf("reviewer fetch should carry AllRepos, got %s %+v", p.calls[4], p.opts[4])
	}

	m.SetAllRepos(false)
	runCmd(t, m.Init())
	if p.opts[len(p.opts)-1].AllRepos {
		t.Error("switching the filter back on must send AllRepos=false again")
	}
}

func TestModel_PullRequestsMsg_RedispatchesMyPRsWithToggle(t *testing.T) {
	p := &optsRecordingProvider{}
	m := NewModel(p)
	m.SetAllRepos(true)
	m.myPRsOnly = true

	_, cmd := m.Update(pullRequestsMsg{prs: nil, err: nil})
	runCmd(t, cmd)
	if len(p.calls) != 1 || p.calls[0] != "mine" || !p.opts[0].AllRepos {
		t.Errorf("expected a my-PRs re-dispatch carrying AllRepos, got %v %+v", p.calls, p.opts)
	}
}
