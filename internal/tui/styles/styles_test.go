package styles_test

import (
	"testing"

	"github.com/billygate/kapctl/internal/tui/styles"
	"github.com/billygate/kapctl/internal/tui/themes"
)

func TestNewReturnsNonNilStyles(t *testing.T) {
	palette, ok := themes.Get("catppuccin")
	if !ok {
		t.Fatal("themes.Get(catppuccin) not found")
	}
	s := styles.New(palette)
	if s == nil {
		t.Fatal("styles.New returned nil")
	}
}

func TestNewPreservesPalette(t *testing.T) {
	palette, ok := themes.Get("nord")
	if !ok {
		t.Fatal("themes.Get(nord) not found")
	}
	s := styles.New(palette)
	if s.Palette != palette {
		t.Error("Palette field not set correctly")
	}
}
