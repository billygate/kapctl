// Package overlays holds the pieces of the TUI that float over (or
// stand alone from) the main panes: the single-shot Pick modal reused
// by Cobra subcommands, the toast queue, and the port-selection helpers.
package overlays

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kap-toolsbox/internal/tui/core"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
)

// pickerModel is a single-shot list picker driving the public Pick().
type pickerModel struct {
	list     list.Model
	choice   string
	quitting bool
	help     help.Model
	width    int
	height   int
	styles   *styles.Styles
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h, v := m.styles.Window.GetFrameSize()
		listW, listH := msg.Width-h, msg.Height-v-5

		if core.ShouldShowTwoColumns(listW, m.list.Items()) {
			m.list.SetSize(listW, listH*2)
		} else {
			m.list.SetSize(listW, listH)
		}

		m.help.Width = listW
		return m, nil

	case tea.KeyMsg:
		if m.list.FilterState() == list.Filtering {
			break
		}

		switch {
		case key.Matches(msg, core.Keys.Quit):
			m.quitting = true
			return m, tea.Quit
		case key.Matches(msg, core.Keys.Select):
			i, ok := m.list.SelectedItem().(core.ListItem)
			if ok {
				m.choice = i.Text
			}
			return m, tea.Quit
		case key.Matches(msg, core.Keys.Filter):
			m.list.ResetFilter()
			m.list.SetFilteringEnabled(true)
			return m, m.list.FilterInput.Focus()
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickerModel) View() string {
	if m.quitting {
		return ""
	}

	h, v := m.styles.Window.GetFrameSize()
	listW, listH := m.width-h, m.height-v-5

	var content string
	if core.ShouldShowTwoColumns(listW, m.list.Items()) {
		title := m.styles.Title.Render(m.list.Title)
		listContent := core.RenderTwoColumns(m.list, listW, listH)
		content = lipgloss.JoinVertical(lipgloss.Left, title, "", listContent)
	} else {
		content = m.list.View()
	}

	helpView := m.styles.Footer.Render(m.help.View(core.Keys))

	return m.styles.Window.
		Width(m.width - 2).
		Height(m.height - 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, content, helpView))
}

// Pick presents a single-list picker and returns the selected item's text.
func Pick(title string, items []string, s *styles.Styles) (string, error) {
	var listItems []list.Item
	for _, i := range items {
		listItems = append(listItems, core.Item(i))
	}

	l := list.New(listItems, core.NewItemDelegate(s), 0, 0)
	l.Title = title
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false) // Vim style: press / to filter
	l.Styles.Title = s.Title
	l.SetShowHelp(false)

	m := pickerModel{
		list:   l,
		help:   help.New(),
		styles: s,
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}

	return finalModel.(pickerModel).choice, nil
}
