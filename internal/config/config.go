// Package config loads and persists the kap user config at
// $HOME/.config/kap/config.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the persistent settings file loaded from
// $HOME/.config/kap/config.yaml.
type Config struct {
	Theme string         `yaml:"theme"`
	Ports map[string]int `yaml:"ports"` // key: "<context>.<namespace>"
}

// validThemes is checked against the registered theme names. If the
// config asks for a theme that isn't registered, Load falls back to
// "catppuccin" and emits a warning.
var validThemes = map[string]struct{}{
	"catppuccin": {},
	"nord":       {},
}

const defaultTheme = "catppuccin"

// Load reads the user config from the conventional location.
// Missing file is not an error — returns defaults.
func Load() (*Config, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(home, ".config", "kap", "config.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return defaults(), nil, nil
	}
	return LoadFile(path)
}

// LoadFile is Load with an explicit path — used by tests.
func LoadFile(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Ports == nil {
		cfg.Ports = map[string]int{}
	}

	var warnings []string
	if cfg.Theme == "" {
		cfg.Theme = defaultTheme
	} else if _, ok := validThemes[cfg.Theme]; !ok {
		warnings = append(warnings, fmt.Sprintf("unknown theme %q, falling back to %q", cfg.Theme, defaultTheme))
		cfg.Theme = defaultTheme
	}
	return cfg, warnings, nil
}

func defaults() *Config {
	return &Config{Theme: defaultTheme, Ports: map[string]int{}}
}

// portKey returns the "<context>.<namespace>" key used in the Ports map.
func portKey(ctx, ns string) string { return ctx + "." + ns }

// GetPort returns the cached port for the given context+namespace, or 0
// if not set.
func (c *Config) GetPort(ctx, ns string) int {
	if c.Ports == nil {
		return 0
	}
	return c.Ports[portKey(ctx, ns)]
}

// SetPort caches a port for the given context+namespace.
func (c *Config) SetPort(ctx, ns string, port int) {
	if c.Ports == nil {
		c.Ports = map[string]int{}
	}
	c.Ports[portKey(ctx, ns)] = port
}

// Save writes the config back to $HOME/.config/kap/config.yaml,
// creating directories as needed.
func (c *Config) Save() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "kap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644)
}
