package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/billygate/kap-toolsbox/internal/config"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		wantTheme string
		wantWarn  bool
	}{
		{"valid", "valid.yaml", "nord", false},
		{"bad theme falls back to catppuccin with warning", "bad_theme.yaml", "catppuccin", true},
		{"minimal config defaults to catppuccin", "minimal.yaml", "catppuccin", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("testdata", tt.fixture)
			cfg, warnings, err := config.LoadFile(path)
			if err != nil {
				t.Fatalf("LoadFile(%s): %v", path, err)
			}
			if cfg.Theme != tt.wantTheme {
				t.Errorf("theme = %q, want %q", cfg.Theme, tt.wantTheme)
			}
			gotWarn := len(warnings) > 0
			if gotWarn != tt.wantWarn {
				t.Errorf("warnings present = %v, want %v (warnings=%v)", gotWarn, tt.wantWarn, warnings)
			}
		})
	}
}

func TestPortCacheRoundtrip(t *testing.T) {
	cfg := &config.Config{Theme: "catppuccin", Ports: map[string]int{}}
	cfg.SetPort("ctx-a", "ns-x", 5432)
	if got := cfg.GetPort("ctx-a", "ns-x"); got != 5432 {
		t.Errorf("GetPort = %d, want 5432", got)
	}
	if got := cfg.GetPort("ctx-a", "ns-y"); got != 0 {
		t.Errorf("GetPort for missing key = %d, want 0", got)
	}
}

func TestGetPortWithNilPorts(t *testing.T) {
	cfg := &config.Config{Theme: "catppuccin"}
	// Ports is nil — GetPort must not panic
	if got := cfg.GetPort("ctx", "ns"); got != 0 {
		t.Errorf("GetPort with nil Ports = %d, want 0", got)
	}
}

func TestSetPortInitializesNilMap(t *testing.T) {
	cfg := &config.Config{Theme: "catppuccin"}
	// Ports is nil — SetPort must initialise it
	cfg.SetPort("ctx", "ns", 9999)
	if got := cfg.GetPort("ctx", "ns"); got != 9999 {
		t.Errorf("GetPort after SetPort on nil map = %d, want 9999", got)
	}
}

func TestLoadFileMissingFile(t *testing.T) {
	_, _, err := config.LoadFile(filepath.Join("testdata", "nonexistent.yaml"))
	if err == nil {
		t.Error("LoadFile for non-existent file should return an error")
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	// Override HOME so Save() writes to our temp dir
	t.Setenv("HOME", dir)

	cfg := &config.Config{Theme: "nord", Ports: map[string]int{"ctx.ns": 5432}}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	savedPath := filepath.Join(dir, ".config", "kapctl", "config.yaml")
	if _, err := os.Stat(savedPath); err != nil {
		t.Fatalf("saved file not found at %s: %v", savedPath, err)
	}

	loaded, warnings, err := config.LoadFile(savedPath)
	if err != nil {
		t.Fatalf("LoadFile after Save: %v", err)
	}
	if len(warnings) > 0 {
		t.Errorf("unexpected warnings after reload: %v", warnings)
	}
	if loaded.Theme != "nord" {
		t.Errorf("Theme = %q, want nord", loaded.Theme)
	}
	if loaded.GetPort("ctx", "ns") != 5432 {
		t.Errorf("GetPort after reload = %d, want 5432", loaded.GetPort("ctx", "ns"))
	}
}
