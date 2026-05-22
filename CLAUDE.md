# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, test, lint

```bash
make build          # → bin/kapctl   (preferred; never run `go build -o ./kapctl` at repo root)
make run
make test           # go test ./...
make cover          # go test -coverprofile=coverage.out  →  prints total %
make lint           # golangci-lint run  (uses .golangci.yml: errcheck, govet, revive, staticcheck, gofmt)
make tidy
go test ./internal/tui/panes -run TestExplorerStepFlow -v   # run a single test
```

Go 1.26.2. Module path is `github.com/billygate/kapctl`. Build artifacts go to `bin/` (gitignored). The `.go/` directory is a project-local `GOPATH`/build cache — do not commit changes inside it.

## What this repo is

A Go TUI/CLI toolbox for working with Kubernetes contexts and an
optional local kind cluster. The `kapctl` binary launches a Bubble Tea
tabbed app by default; Cobra subcommands (`ctrl`, `pgsql`, `loc`)
expose the same flows non-interactively.

The `LOCAL` tab and `kapctl loc up|down` shell out to an external
`spacebox` binary on `PATH`. `spacebox` is not bundled; without it the
tab is hidden and the `up`/`down` subcommands print a note.
`pause`/`resume`/`status` operate directly via Docker and work
regardless.

## Architecture

```
cmd/kapctl/main.go → Execute (Cobra)
  ├── (no args) → tui.RunApp(cfg)  — Bubble Tea TUI (EXPLORER + optional LOCAL + FORWARDS)
  ├── ctrl                          — interactive kubectl: ctx → ns → pod → {logs|exec|describe|delete}
  ├── loc {up|down|pause|resume|status}
  └── pgsql                         — interactive postgres port-forward
```

`internal/` layout:

- `internal/kube` — k8s.io/client-go wrapper. `NewClient(contextName)` loads kubeconfig with default loading rules; pass `""` to use current context. Sets a 120s request timeout. `GetPodRole` reads the `spilo-role` label (Spilo/Patroni Postgres operator).
- `internal/docker` — Docker SDK wrapper around a small `clientAPI` interface (so tests can fake it). All "kind container" operations filter by label `io.x-k8s.kind.cluster`.
- `internal/spacebox` — thin `exec.Command` shell-out to `spacebox cluster up|down`. `IsInstalled()` is used by callers (CLI subcommand and the TUI) to gate UI surface and behavior.
- `internal/config` — plain `gopkg.in/yaml.v3` config at `~/.config/kapctl/config.yaml`. Holds `Theme` and `Ports` (key `<ctx>.<ns>` → port). No viper. Missing file is not an error; unknown theme falls back to `catppuccin` with a warning.
- `internal/tui` — root tabbed app (`AppModel` in `app.go`). The tab list is built at startup: `EXPLORER` is always present, `LOCAL` is appended only when `spacebox.IsInstalled()` is true, `FORWARDS` is always present. Update/View dispatch by tab **name**, not by integer index — keep it that way when adding new tabs so the conditional doesn't break routing.
- `internal/tui/core` — primitives shared between `tui` and its sub-packages (panes, overlays). Holds the `KubeClient`/`DockerClient` interfaces (compile-time satisfied by `*kube.Client` / `*docker.Client`), the global `Keys` map, the `ListItem`/`Separator` model + item delegate, and the `Loader` async-cancel helper. **Anything in `core` must compile without importing `tui`** — that's the whole point: it breaks the cycle between `tui` (owns the root model, imports panes) and panes (need keys, item delegate, kube/docker interfaces, shared messages).
- `internal/tui/panes` — `Explorer` (k8s wizard: context → namespace → pod → action [→ port]) and `Local` (kind status table). Each pane owns its own list state, filter handling, and numeric jump logic. The root model only does tab switching, sizing, help, and message routing.
- `internal/tui/overlays` — `Pick(title, items, styles)` modal reused by `cmd/*` subcommands; `portselect` (`BuildPortChoices`, `ParsePort`); `Toasts` (TTL-based notification queue, `ToastMsg` is the tea.Msg flavour).
- `internal/tui/styles` — bundles lipgloss styles built once from a `Palette`.
- `internal/tui/themes` — `Palette` interface and registry. Themes self-register via `init()`. Currently `catppuccin` and `nord`.

