package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// emptyArgs is the input for local tools that take no parameters.
type emptyArgs struct{}

func (s *server) localStatus(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	statuses, err := s.deps.Docker.GetStatus(ctx)
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"containers": statuses})
}

func (s *server) localPause(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	names, err := s.deps.Docker.GetKindContainers(ctx, "running")
	if err != nil {
		return nil, nil, err
	}
	if err := s.deps.Docker.PauseContainers(ctx, names); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"paused": names})
}

func (s *server) localResume(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	names, err := s.deps.Docker.GetKindContainers(ctx, "paused")
	if err != nil {
		return nil, nil, err
	}
	if err := s.deps.Docker.ResumeContainers(ctx, names); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"resumed": names})
}

func (s *server) localUp(_ context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	if err := s.deps.Spacebox.Up(); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"ok": true, "action": "up"})
}

func (s *server) localDown(_ context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	if err := s.deps.Spacebox.Down(); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"ok": true, "action": "down"})
}
