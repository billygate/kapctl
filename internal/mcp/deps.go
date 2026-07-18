// Package mcp exposes a subset of kapctl's capabilities as Model Context
// Protocol tools over stdio. It depends on internal/{kube,docker,spacebox}
// through small interfaces so handlers are unit-testable with fakes.
package mcp

import (
	"context"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/kube"
)

// KubeAPI is the subset of *kube.Client the handlers use.
type KubeAPI interface {
	GetContexts() []string
	GetCurrentContext() string
	GetNamespaces(ctx context.Context) ([]string, error)
	GetPods(ctx context.Context, namespace string) ([]kube.PodInfo, error)
	GetPodRole(ctx context.Context, namespace, pod string) (string, error)
	GetPodPorts(ctx context.Context, namespace, pod string) ([]kube.ContainerPort, error)
	GetPodLogs(ctx context.Context, namespace, pod string, opts kube.LogOptions) (string, error)
}

// DockerAPI is the subset of *docker.Client the handlers use.
type DockerAPI interface {
	GetStatus(ctx context.Context) ([]docker.ContainerStatus, error)
	GetKindContainers(ctx context.Context, state string) ([]string, error)
	PauseContainers(ctx context.Context, names []string) error
	ResumeContainers(ctx context.Context, names []string) error
}

// SpaceboxAPI wraps the spacebox package functions for testability.
type SpaceboxAPI interface {
	IsInstalled() bool
	Up() error
	Down() error
}

// Deps holds the runtime dependencies the tool handlers call.
type Deps struct {
	// NewKube builds a KubeAPI for the given context name ("" = current).
	NewKube  func(contextName string) (KubeAPI, error)
	Docker   DockerAPI
	Spacebox SpaceboxAPI
}

// Options configures which tools are registered.
type Options struct {
	AllowLocalControl bool
	Version           string
}
