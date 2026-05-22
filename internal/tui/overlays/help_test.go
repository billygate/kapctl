package overlays_test

import (
	"strings"
	"testing"

	"github.com/billygate/kapctl/internal/tui/overlays"
	"github.com/billygate/kapctl/internal/tui/styles"
	"github.com/billygate/kapctl/internal/tui/themes"
)

func newTestStyles(t *testing.T) *styles.Styles {
	t.Helper()
	p, ok := themes.Get("catppuccin")
	if !ok {
		t.Fatal("catppuccin theme not registered")
	}
	return styles.New(p)
}

func TestHelpViewContainsSectionsAndBindings(t *testing.T) {
	s := newTestStyles(t)
	h := &overlays.Help{Visible: true}
	v := h.View(120, 40, s)

	for _, want := range []string{
		// Modal title
		"Help",
		// Section titles
		"Global",
		"Navigation in lists",
		"Tables (Pod step)",
		"Port-forwards tab",
		// Sample bindings from each section
		"?", "enter", "esc", "1–9",
		// Sample descriptions
		"select", "filter", "stop forward",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("Help.View missing %q in:\n%s", want, v)
		}
	}
}

func TestHelpViewEmptyWhenHidden(t *testing.T) {
	s := newTestStyles(t)
	h := &overlays.Help{Visible: false}
	if got := h.View(120, 40, s); got != "" {
		t.Errorf("Help.View when hidden = %q, want empty", got)
	}
}
