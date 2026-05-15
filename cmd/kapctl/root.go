package main

import (
	"fmt"
	"os"

	"github.com/billygate/kap-toolsbox/internal/config"
	"github.com/billygate/kap-toolsbox/internal/tui"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
	"github.com/billygate/kap-toolsbox/internal/tui/themes"
	"github.com/spf13/cobra"
)

var (
	loadedConfig *config.Config
	loadedStyles *styles.Styles
)

var rootCmd = &cobra.Command{
	Use:   "kapctl",
	Short: "kapctl is a CLI toolbox for Kubernetes and local clusters",
	Long:  `A consolidated CLI tool for managing Kubernetes resources and local kind clusters.`,
	RunE: func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return tui.RunApp(loadedConfig)
		}
		return nil
	},
}

func Execute() error {
	cfg, warnings, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		cfg = &config.Config{Theme: "catppuccin"}
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "config: %s\n", w)
	}
	loadedConfig = cfg

	palette, ok := themes.Get(cfg.Theme)
	if !ok {
		palette, _ = themes.Get("catppuccin")
	}
	loadedStyles = styles.New(palette)

	return rootCmd.Execute()
}
