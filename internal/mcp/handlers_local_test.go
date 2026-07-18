package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type recordingDocker struct {
	running []string
	paused  []string
	pausedC []string
	resumeC []string
}

func (d *recordingDocker) GetStatus(context.Context) ([]docker.ContainerStatus, error) {
	return []docker.ContainerStatus{{Name: "kind-control-plane", Status: "running", Age: "1h"}}, nil
}
func (d *recordingDocker) GetKindContainers(_ context.Context, state string) ([]string, error) {
	if state == "running" {
		return d.running, nil
	}
	return d.paused, nil
}
func (d *recordingDocker) PauseContainers(_ context.Context, names []string) error {
	d.pausedC = names
	return nil
}
func (d *recordingDocker) ResumeContainers(_ context.Context, names []string) error {
	d.resumeC = names
	return nil
}

func callLocal(t *testing.T, srv *mcp.Server, name string) *mcp.CallToolResult {
	t.Helper()
	cs := connect(t, srv)
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func TestLocalStatus(t *testing.T) {
	srv := NewServer(newTestDeps(true), Options{})
	res := callLocal(t, srv, "local_status")
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	tc := res.Content[0].(*mcp.TextContent)
	var payload struct {
		Containers []docker.ContainerStatus `json:"containers"`
	}
	if err := json.Unmarshal([]byte(tc.Text), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Containers) != 1 || payload.Containers[0].Name != "kind-control-plane" {
		t.Fatalf("containers = %+v", payload.Containers)
	}
}

func TestLocalPausePausesRunningContainers(t *testing.T) {
	rd := &recordingDocker{running: []string{"c1", "c2"}}
	deps := newTestDeps(true)
	deps.Docker = rd
	srv := NewServer(deps, Options{AllowLocalControl: true})
	res := callLocal(t, srv, "local_pause")
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if len(rd.pausedC) != 2 || rd.pausedC[0] != "c1" {
		t.Fatalf("paused = %+v, want [c1 c2]", rd.pausedC)
	}
}

type failSpacebox struct{}

func (failSpacebox) IsInstalled() bool { return true }
func (failSpacebox) Up() error         { return errors.New("boom") }
func (failSpacebox) Down() error       { return nil }

func TestLocalUpSurfacesError(t *testing.T) {
	deps := newTestDeps(true)
	deps.Spacebox = failSpacebox{}
	srv := NewServer(deps, Options{AllowLocalControl: true})
	res := callLocal(t, srv, "local_up")
	if !res.IsError {
		t.Fatalf("expected IsError for failing spacebox up")
	}
}
