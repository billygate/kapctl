// Package docker is the thin wrapper around the Docker SDK used by kap
// to inspect and manage the kind cluster's containers.
package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// clientAPI is the subset of the Docker SDK client used by Client.
// The real *client.Client satisfies this interface automatically.
type clientAPI interface {
	ContainerList(ctx context.Context, opts container.ListOptions) ([]container.Summary, error)
	ContainerPause(ctx context.Context, containerID string) error
	ContainerUnpause(ctx context.Context, containerID string) error
}

// Client wraps the Docker SDK with the small surface kap needs.
type Client struct {
	cli clientAPI
}

// NewClient connects to the local Docker daemon using environment
// defaults and lazy API version negotiation.
func NewClient() (*Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Client{cli: cli}, nil
}

// GetKindContainers returns the names of containers labelled
// io.x-k8s.kind.cluster, optionally filtered by Docker container state.
func (c *Client) GetKindContainers(ctx context.Context, state string) ([]string, error) {
	f := filters.NewArgs()
	f.Add("label", "io.x-k8s.kind.cluster")
	if state != "" {
		f.Add("status", state)
	}

	containers, err := c.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return nil, err
	}

	var names []string
	for _, container := range containers {
		if len(container.Names) > 0 {
			// Remove leading slash
			names = append(names, container.Names[0][1:])
		}
	}
	return names, nil
}

// PauseContainers pauses each named container, stopping at the first error.
func (c *Client) PauseContainers(ctx context.Context, names []string) error {
	for _, name := range names {
		if err := c.cli.ContainerPause(ctx, name); err != nil {
			return fmt.Errorf("failed to pause container %s: %w", name, err)
		}
	}
	return nil
}

// ResumeContainers unpauses each named container, stopping at the first error.
func (c *Client) ResumeContainers(ctx context.Context, names []string) error {
	for _, name := range names {
		if err := c.cli.ContainerUnpause(ctx, name); err != nil {
			return fmt.Errorf("failed to resume container %s: %w", name, err)
		}
	}
	return nil
}

// GetStatus returns structured status (name, status, age) for every
// kind cluster container, regardless of its current state.
func (c *Client) GetStatus(ctx context.Context) ([]ContainerStatus, error) {
	f := filters.NewArgs()
	f.Add("label", "io.x-k8s.kind.cluster")

	containers, err := c.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return nil, err
	}

	var stats []ContainerStatus
	for _, container := range containers {
		if len(container.Names) > 0 {
			age := time.Since(time.Unix(container.Created, 0)).Round(time.Second).String()
			stats = append(stats, ContainerStatus{
				Name:   container.Names[0][1:],
				Status: container.Status,
				Age:    age,
			})
		}
	}
	return stats, nil
}
