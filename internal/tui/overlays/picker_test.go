package overlays

import (
	"testing"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/billygate/kapctl/internal/tui/core"
)

func newPickerModel(items []string) pickerModel {
	s := newTestStyles()
	var listItems []list.Item
	for _, i := range items {
		listItems = append(listItems, core.Item(i))
	}
	l := list.New(listItems, core.NewItemDelegate(s), 80, 20)
	l.Title = "test"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	return pickerModel{
		list:   l,
		help:   help.New(),
		styles: s,
		width:  80,
		height: 24,
	}
}

func TestPickerModelInit(t *testing.T) {
	m := newPickerModel([]string{"a", "b"})
	if cmd := m.Init(); cmd != nil {
		t.Error("Init should return nil Cmd")
	}
}

func TestPickerModelWindowSizeMsg(t *testing.T) {
	m := newPickerModel([]string{"a", "b"})
	msg := tea.WindowSizeMsg{Width: 100, Height: 40}
	next, _ := m.Update(msg)
	pm := next.(pickerModel)
	if pm.width != 100 {
		t.Errorf("width = %d, want 100", pm.width)
	}
	if pm.height != 40 {
		t.Errorf("height = %d, want 40", pm.height)
	}
}

func TestPickerModelQuitKey(t *testing.T) {
	m := newPickerModel([]string{"a", "b"})
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}
	next, cmd := m.Update(msg)
	pm := next.(pickerModel)
	if !pm.quitting {
		t.Error("q key should set quitting = true")
	}
	if cmd == nil {
		t.Error("q key should return a Cmd (tea.Quit)")
	}
}

func TestPickerModelViewQuitting(t *testing.T) {
	m := newPickerModel([]string{"a"})
	m.quitting = true
	if v := m.View(); v != "" {
		t.Errorf("View when quitting = %q, want empty", v)
	}
}

func TestPickerModelViewNormal(t *testing.T) {
	m := newPickerModel([]string{"a", "b"})
	m.width = 80
	m.height = 24
	v := m.View()
	if v == "" {
		t.Error("View should return non-empty string for normal state")
	}
}
