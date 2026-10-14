// Package orgselect provides a small standalone Bubble Tea model for
// prompting the user to choose an Azure DevOps organisation during
// 'azdo auth' when more than one organisation is configured. It mirrors
// the shape of providerselect: exported Model, NewModel(), Init/Update/View,
// Selected() and Cancelled() getters.
package orgselect

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// Model is the organisation-selection Bubble Tea model.
type Model struct {
	orgs      []string
	cursor    int
	selected  string
	chosen    bool
	cancelled bool
}

// NewModel returns a Model listing the given organisation names. orgs must
// be non-empty; the cursor starts on the first entry.
func NewModel(orgs []string) Model {
	selected := ""
	if len(orgs) > 0 {
		selected = orgs[0]
	}
	return Model{
		orgs:     append([]string(nil), orgs...),
		cursor:   0,
		selected: selected,
	}
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update handles key presses: j/down moves down, k/up moves up, enter
// selects, esc/ctrl+c cancels.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.cursor < len(m.orgs)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor >= 0 && m.cursor < len(m.orgs) {
				m.selected = m.orgs[m.cursor]
			}
			m.chosen = true
			return m, tea.Quit
		case "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders the organisation list.
func (m Model) View() string {
	s := titleStyle.Render("Select an Azure DevOps organisation") + "\n\n"

	for i, org := range m.orgs {
		cursor := "  "
		if m.cursor == i {
			cursor = "> "
			s += cursor + selectedStyle.Render(org) + "\n"
		} else {
			s += cursor + org + "\n"
		}
	}

	s += "\n" + helpStyle.Render("↑/k up · ↓/j down · enter select · esc cancel")
	if len(m.orgs) == 0 {
		s += "\n" + fmt.Sprintf("\n(no organisations configured)")
	}
	return s
}

// Selected returns the chosen organisation name. Only meaningful after
// Chosen() is true.
func (m Model) Selected() string {
	return m.selected
}

// Chosen reports whether the user confirmed a selection with enter.
func (m Model) Chosen() bool {
	return m.chosen
}

// Cancelled reports whether the user aborted with esc/ctrl+c.
func (m Model) Cancelled() bool {
	return m.cancelled
}
