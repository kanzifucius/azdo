package orgselect

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOrgSelect_EnterChooses(t *testing.T) {
	m := NewModel([]string{"org-a", "org-b"})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if cmd != nil {
		// j should not quit
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if !m.Chosen() || m.Selected() != "org-b" {
		t.Fatalf("Chosen=%v Selected=%q, want org-b", m.Chosen(), m.Selected())
	}
	if cmd == nil {
		t.Fatal("enter should return tea.Quit")
	}
}

func TestOrgSelect_Cancel(t *testing.T) {
	m := NewModel([]string{"org-a"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if !m.Cancelled() {
		t.Fatal("esc should cancel")
	}
}
