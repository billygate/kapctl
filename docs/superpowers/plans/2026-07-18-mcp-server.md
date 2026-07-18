# kapctl MCP Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `kapctl mcp` subcommand that exposes read-only Kubernetes introspection and local kind/Docker cluster control as MCP tools over stdio.

**Architecture:** A new `internal/mcp` package builds an official-SDK `*mcp.Server` and registers thin tool handlers that call the existing `internal/{kube,docker,spacebox}` wrappers through small fake-able interfaces. A new `cmd/kapctl/mcp.go` Cobra subcommand wires real dependencies and runs the server over stdio. Mutating local tools are gated behind `--allow-local-control`.

**Tech Stack:** Go 1.26, `github.com/modelcontextprotocol/go-sdk` (official MCP SDK), Cobra, client-go, Docker SDK.

## Global Constraints

- Module path: `github.com/billygate/kapctl`. Go 1.26.2.
- Build only via `make build` → `bin/kapctl`. Never `go build -o ./kapctl` at repo root.
- MCP SDK import path: `github.com/modelcontextprotocol/go-sdk/mcp`.
- `internal/mcp` must NOT import `cmd/` or `internal/tui`. It may import `internal/{kube,docker,spacebox}`.
- No `fmt.Println` from library code in `internal/mcp` (it speaks JSON-RPC on stdout — printing corrupts the stream). Only `cmd/kapctl/mcp.go` may print to stderr before the server starts.
- Read-only by default. `local_pause`/`local_resume`/`local_up`/`local_down` are registered only when `Options.AllowLocalControl` is true; `local_up`/`local_down` additionally require `deps.Spacebox.IsInstalled()`.
- No `exec`, pod `delete`, or port-forward tools.
- Tool handler signature (SDK): `func(ctx context.Context, req *mcp.CallToolRequest, args In) (*mcp.CallToolResult, any, error)`. Returning a non-nil error makes the SDK produce an `IsError: true` tool result automatically.

## File Structure

- `internal/kube/logs.go` (create) — `GetPodLogs` + `LogOptions` + `capLines` helper.
- `internal/kube/logs_test.go` (create) — tests for `capLines` and `GetPodLogs` via fake clientset.
- `internal/mcp/deps.go` (create) — `KubeAPI`, `DockerAPI`, `SpaceboxAPI` interfaces, `Deps`, `Options`.
- `internal/mcp/result.go` (create) — `jsonResult`, `kubeErr` helpers.
- `internal/mcp/server.go` (create) — `NewServer`, the `server` struct, tool registration.
- `internal/mcp/handlers_kube.go` (create) — read-only k8s handlers.
- `internal/mcp/handlers_local.go` (create) — local cluster handlers.
- `internal/mcp/server_test.go` (create) — in-memory round-trip + gating tests with fakes.
- `internal/mcp/handlers_local_test.go` (create) — local handler tests with fakes.
- `cmd/kapctl/mcp.go` (create) — `kapctl mcp [--allow-local-control]` subcommand.
- `go.mod` / `go.sum` (modify) — add the SDK dependency.
- `README.md` (modify) — client config snippet.

---

### Task 1: Add SDK dependency and `kube.GetPodLogs`

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/kube/logs.go`
- Test: `internal/kube/logs_test.go`

**Interfaces:**
- Consumes: `kube.Client{ Clientset kubernetes.Interface }` (existing).
- Produces:
  - `type LogOptions struct { Container string; TailLines int }`
  - `func (c *Client) GetPodLogs(ctx context.Context, namespace, pod string, opts LogOptions) (string, error)`
  - `func capLines(s string, n int) string` (unexported helper; caps to the last `n` lines when `n > 0`).

- [ ] **Step 1: Add the MCP SDK dependency**

Run:
```bash
cd /Users/7424d/Dev/kapctl && go get github.com/modelcontextprotocol/go-sdk@latest && go mod tidy
```
Expected: `go.mod` gains a `github.com/modelcontextprotocol/go-sdk vX.Y.Z` require line; `go.sum` updated. No build errors.

- [ ] **Step 2: Write the failing test for `capLines` and `GetPodLogs`**

Create `internal/kube/logs_test.go`:
```go
package kube

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCapLines(t *testing.T) {
	in := "l1\nl2\nl3\nl4\n"
	if got := capLines(in, 2); got != "l3\nl4" {
		t.Fatalf("capLines last-2 = %q, want %q", got, "l3\nl4")
	}
	if got := capLines(in, 0); got != in {
		t.Fatalf("capLines n=0 should be identity, got %q", got)
	}
	if got := capLines("only", 5); got != "only" {
		t.Fatalf("capLines fewer-than-n = %q, want %q", got, "only")
	}
}

