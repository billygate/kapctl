package themes

import "github.com/charmbracelet/lipgloss"

type catppuccin struct{}

func (catppuccin) Base() lipgloss.Color      { return lipgloss.Color("#1e1e2e") }
func (catppuccin) Text() lipgloss.Color      { return lipgloss.Color("#cdd6f4") }
func (catppuccin) Subtext() lipgloss.Color   { return lipgloss.Color("#a6adc8") }
func (catppuccin) Accent() lipgloss.Color    { return lipgloss.Color("#cba6f7") } // mauve
func (catppuccin) Primary() lipgloss.Color   { return lipgloss.Color("#fab387") } // peach
func (catppuccin) Secondary() lipgloss.Color { return lipgloss.Color("#89b4fa") } // blue
func (catppuccin) Warn() lipgloss.Color      { return lipgloss.Color("#f38ba8") } // red
func (catppuccin) Surface() lipgloss.Color   { return lipgloss.Color("#313244") }

func (c catppuccin) PodStatus(phase string) lipgloss.Color {
	switch phase {
	case "Running":
		return lipgloss.Color("#a6e3a1") // green
	case "Pending":
		return lipgloss.Color("#f9e2af") // yellow
	case "Failed", "Error":
		return c.Warn()
	default:
		return c.Text()
	}
}

func (c catppuccin) SpiloRole(role string) lipgloss.Color {
	switch role {
	case "master":
		return c.Primary()
	case "replica":
		return c.Secondary()
	default:
		return c.Subtext()
	}
}

func init() { Register("catppuccin", catppuccin{}) }
