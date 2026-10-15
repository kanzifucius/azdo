package pipelines

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Elpulgo/azdo/internal/provider"
)

// optsRecordingProvider embeds provider.Provider so only ListPipelineRuns
// needs implementing; it records the ListOpts of each call.
type optsRecordingProvider struct {
	provider.Provider
	opts []provider.ListOpts
}

func (p *optsRecordingProvider) IsMultiProject() bool { return false }

func (p *optsRecordingProvider) ListPipelineRuns(top int, opts provider.ListOpts) ([]provider.PipelineRun, error) {
	p.opts = append(p.opts, opts)
	return nil, nil
}

// runInit executes Init's batch so the nested fetch actually runs.
func runInit(t *testing.T, m Model) {
	t.Helper()
	if batch, ok := m.Init()().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
}

func TestModel_SetAllRepos_ReachesTheListFetch(t *testing.T) {
	p := &optsRecordingProvider{}
	m := NewModel(p)

	runInit(t, m)
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
	runInit(t, m)
	if len(p.opts) != 2 || !p.opts[1].AllRepos {
		t.Fatalf("toggled fetch must send AllRepos=true, got %+v", p.opts)
	}

	m.SetAllRepos(false)
	runInit(t, m)
	if p.opts[2].AllRepos {
		t.Error("switching the filter back on must send AllRepos=false again")
	}
}
