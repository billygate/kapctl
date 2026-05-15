package themes

import "github.com/charmbracelet/lipgloss"

type nord struct{}

func (nord) Base() lipgloss.Color      { return lipgloss.Color("#2e3440") }
func (nord) Text() lipgloss.Color      { return lipgloss.Color("#eceff4") }
func (nord) Subtext() lipgloss.Color   { return lipgloss.Color("#d8dee9") }
func (nord) Accent() lipgloss.Color    { return lipgloss.Color("#b48ead") } // purple
func (nord) Primary() lipgloss.Color   { return lipgloss.Color("#d08770") } // orange
func (nord) Secondary() lipgloss.Color { return lipgloss.Color("#81a1c1") } // frost-blue
func (nord) Warn() lipgloss.Color      { return lipgloss.Color("#bf616a") } // aurora-red
func (nord) Surface() lipgloss.Color   { return lipgloss.Color("#3b4252") }

func (n nord) PodStatus(phase string) lipgloss.Color {
	switch phase {
	case "Running":
		return lipgloss.Color("#a3be8c") // green
	case "Pending":
		return lipgloss.Color("#ebcb8b") // yellow
	case "Failed", "Error":
		return n.Warn()
	default:
		return n.Text()
	}
}

func (n nord) SpiloRole(role string) lipgloss.Color {
	switch role {
	case "master":
		return n.Primary()
	case "replica":
		return n.Secondary()
	default:
		return n.Subtext()
	}
}

func init() { Register("nord", nord{}) }
