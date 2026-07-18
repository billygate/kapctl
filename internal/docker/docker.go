// Package docker is the thin wrapper around the Docker SDK used by kap
// to inspect and manage the kind cluster's containers.
package docker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// NewClient connects to the local Docker daemon.
//
// The Docker SDK's client.FromEnv only honors the DOCKER_HOST environment
// variable and otherwise falls back to the hard-coded unix:///var/run/docker.sock,
// which breaks for users running OrbStack, colima, or a non-default Docker
// Desktop socket — their daemon is reachable only through a Docker CLI
// *context*, which FromEnv ignores. When DOCKER_HOST is unset we replicate the
// CLI's behavior: resolve the active context's endpoint and connect there.
func NewClient() (*Client, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if os.Getenv("DOCKER_HOST") == "" {
		configDir := dockerConfigDir()
		if host, err := hostForContext(configDir, resolveContextName(configDir)); err == nil && host != "" {
			opts = append(opts, client.WithHost(host))
		}
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &Client{cli: cli}, nil
}

// dockerConfigDir returns the Docker CLI config directory, honoring
// DOCKER_CONFIG and falling back to ~/.docker.
func dockerConfigDir() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker")
}

// resolveContextName returns the active Docker context name using the same
// precedence as the docker CLI: the DOCKER_CONTEXT environment variable, then
// currentContext in config.json. Returns "" (the default context) when neither
// is set or config.json is absent/unreadable.
func resolveContextName(configDir string) string {
	if name := os.Getenv("DOCKER_CONTEXT"); name != "" {
		return name
	}
	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.CurrentContext
}

// hostForContext reads the docker endpoint host for the named context from the
// CLI context metadata store (contexts/meta/<sha256(name)>/meta.json). It
// returns "" (meaning "use the SDK default") for the empty or "default"
// context, and an error if a named context's metadata cannot be read.
func hostForContext(configDir, name string) (string, error) {
	if name == "" || name == "default" {
		return "", nil
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	metaPath := filepath.Join(configDir, "contexts", "meta", digest, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", err
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", err
	}
	return meta.Endpoints.Docker.Host, nil
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
