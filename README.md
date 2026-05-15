# kap

A keyboard-first TUI for working with Kubernetes contexts and a local
kind cluster, written in Go on top of
[Bubble Tea](https://github.com/charmbracelet/bubbletea). `kap` walks
you through context → namespace → pod → action (logs, exec, describe,
port-forward, delete) without leaving the terminal, manages background
port-forwards, and — when an external `spacebox` CLI is on `PATH` —
exposes a tab for local kind cluster lifecycle.

## Features

- Tabbed layout: `EXPLORER` (k8s wizard), `LOCAL` (kind cluster, only
  when `spacebox` is installed), `FORWARDS` (active port-forwards with
  live status badges)
- Async loading with cancellation: switching contexts/namespaces/pods
  never leaves stale loaders behind
- Inline filtering (`/`), numeric jump (1–9), one-letter retry (`r`)
  when a load fails
- Smart port-forward picker: priority ports (5432, 8080) → ports
  detected from the pod spec → common defaults (80, 6379, 9090, 3000),
  with auto-bumping when the requested local port is occupied
- Background port-forward manager — the TUI stays interactive while
  forwards run; events flow into a live table
- Cobra subcommands for non-interactive use: `kap ctrl`, `kap pgsql`,
  `kap loc {up|down|pause|resume|status}`
- Multiple themes (Catppuccin Mocha default, Nord) selectable via
  `~/.config/kap/config.yaml`

## Install

### Homebrew

```sh
brew tap billygate/tap
brew install kap
```

### From source

Requires Go 1.26 or newer.

```sh
go install github.com/billygate/kap-toolsbox/cmd/kap@latest
```

Or build from a checkout:

```sh
git clone https://github.com/billygate/kap-toolsbox
cd kap-toolsbox
make build      # produces ./bin/kap
```

## Usage

Launch the TUI:

```sh
kap
```

Non-interactive subcommands:

```sh
kap ctrl                 # context → namespace → pod → action picker
kap pgsql                # interactive postgres port-forward
kap loc up               # bring up the local kind cluster (needs spacebox)
kap loc {down|pause|resume|status}
```

### Keys (TUI)

| Key       | Action                  |
| --------- | ----------------------- |
| `enter`   | select                  |
| `/`       | filter                  |
| `1–9`     | numeric jump            |
| `esc`     | back                    |
| `r`       | retry failed load       |
| `[` / `]` | prev / next tab         |
| `?`       | toggle help             |
| `q`       | quit                    |

## Configuration

`~/.config/kap/config.yaml` (created on first run):

```yaml
theme: catppuccin       # or "nord"
ports:
  my-ctx.my-ns: 5432    # per-context/namespace remembered local port
```

## Optional: local cluster support

The `LOCAL` tab and `kap loc {up,down}` shell out to a `spacebox`
binary on `PATH`. Without it the tab is hidden and `up`/`down` print a
note; `pause`/`resume`/`status` work via Docker directly. `spacebox`
is not bundled — bring your own (or skip the feature).

## License

PolyForm Noncommercial 1.0.0 — see [LICENSE](LICENSE).
