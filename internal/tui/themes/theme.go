// Package themes defines the abstract Palette interface and registry
// of named colour palettes (catppuccin, nord) consumed by styles.
package themes

import "github.com/charmbracelet/lipgloss"

// Palette is the abstract color set every theme must provide. Views and
// styles consume only this interface — never raw lipgloss.Color literals.
type Palette interface {
	Base() lipgloss.Color
	Text() lipgloss.Color
	Subtext() lipgloss.Color
	Accent() lipgloss.Color    // mauve in catppuccin — titles, prompts
	Primary() lipgloss.Color   // peach — selected, master
	Secondary() lipgloss.Color // blue — replica, info
	Warn() lipgloss.Color      // red — errors
	Surface() lipgloss.Color   // border, inactive tab bg

	// PodStatus returns a color for a pod phase string.
	// Recognised values: "Running", "Pending", "Failed", "Error".
	// Anything else returns the Text color.
	PodStatus(phase string) lipgloss.Color

	// SpiloRole returns a color for a Patroni/Spilo role label.
	// Recognised values: "master", "replica", "unknown".
	SpiloRole(role string) lipgloss.Color
}

var registry = map[string]Palette{}

// Register installs a theme in the registry, called from each theme's
// init(). Re-registration overwrites silently — last init wins.
func Register(name string, p Palette) { registry[name] = p }

// Get looks up a theme by name. Returns (nil, false) if absent.
func Get(name string) (Palette, bool) { p, ok := registry[name]; return p, ok }

// Names returns the registered theme names.
func Names() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}
