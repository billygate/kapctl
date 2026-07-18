package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// jsonResult marshals v to indented JSON and wraps it as a tool result.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, nil, nil
}

// kubeErr annotates a Kubernetes access error with the same hint the
// `ctrl` subcommand shows, so the client sees an actionable message.
func kubeErr(err error) error {
	return fmt.Errorf("%w (check VPN / SSO / auth)", err)
}
