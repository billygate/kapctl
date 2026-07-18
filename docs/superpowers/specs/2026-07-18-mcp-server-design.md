# kapctl MCP server — design

**Date:** 2026-07-18
**Status:** Approved (design), pending implementation plan

## Goal

Expose a subset of kapctl's capabilities to MCP (Model Context Protocol)
clients — Claude Desktop, Claude Code, etc. — via a new `kapctl mcp`
subcommand that speaks JSON-RPC over stdio. This lets an AI client answer
questions like "what pods are running in the staging context?" or "pause my
local kind cluster" by calling typed tools backed by kapctl's existing
`internal/*` wrappers.

Scope is deliberately narrow and safe:

- **Read-only Kubernetes introspection** (contexts, namespaces, pods, pod
  details, logs).
- **Local kind/Docker cluster control** (status, pause/resume, up/down).

Out of scope: no `exec`, no pod `delete`, no port-forward mutation. Those stay
CLI/TUI-only.

## Tool surface

Each Kubernetes tool takes a `context` argument; a `*kube.Client` is built
per-context via `kube.NewClient(context)` inside the handler (empty string =
current context).

### Read-only Kubernetes tools (always registered)

| Tool | Backing wrapper | Notes |
|---|---|---|
| `list_contexts` | `GetContexts` + `GetCurrentContext` | flags the current context |
| `list_namespaces` | `GetNamespaces(ctx)` | |
| `list_pods` | `GetPods(ctx, ns)` | returns name, phase, spilo-role |
| `describe_pod` | `GetPodRole` + `GetPodPorts` | structured details (role + container ports), not raw `kubectl describe` text |
| `get_pod_logs` | **new** `kube.GetPodLogs` | supports `tailLines` (default 200) and optional `container`; caps output so a chatty pod can't flood model context |

### Local cluster tools

| Tool | Backing wrapper | Gate |
|---|---|---|
| `local_status` | `docker.GetStatus` | always registered |
| `local_pause` | `docker.PauseContainers(GetKindContainers("running"))` | mutating |
| `local_resume` | `docker.ResumeContainers(GetKindContainers("paused"))` | mutating |
| `local_up` | `spacebox.Up` | mutating; only when `spacebox.IsInstalled()` |
| `local_down` | `spacebox.Down` | mutating; only when `spacebox.IsInstalled()` |

### Safety model

- The server starts **read-only by default**. The four mutating local tools
  (`local_pause`, `local_resume`, `local_up`, `local_down`) are registered
  **only** when the user passes `kapctl mcp --allow-local-control`.
- `local_up`/`local_down` are additionally gated on `spacebox.IsInstalled()`,
  mirroring the existing TUI/CLI behavior.
- No tool can mutate real Kubernetes workloads.

## Architecture

### New package `internal/mcp`

No Cobra or TUI imports. Compiles independently of `cmd/` and `internal/tui`.

- `server.go` — `NewServer(deps Deps, opts Options) *mcp.Server`. Builds the
  official SDK server and registers tools. `opts.AllowLocalControl` gates the
  mutating tools.
- `handlers.go` — one thin handler per tool. Each unmarshals typed args, calls
  a wrapper, and returns a structured result — or a tool-error result with a
  helpful hint (echoing `ctrl`'s "check VPN / SSO / auth" note on auth
  failure). Handlers hold no business logic beyond arg translation.
- `deps.go` — small interfaces covering only the methods used, so handlers are
  unit-testable with fakes:
  - `KubeAPI` — the subset of `*kube.Client` methods the handlers call.
  - `kubeClientFactory func(context string) (KubeAPI, error)` — production
    wiring passes an adapter over `kube.NewClient`.
  - `DockerAPI` — the subset of `*docker.Client` methods used.
  - spacebox is used via its package functions (`Up`/`Down`/`IsInstalled`),
    wrapped behind a tiny interface field on `Deps` for testability.

### New subcommand `cmd/kapctl/mcp.go`

- `kapctl mcp [--allow-local-control]`.
- Builds the real `Deps` (kube client factory, docker client, spacebox
  adapter), calls `mcp.NewServer`, and runs it over the SDK's **stdio
  transport**, blocking until stdin closes.
- Does **not** read `loadedStyles` (no TUI). Follows the existing
  package-level-var pattern from `root.go` only if it needs `loadedConfig`
  (not currently required for the read-only + local surface).

### New method `kube.GetPodLogs`

- Signature roughly `GetPodLogs(ctx, namespace, pod string, opts LogOptions)
  (string, error)` where `LogOptions` carries `TailLines` and `Container`.
- Implemented with client-go `CoreV1().Pods(ns).GetLogs(...).Stream(ctx)`,
  reusing the client's existing 120s request timeout.
- Caps returned output (default `TailLines` = 200) to protect model context.

### Dependency

- Add `github.com/modelcontextprotocol/go-sdk` to `go.mod` (official SDK,
  co-maintained by Anthropic & Google; stdio transport built in).

## Data flow

```
Claude client
   → JSON-RPC over stdio
   → go-sdk server
   → internal/mcp handler (arg unmarshal)
   → internal/{kube,docker,spacebox} wrapper
   → k8s API / Docker / spacebox binary
   → structured JSON result back up the chain
```

## Error handling

- Wrapper errors become MCP **tool-error results** (`isError`), never protocol
  errors, so the client sees a readable message.
- Auth/connectivity failures on Kubernetes tools include the same hint style as
  `ctrl` ("check VPN / SSO / auth").
- Mutating local tools return a concise success summary (e.g. which containers
  were paused) so the model can confirm the effect.

## Testing

- **Handler unit tests** with fake `KubeAPI`/`DockerAPI`: assert arg parsing,
  result shape, auth-error hinting, and that mutating tools are **absent**
  unless `AllowLocalControl` is set.
- **Integration test** using the SDK's in-memory transport: connect a client,
  `list_tools`, call `list_contexts`/`list_pods` against fakes, assert the
  round-trip and the tool set changes with `--allow-local-control`.
- **`kube.GetPodLogs` test** with a fake clientset (fake logs stream), asserting
  `tailLines` capping and container selection.

## Docs

- Short README section with the client config snippet:
  ```json
  { "command": "kapctl", "args": ["mcp"] }
  ```
  and a note that `--allow-local-control` opts into the mutating local tools.

## Non-goals / YAGNI

- No HTTP/SSE transport (stdio only — this is a local dev tool).
- No MCP `resources` or `prompts` in v1; tools only.
- No `exec`, pod `delete`, or port-forward tools.
