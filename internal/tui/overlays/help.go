package overlays

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kap-toolsbox/internal/tui/core"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
)

// Help is the modal opened with `?`. View returns the rendered modal
// when Visible is true, or an empty string when hidden.
type Help struct {
	Visible bool
}

// helpSection is one titled group of bindings rendered inside the modal.
type helpSection struct {
	title    string
	bindings [][2]string // {keys, description}
}

// helpSections returns the static content of the modal. Where possible
// the strings come from core.Keys via key.WithHelp so a binding rename
// is reflected automatically.
func helpSections() []helpSection {
	k := core.Keys
	return []helpSection{
		{
			title: "Global",
			bindings: [][2]string{
				{"?", "help (close)"},
				{"q / ctrl+c", "quit"},
				{k.NextTab.Help().Key + " / " + k.PrevTab.Help().Key, "next / prev tab"},
				{k.Retry.Help().Key, "retry load (when error shown)"},
			},
		},
		{
			title: "Navigation in lists",
			bindings: [][2]string{
				{k.Select.Help().Key, "select"},
				{k.Filter.Help().Key, "filter"},
				{k.Back.Help().Key, "back / clear filter"},
				{"1–9", "jump to item"},
				{"↑/↓  k/j", "move cursor"},
			},
		},
		{
			title: "Tables (Pod step)",
			bindings: [][2]string{
				{"/", "filter (enter commits, esc cancels)"},
				{"enter", "select"},
				{"esc", "back / reset filter"},
			},
		},
		{
			title: "Port-forwards tab",
			bindings: [][2]string{
				{"d", "stop forward"},
				{"x", "remove entry"},
				{"l", "toggle log view"},
				{"/", "filter"},
			},
		},
	}
}

// View renders the modal centered on a (w x h) area. Returns "" when hidden.
func (h *Help) View(w, h2 int, s *styles.Styles) string {
	if !h.Visible {
		return ""
	}

	var rows []string
	rows = append(rows, s.Title.Render("Help"))
	rows = append(rows, "")
	for _, sec := range helpSections() {
		rows = append(rows, s.Master.Render(sec.title))
		for _, b := range sec.bindings {
			rows = append(rows, "  "+s.Value.Render(padRight(b[0], 14))+s.Muted.Render(b[1]))
		}
		rows = append(rows, "")
	}
	rows = append(rows, s.Muted.Render("?/esc/q  close"))

	body := strings.Join(rows, "\n")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(s.Palette.Accent()).
		Padding(1, 2).
		Render(body)

	return lipgloss.Place(w, h2, lipgloss.Center, lipgloss.Center, box)
}

func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-w)
}
