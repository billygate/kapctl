package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyArgs struct{}

func (s *server) localStatus(context.Context, *mcp.CallToolRequest, emptyArgs) (*mcp.CallToolResult, any, error) {
	return jsonResult(map[string]any{"containers": []any{}})
}
func (s *server) localPause(context.Context, *mcp.CallToolRequest, emptyArgs) (*mcp.CallToolResult, any, error) {
	return jsonResult(map[string]any{"paused": []string{}})
}
func (s *server) localResume(context.Context, *mcp.CallToolRequest, emptyArgs) (*mcp.CallToolResult, any, error) {
	return jsonResult(map[string]any{"resumed": []string{}})
}
func (s *server) localUp(context.Context, *mcp.CallToolRequest, emptyArgs) (*mcp.CallToolResult, any, error) {
	return jsonResult(map[string]any{"ok": true})
}
func (s *server) localDown(context.Context, *mcp.CallToolRequest, emptyArgs) (*mcp.CallToolResult, any, error) {
	return jsonResult(map[string]any{"ok": true})
}