func TestGetPodLogs(t *testing.T) {
	cs := fake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "ns"},
	})
	c := &Client{Clientset: cs}
	out, err := c.GetPodLogs(context.Background(), "ns", "p1", LogOptions{TailLines: 100})
	if err != nil {
		t.Fatalf("GetPodLogs error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("GetPodLogs returned empty output")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/kube/ -run 'TestCapLines|TestGetPodLogs' -v`
Expected: FAIL — `undefined: capLines` / `c.GetPodLogs undefined`.

- [ ] **Step 4: Implement `GetPodLogs` and `capLines`**

Create `internal/kube/logs.go`:
```go
package kube

import (
	"context"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// LogOptions controls a GetPodLogs request. TailLines <= 0 means "no cap".
type LogOptions struct {
	Container string
	TailLines int
}

// GetPodLogs streams the named pod's logs and returns them as a string,
// capped to the last opts.TailLines lines. Container selects a specific
// container; empty means the pod's default container.
func (c *Client) GetPodLogs(ctx context.Context, namespace, pod string, opts LogOptions) (string, error) {
	logOpts := &corev1.PodLogOptions{}
	if opts.Container != "" {
		logOpts.Container = opts.Container
	}
	if opts.TailLines > 0 {
		tl := int64(opts.TailLines)
		logOpts.TailLines = &tl
	}

	req := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, logOpts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return capLines(string(data), opts.TailLines), nil
}

// capLines returns the last n non-trailing lines of s. n <= 0 is identity.
func capLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/kube/ -run 'TestCapLines|TestGetPodLogs' -v`
Expected: PASS (both tests).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/kube/logs.go internal/kube/logs_test.go
git commit -m "kube: add GetPodLogs and MCP SDK dependency"
```

---

### Task 2: `internal/mcp` foundation, read-only k8s tools, server

**Files:**
- Create: `internal/mcp/deps.go`, `internal/mcp/result.go`, `internal/mcp/server.go`, `internal/mcp/handlers_kube.go`
- Test: `internal/mcp/server_test.go`

**Interfaces:**
- Consumes: `kube.PodInfo`, `kube.ContainerPort`, `kube.LogOptions` (Task 1), the MCP SDK.
- Produces:
  - `type KubeAPI interface { ... }`, `type DockerAPI interface { ... }`, `type SpaceboxAPI interface { ... }`
  - `type Deps struct { NewKube func(context string) (KubeAPI, error); Docker DockerAPI; Spacebox SpaceboxAPI }`
  - `type Options struct { AllowLocalControl bool; Version string }`
  - `func NewServer(deps Deps, opts Options) *mcp.Server`
  - unexported `server` struct with the read-only handlers used again in Task 3.

- [ ] **Step 1: Write `deps.go` (interfaces, Deps, Options)**

Create `internal/mcp/deps.go`:
```go
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
```

- [ ] **Step 2: Write `result.go` (helpers)**

Create `internal/mcp/result.go`:
```go
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
```

- [ ] **Step 3: Write `handlers_kube.go` (read-only handlers)**

Create `internal/mcp/handlers_kube.go`:
```go
package mcp

import (
	"context"

	"github.com/billygate/kapctl/internal/kube"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ctxArgs struct {
	Context string `json:"context" jsonschema:"kube context name; empty uses the current context"`
}

type nsArgs struct {
	Context   string `json:"context" jsonschema:"kube context name; empty uses the current context"`
	Namespace string `json:"namespace" jsonschema:"namespace name"`
}

type podArgs struct {
	Context   string `json:"context" jsonschema:"kube context name; empty uses the current context"`
	Namespace string `json:"namespace" jsonschema:"namespace name"`
	Pod       string `json:"pod" jsonschema:"pod name"`
}

type logsArgs struct {
	Context   string `json:"context" jsonschema:"kube context name; empty uses the current context"`
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
```

- [ ] **Step 4: Write `server.go` (NewServer + registration)**

Create `internal/mcp/server.go`:
```go
package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
```

Note: `server.go` references the `localStatus/localPause/localResume/localUp/localDown` handlers created in Task 3. This task's steps below add a temporary stub file so Task 2 compiles and its tests pass; Task 3 replaces the stubs with real implementations.

- [ ] **Step 5: Add temporary local-handler stubs so the package compiles**

Create `internal/mcp/handlers_local.go` (replaced in Task 3):
```go
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
```

- [ ] **Step 6: Write the failing round-trip + gating test**

Create `internal/mcp/server_test.go`:
```go
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/kube"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeKube implements KubeAPI.
type fakeKube struct {
	contexts []string
	current  string
	pods     []kube.PodInfo
}

func (f *fakeKube) GetContexts() []string       { return f.contexts }
func (f *fakeKube) GetCurrentContext() string   { return f.current }
func (f *fakeKube) GetNamespaces(context.Context) ([]string, error) {
	return []string{"default"}, nil
}
func (f *fakeKube) GetPods(context.Context, string) ([]kube.PodInfo, error) { return f.pods, nil }
func (f *fakeKube) GetPodRole(context.Context, string, string) (string, error) {
	return "master", nil
}
func (f *fakeKube) GetPodPorts(context.Context, string, string) ([]kube.ContainerPort, error) {
	return []kube.ContainerPort{{Name: "pg", Port: 5432}}, nil
}
func (f *fakeKube) GetPodLogs(context.Context, string, string, kube.LogOptions) (string, error) {
	return "hello\nworld", nil
}

// fakeDocker implements DockerAPI (unused fields return empty).
type fakeDocker struct{}

func (fakeDocker) GetStatus(context.Context) ([]docker.ContainerStatus, error) { return nil, nil }
func (fakeDocker) GetKindContainers(context.Context, string) ([]string, error) { return nil, nil }
func (fakeDocker) PauseContainers(context.Context, []string) error             { return nil }
func (fakeDocker) ResumeContainers(context.Context, []string) error            { return nil }

type fakeSpacebox struct{ installed bool }

func (f fakeSpacebox) IsInstalled() bool { return f.installed }
func (fakeSpacebox) Up() error           { return nil }
func (fakeSpacebox) Down() error         { return nil }

func newTestDeps(installed bool) Deps {
	return Deps{
		NewKube: func(string) (KubeAPI, error) {
			return &fakeKube{contexts: []string{"a", "b"}, current: "a"}, nil
		},
		Docker:   fakeDocker{},
		Spacebox: fakeSpacebox{installed: installed},
	}
}

func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
	}
	return names
}

func TestListContextsRoundTrip(t *testing.T) {
	srv := NewServer(newTestDeps(true), Options{})
	cs := connect(t, srv)
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_contexts"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res.Content)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	var payload struct {
		Current  string   `json:"current"`
		Contexts []string `json:"contexts"`
	}
	if err := json.Unmarshal([]byte(tc.Text), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Current != "a" || len(payload.Contexts) != 2 {
		t.Fatalf("payload = %+v, want current=a and 2 contexts", payload)
	}
}

func TestReadOnlyGating(t *testing.T) {
	srv := NewServer(newTestDeps(true), Options{AllowLocalControl: false})
	cs := connect(t, srv)
	defer cs.Close()
	names := toolNames(t, cs)

	for _, want := range []string{"list_contexts", "list_pods", "get_pod_logs", "local_status"} {
		if !names[want] {
			t.Errorf("expected tool %q to be registered", want)
		}
	}
	for _, absent := range []string{"local_pause", "local_resume", "local_up", "local_down"} {
		if names[absent] {
			t.Errorf("tool %q must be absent in read-only mode", absent)
		}
	}
}

func TestAllowLocalControlGating(t *testing.T) {
	srv := NewServer(newTestDeps(true), Options{AllowLocalControl: true})
	cs := connect(t, srv)
	defer cs.Close()
	names := toolNames(t, cs)
	for _, want := range []string{"local_pause", "local_resume", "local_up", "local_down"} {
		if !names[want] {
			t.Errorf("expected tool %q with AllowLocalControl", want)
		}
	}
}

func TestSpaceboxGatingWhenNotInstalled(t *testing.T) {
	srv := NewServer(newTestDeps(false), Options{AllowLocalControl: true})
	cs := connect(t, srv)
	defer cs.Close()
	names := toolNames(t, cs)
	if names["local_up"] || names["local_down"] {
		t.Errorf("local_up/local_down must be absent when spacebox is not installed")
	}
	if !names["local_pause"] {
		t.Errorf("local_pause should still be present")
	}
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/mcp/ -v`
Expected: PASS — all four tests. (If `NewInMemoryTransports`/`ListTools` symbols differ in the installed SDK version, adjust to the version's API; the shapes above match the current SDK.)

- [ ] **Step 8: Commit**

```bash
git add internal/mcp/deps.go internal/mcp/result.go internal/mcp/server.go internal/mcp/handlers_kube.go internal/mcp/handlers_local.go internal/mcp/server_test.go
git commit -m "mcp: add server foundation and read-only kube tools"
```

---

### Task 3: Local cluster handlers

**Files:**
- Modify (replace stubs): `internal/mcp/handlers_local.go`
- Test: `internal/mcp/handlers_local_test.go`

**Interfaces:**
- Consumes: `Deps.Docker` (`DockerAPI`), `Deps.Spacebox` (`SpaceboxAPI`), `docker.ContainerStatus`.
- Produces: real `localStatus/localPause/localResume/localUp/localDown` methods on `*server` (same names Task 2 registered).

- [ ] **Step 1: Write the failing local-handler test**

Create `internal/mcp/handlers_local_test.go`:
```go
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
	defer cs.Close()
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/mcp/ -run 'TestLocalStatus|TestLocalPause|TestLocalUp' -v`
Expected: FAIL — stubs return empty `containers`/`paused`, so `TestLocalStatus` and `TestLocalPausePausesRunningContainers` fail; `TestLocalUpSurfacesError` fails because the stub always returns ok.

- [ ] **Step 3: Replace the stubs with real implementations**

Replace the entire contents of `internal/mcp/handlers_local.go`:
```go
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
```

- [ ] **Step 4: Run the full package tests to verify they pass**

Run: `go test ./internal/mcp/ -v`
Expected: PASS — all Task 2 and Task 3 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/handlers_local.go internal/mcp/handlers_local_test.go
git commit -m "mcp: implement local cluster tools"
```

---

### Task 4: `kapctl mcp` subcommand and docs

**Files:**
- Create: `cmd/kapctl/mcp.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `mcp.NewServer`, `mcp.Deps`, `mcp.Options` (Task 2); `kube.NewClient`, `docker.NewClient`, `spacebox` package funcs; the package-level `version` var (from `cmd/kapctl/version.go`); `rootCmd` (from `root.go`).
- Produces: a registered `kapctl mcp` Cobra command.

- [ ] **Step 1: Write the subcommand**

Create `cmd/kapctl/mcp.go`:
```go
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
	RunE: func(cmd *cobra.Command, _ []string) error {
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
```

Note: `kube.NewClient` returns `(*kube.Client, error)`; `*kube.Client` satisfies `appmcp.KubeAPI` (it has all the interface methods, including `GetPodLogs` from Task 1). `*docker.Client` satisfies `appmcp.DockerAPI`.

- [ ] **Step 2: Build to verify wiring compiles and interfaces are satisfied**

Run: `make build`
Expected: builds `bin/kapctl` with no errors. (A mismatch here means `*kube.Client` or `*docker.Client` doesn't satisfy the interface — fix the interface or the wrapper.)

- [ ] **Step 3: Smoke-test the server handshake over stdio**

Run:
```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' | ./bin/kapctl mcp
```
Expected: a single JSON-RPC response line containing `"serverInfo"` with `"name":"kapctl"` (no panic, process exits when stdin closes).

- [ ] **Step 4: Verify tools list over stdio (read-only default)**

Run:
```bash
printf '%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | ./bin/kapctl mcp
```
Expected: the second response lists `list_contexts`, `list_namespaces`, `list_pods`, `describe_pod`, `get_pod_logs`, `local_status` and does NOT list `local_pause`/`local_up`.

- [ ] **Step 5: Add README documentation**

Add a section to `README.md` (place it after the existing subcommand documentation):
```markdown
## MCP server

`kapctl mcp` runs kapctl as a [Model Context Protocol](https://modelcontextprotocol.io)
server over stdio, exposing read-only Kubernetes introspection
(`list_contexts`, `list_namespaces`, `list_pods`, `describe_pod`,
`get_pod_logs`) and local kind cluster status (`local_status`).

Pass `--allow-local-control` to also expose the mutating local tools
`local_pause`, `local_resume`, and (when the `spacebox` binary is
installed) `local_up` / `local_down`. No tool can mutate real
Kubernetes workloads.

Register it with an MCP client (e.g. Claude Desktop / Claude Code):

```json
{
  "mcpServers": {
    "kapctl": {
      "command": "kapctl",
      "args": ["mcp"]
    }
  }
}
```
```

- [ ] **Step 6: Run the full test suite and lint**

Run: `make test && make lint`
Expected: all tests PASS; lint reports no new issues.

- [ ] **Step 7: Commit**

```bash
git add cmd/kapctl/mcp.go README.md
git commit -m "cli: add mcp subcommand and docs"
```

---

## Self-Review

**Spec coverage:**
- Read-only k8s tools (list_contexts/namespaces/pods, describe_pod, get_pod_logs) → Task 2 + Task 1 (`GetPodLogs`). ✓
- Local tools (status + gated pause/resume/up/down) → Task 3, gating in Task 2's `NewServer`. ✓
- Safety: read-only default, `--allow-local-control`, spacebox gate → Task 2 (`NewServer`), Task 4 (flag). ✓
- `internal/mcp` package with deps/handlers/server split, fake-able interfaces → Tasks 2–3. ✓
- `kube.GetPodLogs` via client-go with tail cap → Task 1. ✓
- stdio transport subcommand, no styles → Task 4. ✓
- Tests: handler units, in-memory round-trip, gating, GetPodLogs → Tasks 1–3. ✓
- Docs README snippet → Task 4. ✓
- Dependency added → Task 1. ✓

**Placeholder scan:** No TBD/TODO; all steps carry concrete code and commands. The Task 2 local stubs are intentional, explicitly labeled temporary, and replaced wholesale in Task 3.

**Type consistency:** `KubeAPI`/`DockerAPI`/`SpaceboxAPI`, `Deps`, `Options{AllowLocalControl, Version}`, `NewServer(Deps, Options)`, handler method names (`listContexts`…`localDown`), and `kube.LogOptions{Container, TailLines}` are used identically across Tasks 1–4. `*kube.Client`/`*docker.Client` satisfy the interfaces (verified against the existing method sets).
