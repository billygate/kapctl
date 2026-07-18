package main

import (
	"context"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/kube"
	appmcp "github.com/billygate/kapctl/internal/mcp"
	"github.com/billygate/kapctl/internal/spacebox"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

var mcpAllowLocalControl bool

// spaceboxAdapter adapts the spacebox package functions to mcp.SpaceboxAPI.
type spaceboxAdapter struct{}

func (spaceboxAdapter) IsInstalled() bool { return spacebox.IsInstalled() }
func (spaceboxAdapter) Up() error         { return spacebox.Up() }
func (spaceboxAdapter) Down() error       { return spacebox.Down() }

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "run kapctl as an MCP server over stdio",
	Long: "Run kapctl as a Model Context Protocol server on stdio. Exposes " +
		"read-only Kubernetes introspection and local kind cluster status. " +
		"Pass --allow-local-control to also expose pause/resume/up/down.",
	RunE: func(_ *cobra.Command, _ []string) error {
		dockerClient, err := docker.NewClient()
		if err != nil {
			return err
		}
		deps := appmcp.Deps{
			NewKube: func(name string) (appmcp.KubeAPI, error) {
				return kube.NewClient(name)
			},
			Docker:   dockerClient,
			Spacebox: spaceboxAdapter{},
		}
		srv := appmcp.NewServer(deps, appmcp.Options{
			AllowLocalControl: mcpAllowLocalControl,
			Version:           version,
		})
		return srv.Run(context.Background(), &mcp.StdioTransport{})
	},
}

func init() {
	mcpCmd.Flags().BoolVar(&mcpAllowLocalControl, "allow-local-control", false,
		"expose mutating local cluster tools (pause/resume/up/down)")
	rootCmd.AddCommand(mcpCmd)
}
