package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/spacebox"
	"github.com/spf13/cobra"
)

var locCmd = &cobra.Command{
	Use:   "loc",
	Short: "manage local kind cluster",
	Run: func(cmd *cobra.Command, _ []string) {
		_ = cmd.Help()
	},
}

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "cluster up",
	RunE: func(_ *cobra.Command, _ []string) error {
		if !spacebox.IsInstalled() {
			fmt.Println(loadedStyles.Warn.Render("spacebox is not installed"))
			return nil
		}
		fmt.Println(loadedStyles.Muted.Render("spacebox cluster up"))
		return spacebox.Up()
	},
}

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "cluster down",
	RunE: func(_ *cobra.Command, _ []string) error {
		if !spacebox.IsInstalled() {
			fmt.Println(loadedStyles.Warn.Render("spacebox is not installed"))
			return nil
		}
		fmt.Println(loadedStyles.Muted.Render("spacebox cluster down"))
		return spacebox.Down()
	},
}

var pauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "docker pause kind containers",
	RunE: func(_ *cobra.Command, _ []string) error {
		cli, err := docker.NewClient()
		if err != nil {
			return err
		}
		names, err := cli.GetKindContainers(context.Background(), "running")
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println(loadedStyles.Muted.Render("No running kind containers"))
			return nil
		}
		fmt.Printf("%s %s\n", loadedStyles.Muted.Render("Pausing:"), loadedStyles.Value.Render(strings.Join(names, ", ")))
		if err := cli.PauseContainers(context.Background(), names); err != nil {
			return err
		}
		fmt.Printf("%s %s\n", loadedStyles.Master.Render("✓"), loadedStyles.Muted.Render(fmt.Sprintf("paused %d container(s)", len(names))))
		return nil
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "docker unpause kind containers",
	RunE: func(_ *cobra.Command, _ []string) error {
		cli, err := docker.NewClient()
		if err != nil {
			return err
		}
		names, err := cli.GetKindContainers(context.Background(), "paused")
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println(loadedStyles.Muted.Render("No paused kind containers"))
			return nil
		}
		fmt.Printf("%s %s\n", loadedStyles.Muted.Render("Resuming:"), loadedStyles.Value.Render(strings.Join(names, ", ")))
		if err := cli.ResumeContainers(context.Background(), names); err != nil {
			return err
		}
		fmt.Printf("%s %s\n", loadedStyles.Master.Render("✓"), loadedStyles.Muted.Render(fmt.Sprintf("resumed %d container(s)", len(names))))
		return nil
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "docker restart kind containers",
	RunE: func(_ *cobra.Command, _ []string) error {
		cli, err := docker.NewClient()
		if err != nil {
			return err
		}
		names, err := cli.GetKindContainers(context.Background(), "running")
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println(loadedStyles.Muted.Render("No running kind containers"))
			return nil
		}
		fmt.Printf("%s %s\n", loadedStyles.Muted.Render("Restarting:"), loadedStyles.Value.Render(strings.Join(names, ", ")))
		if err := cli.RestartContainers(context.Background(), names); err != nil {
			return err
		}
		fmt.Printf("%s %s\n", loadedStyles.Master.Render("✓"), loadedStyles.Muted.Render(fmt.Sprintf("restarted %d container(s)", len(names))))
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "show containers state",
	RunE: func(_ *cobra.Command, _ []string) error {
		cli, err := docker.NewClient()
		if err != nil {
			return err
		}
		stats, err := cli.GetStatus(context.Background())
		if err != nil {
			return err
		}
		if len(stats) == 0 {
			fmt.Println(loadedStyles.Muted.Render("No kind containers found (cluster is down)"))
			return nil
		}

		fmt.Println()
		for _, s := range stats {
			marker := loadedStyles.Master.Render("●")
			if strings.Contains(s.Status, "Paused") {
				marker = loadedStyles.Muted.Render("○")
			}
			fmt.Printf("  %s %s %s\n", marker, loadedStyles.Value.Render(s.Name), loadedStyles.Muted.Render("("+s.Status+")"))
		}
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(locCmd)
	locCmd.AddCommand(upCmd)
	locCmd.AddCommand(downCmd)
	locCmd.AddCommand(pauseCmd)
	locCmd.AddCommand(resumeCmd)
	locCmd.AddCommand(restartCmd)
	locCmd.AddCommand(statusCmd)
}
