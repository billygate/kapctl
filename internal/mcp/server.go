package mcp

import (
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// codeServerClosing is the JSON-RPC error code the SDK returns while the
// session is shutting down (there is no exported constant for it). It
// arrives wrapped in a jsonrpc.Error when a client disconnects with a
// request still in flight — a normal end of a stdio session, not a fault.
const codeServerClosing = -32004

// IsCleanShutdown reports whether a *mcp.Server Run error represents an
// ordinary client disconnect rather than a real failure. A nil error and
// the SDK's "server is closing" error (code -32004, which wraps the stdin
// EOF) both count as clean, so callers can exit 0 without dumping usage.
func IsCleanShutdown(err error) bool {
	if err == nil {
		return true
	}
	var we *jsonrpc.Error
	return errors.As(err, &we) && we.Code == codeServerClosing
}

type server struct {
	deps Deps
	opts Options
}

// NewServer builds an MCP server exposing kapctl's read-only Kubernetes
// tools plus local cluster tools. Mutating local tools are registered
// only when opts.AllowLocalControl is true; local_up/local_down also
// require the spacebox binary to be installed.
func NewServer(deps Deps, opts Options) *mcp.Server {
	s := &server{deps: deps, opts: opts}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "kapctl", Version: version}, nil)

	// Read-only Kubernetes tools (always registered).
	mcp.AddTool(srv, &mcp.Tool{Name: "list_contexts", Description: "List kube contexts and the current one."}, s.listContexts)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_namespaces", Description: "List namespaces in a context."}, s.listNamespaces)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_pods", Description: "List pods in a namespace (name, status, restarts, age, ports)."}, s.listPods)
	mcp.AddTool(srv, &mcp.Tool{Name: "describe_pod", Description: "Get a pod's spilo-role and declared container ports."}, s.describePod)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_pod_logs", Description: "Fetch recent logs for a pod (tail-capped)."}, s.getPodLogs)

	// Local cluster status (read-only, always registered).
	mcp.AddTool(srv, &mcp.Tool{Name: "local_status", Description: "Show local kind container status."}, s.localStatus)

	if opts.AllowLocalControl {
		mcp.AddTool(srv, &mcp.Tool{Name: "local_pause", Description: "Pause running local kind containers."}, s.localPause)
		mcp.AddTool(srv, &mcp.Tool{Name: "local_resume", Description: "Resume paused local kind containers."}, s.localResume)
		if deps.Spacebox.IsInstalled() {
			mcp.AddTool(srv, &mcp.Tool{Name: "local_up", Description: "Bring up the local kind cluster via spacebox."}, s.localUp)
			mcp.AddTool(srv, &mcp.Tool{Name: "local_down", Description: "Tear down the local kind cluster via spacebox."}, s.localDown)
		}
	}
	return srv
}
