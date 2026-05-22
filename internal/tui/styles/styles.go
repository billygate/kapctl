// Package styles bundles the lipgloss styles consumed by the TUI.
// Built once from a Palette and treated as read-only thereafter.
package styles

import (
	"github.com/billygate/kapctl/internal/tui/themes"
	"github.com/charmbracelet/lipgloss"
)

// Styles bundles the lipgloss.Style values used across the TUI. Built
// once from a Palette in New(), then read-only.
type Styles struct {
	Window       lipgloss.Style
	Title        lipgloss.Style
	SelectedItem lipgloss.Style
	Item         lipgloss.Style
	Muted        lipgloss.Style
	Value        lipgloss.Style
	Master       lipgloss.Style
	Warn         lipgloss.Style
	Footer       lipgloss.Style
	ActiveTab    lipgloss.Style
	InactiveTab  lipgloss.Style
	Separator    lipgloss.Style
	Palette      themes.Palette
}

// New builds a Styles bundle from the given palette.
func New(p themes.Palette) *Styles {
	return &Styles{
		Palette: p,
		Window: lipgloss.NewStyle().
			Padding(1, 2).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.Accent()),
		Title: lipgloss.NewStyle().
			Foreground(p.Base()).
			Background(p.Accent()).
			Padding(0, 1).
			Bold(true),
		SelectedItem: lipgloss.NewStyle().Foreground(p.Primary()).Bold(true),
		Item:         lipgloss.NewStyle().Foreground(p.Text()),
		Muted:        lipgloss.NewStyle().Foreground(p.Subtext()),
		Value:        lipgloss.NewStyle().Foreground(p.Text()),
		Master:       lipgloss.NewStyle().Foreground(p.Primary()).Bold(true),
		Warn:         lipgloss.NewStyle().Foreground(p.Warn()),
		Footer:       lipgloss.NewStyle().MarginTop(1),
		ActiveTab: lipgloss.NewStyle().
			Foreground(p.Base()).
			Background(p.Accent()).
			Padding(0, 2).
			Bold(true),
		InactiveTab: lipgloss.NewStyle().
			Foreground(p.Subtext()).
			Background(p.Surface()).
			Padding(0, 2),
		Separator: lipgloss.NewStyle().Foreground(p.Subtext()).Faint(true),
	}
}
