package themes_test

import (
	"testing"

	"github.com/billygate/kapctl/internal/tui/themes"
	"github.com/charmbracelet/lipgloss"
)

func TestRegistryComplete(t *testing.T) {
	want := []string{"catppuccin", "nord"}
	for _, name := range want {
		if _, ok := themes.Get(name); !ok {
			t.Errorf("theme %q not registered", name)
		}
	}
}

func TestPaletteCompleteness(t *testing.T) {
	for _, name := range themes.Names() {
		t.Run(name, func(t *testing.T) {
			p, _ := themes.Get(name)
			checks := map[string]lipgloss.Color{
				"Base":      p.Base(),
				"Text":      p.Text(),
				"Subtext":   p.Subtext(),
				"Accent":    p.Accent(),
				"Primary":   p.Primary(),
				"Secondary": p.Secondary(),
				"Warn":      p.Warn(),
				"Surface":   p.Surface(),
			}
			for label, c := range checks {
				if c == "" {
					t.Errorf("%s.%s is empty", name, label)
				}
			}
			for _, phase := range []string{"Running", "Pending", "Failed", "Error", "Unknown"} {
				if c := p.PodStatus(phase); c == "" {
					t.Errorf("%s.PodStatus(%q) is empty", name, phase)
				}
			}
			for _, role := range []string{"master", "replica", "unknown", "weird"} {
				if c := p.SpiloRole(role); c == "" {
					t.Errorf("%s.SpiloRole(%q) is empty", name, role)
				}
			}
		})
	}
}