Two TUI entry points coexist on purpose: the **tabbed app** (`tui.RunApp`) is the primary UX; the **`overlays.Pick` modal** is reused by Cobra subcommands so each can be driven non-interactively too.

### Async work in panes (Loader pattern)

`core.Loader` sequences async fetches so a fresh load cancels the previous one and any in-flight result from a superseded operation is dropped at the message boundary. Pattern:

1. `cmd := pane.loader.Start(ctx, func(ctx) tea.Msg { ... })` — returns a `tea.Cmd` that produces a `core.Result{Generation, Payload}`.
2. In `Update`, on `core.Result`, **always check `pane.loader.Accept(msg.Generation)`** before applying the payload. Stale results are silently dropped.
3. Step-bound fetch errors (`GetNamespaces`/`GetPods`/`GetPodPorts` failing) are surfaced inline via the pane's `loadErr` field and rendered as a persistent error view with an `r`-to-retry hint — see `Explorer`. **Don't emit a `ToastMsg` from the same path**: the toast disappears with its TTL and leaves the user staring at an empty list. Toasts are still the right channel for one-shot errors that have no pane to live in (port parse, port conflict, etc.).

When adding a new async load, follow this exact shape — don't bypass the loader, even for "quick" fetches. Extract the load into a small helper on the pane (`loadX() tea.Cmd`) that clears `loadErr` at kick-off; share it between `handleSelect` and `retryCurrentLoad`.

### Theming

- Views and styles consume only the `themes.Palette` interface — never raw `lipgloss.Color` literals. If you reach for a hex code in a pane/overlay, add a method to `Palette` instead.
- `Palette.PodStatus(phase)` and `Palette.SpiloRole(role)` are the documented escape hatches for status-tinted color.
- Catppuccin Mocha is the default palette.

### TUI conventions

- Keys: `enter` select, `/` filter, `esc` back, `r` retry (when a load error is shown), `[`/`]` switch tabs, `?` help, `q`/`ctrl+c` quit. Numeric keys 1–9 (and 2-digit beyond 10) jump to list items. The numeric-jump logic lives in each pane (e.g. `Explorer.handleNumeric`).
- Separator items (`core.Separator`) are rendered but not selectable; `core.NewItemDelegate` skips them on `up`/`down`/`k`/`j`.
- Long, narrow lists may render in two columns via `core.ShouldShowTwoColumns` + `core.RenderTwoColumns` (used by the picker overlay).
- Avoid `fmt.Println` from TUI code paths — surface user-visible messages via `overlays.ToastMsg` instead, or as inline `loadErr` for step-bound fetch failures.

### Port-forward defaults

`overlays.BuildPortChoices` produces a priority list (5432 postgres, 8080 http-alt), then a "DETECTED" separator with ports detected from the pod spec, then a "COMMON" list (80, 6379, 9090, 3000). Detected/common ports are deduped against priority. **Preserve the priority → detected → common grouping when changing this — it's the UX contract.** `overlays.ParsePort` extracts the leading integer from a label like `"5432 (postgresql)"`.

### Cobra subcommands and shared state

`cmd/kapctl/root.go` populates two package-level vars in `Execute()` before `rootCmd.Execute()`: `loadedConfig` (`*config.Config`) and `loadedStyles` (`*styles.Styles`). All sibling subcommand files (`ctrl.go`, `loc.go`, `pgsql.go`) read these directly. If you add a new subcommand and it needs config or styles, use the same pattern rather than re-loading.
