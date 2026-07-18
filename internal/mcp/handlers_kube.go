package mcp

import (
	"context"

	"github.com/billygate/kapctl/internal/kube"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ctxArgs struct {
	Context string `json:"context,omitempty" jsonschema:"kube context name; empty uses the current context"`
}

type nsArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"kube context name; empty uses the current context"`
	Namespace string `json:"namespace" jsonschema:"namespace name"`
}

type podArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"kube context name; empty uses the current context"`
	Namespace string `json:"namespace" jsonschema:"namespace name"`
	Pod       string `json:"pod" jsonschema:"pod name"`
}

type logsArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"kube context name; empty uses the current context"`
	Namespace string `json:"namespace" jsonschema:"namespace name"`
	Pod       string `json:"pod" jsonschema:"pod name"`
	Container string `json:"container,omitempty" jsonschema:"container name; empty uses the default container"`
	TailLines int    `json:"tailLines,omitempty" jsonschema:"max lines to return from the tail; default 200"`
}

func (s *server) listContexts(_ context.Context, _ *mcp.CallToolRequest, _ ctxArgs) (*mcp.CallToolResult, any, error) {
	kc, err := s.deps.NewKube("")
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	return jsonResult(map[string]any{
		"current":  kc.GetCurrentContext(),
		"contexts": kc.GetContexts(),
	})
}

func (s *server) listNamespaces(ctx context.Context, _ *mcp.CallToolRequest, args ctxArgs) (*mcp.CallToolResult, any, error) {
	kc, err := s.deps.NewKube(args.Context)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	ns, err := kc.GetNamespaces(ctx)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	return jsonResult(map[string]any{"namespaces": ns})
}

func (s *server) listPods(ctx context.Context, _ *mcp.CallToolRequest, args nsArgs) (*mcp.CallToolResult, any, error) {
	kc, err := s.deps.NewKube(args.Context)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	pods, err := kc.GetPods(ctx, args.Namespace)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	return jsonResult(map[string]any{"pods": pods})
}

func (s *server) describePod(ctx context.Context, _ *mcp.CallToolRequest, args podArgs) (*mcp.CallToolResult, any, error) {
	kc, err := s.deps.NewKube(args.Context)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	role, err := kc.GetPodRole(ctx, args.Namespace, args.Pod)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	ports, err := kc.GetPodPorts(ctx, args.Namespace, args.Pod)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	return jsonResult(map[string]any{
		"pod":   args.Pod,
		"role":  role,
		"ports": ports,
	})
}

func (s *server) getPodLogs(ctx context.Context, _ *mcp.CallToolRequest, args logsArgs) (*mcp.CallToolResult, any, error) {
	kc, err := s.deps.NewKube(args.Context)
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	tail := args.TailLines
	if tail <= 0 {
		tail = 200
	}
	out, err := kc.GetPodLogs(ctx, args.Namespace, args.Pod, kube.LogOptions{
		Container: args.Container,
		TailLines: tail,
	})
	if err != nil {
		return nil, nil, kubeErr(err)
	}
	return jsonResult(map[string]any{"logs": out})
}
