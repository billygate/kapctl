# TUI: persistent header, resume last selection, help modal

Date: 2026-05-21
Status: Draft, awaiting user review

## Problem

Three usability gaps in the Bubble Tea TUI:

1. The selected Kubernetes context and namespace are only visible inside the
   Explorer wizard's step title (`Select Namespace (ctx)`). Once the user
   advances past `stepNamespace`, the active context becomes implicit; on
   `LOCAL` or `FORWARDS` tabs the user has no clue which `ctx`/`ns` the next
   Explorer action will target.
2. Every TUI start makes the user click through `stepContext → stepNamespace`
   even when they have been working in the same `ctx`/`ns` for hours. There
   is no notion of "last used".
3. `?` currently toggles `help.Model.ShowAll`, which expands the inline footer.
   The expanded line is cramped, gets truncated on narrow terminals, and is
   not what users expect from a `?` keybinding — they expect a modal.

## Goals

- Persistent visual indicator of currently selected `ctx`/`ns`, globally
  across all tabs.
- On startup, auto-resume to the last selected `ctx`/`ns` (no pod), so
  returning users land directly on the pod list.
- `?` opens a centered modal with all keybindings grouped by section. Closes
  via `?`, `esc`, or `q`.

## Non-goals

- Persisting the selected pod (pods are ephemeral; saving the name has near-zero hit rate).
- Persisting the active tab.
- Tab-specific resume state for `LOCAL` / `FORWARDS`.
- A configurable header format / position.

## Design

### Architecture

| File | Change |
|---|---|
| `internal/config/config.go` | Add `LastContext` and `LastNamespace` fields with yaml tags `last_context` / `last_namespace`. Add accessor methods `LastContext() string`, `LastNamespace() string`, `SetLastContext(string)`, `SetLastNamespace(string)`. Update `config_test.go` for round-trip + backwards-compat with old yaml. |
| `internal/tui/overlays/help.go` | New. `Help` overlay type with `Visible bool`. Exports `View(termW, termH int, s *styles.Styles) string` rendering a centered modal. Static `[]Section` content; binding labels read from `core.Keys` so they stay consistent with the footer. |
| `internal/tui/panes/explorer.go` | (a) Add `ResumeStore` interface (declared locally in `panes`) with `LastContext()`, `LastNamespace()`, `SetLastContext`, `SetLastNamespace`, `Save() error`. `*config.Config` implements it. (b) Extend `NewExplorer` signature with `cfg ResumeStore`. (c) New method `Selection() (ctx, ns string)`. (d) `SetKubeClient` performs auto-resume on success (details below). (e) `handleSelect` writes `SetLastContext` / `SetLastNamespace` and triggers a fire-and-forget Save via `tea.Cmd`. |
| `internal/tui/app.go` | (a) Replace `help help.Model` field with `helpOverlay *overlays.Help`; existing `help.Model` for the short footer is kept and renamed to `footerHelp`. (b) `?` toggles `helpOverlay.Visible` (no longer flips `ShowAll`). (c) When `helpOverlay.Visible`, intercept all key events: `?`, `esc`, `q` close the modal; every other key is dropped without forwarding to a pane. (d) `View` adds a breadcrumb row between tab divider and pane content. (e) `paneSize` reduces inner height by the two extra rows (breadcrumb + secondary divider). (f) `NewAppModel` passes `cfg` to `NewExplorer`. |
| `internal/tui/app_test.go` | Add: `?` opens modal; `enter` while open is absorbed; `esc`/`?`/`q` close it; View contains breadcrumb; View shows placeholder when ctx/ns empty. |
| `internal/tui/panes/panes_test.go` | Update Explorer construction to pass a fake `ResumeStore`. Add: auto-resume scenarios (empty, ctx-only, ctx+ns, ctx not in kubeconfig); persist-on-select for ctx and ns; ns clears when ctx changes. |

### Component boundaries

- **`Explorer` is the source of truth for selected ctx/ns.** Header reads via
  `Selection()`. No state is duplicated in `AppModel`.
- **`ResumeStore` interface in `panes`** — Explorer does not import
  `internal/config`. Same pattern as `core.KubeClient` / `core.DockerClient`:
  define a small interface where it is used, satisfy from outside.
- **`overlays.Help` is a separate type** from `overlays.Pick` — both are
  modals but their interaction model differs (Pick navigates a list and
  emits a selection; Help is read-only). Sharing a base would be a
  premature abstraction.

### Data flow: persist on selection

```
Explorer.handleSelect on stepContext:
  e.ctx = val
  cfg.SetLastContext(val)
  cfg.SetLastNamespace("")           # ns from old ctx is meaningless
  return tea.Cmd: Save() (and emit toast on error)
  ... existing step transition ...

Explorer.handleSelect on stepNamespace:
  e.ns = val
  cfg.SetLastNamespace(val)
  return tea.Cmd: Save() (toast on error)
  ... existing step transition ...
```

`Save()` is wrapped as `func() tea.Msg { ... }` so errors flow through the
existing `ToastMsg` channel without goroutine/race issues. Pod selection
does NOT write to cfg.

### Data flow: auto-resume on startup

Triggered from `Explorer.SetKubeClient(k, err)` when `err == nil && k != nil`:

```
if cfg.LastContext() == "":
  initView(stepContext); return nil           # no saved state, behave as today

if cfg.LastContext() not in k.GetContexts():
  cfg.SetLastContext(""); cfg.SetLastNamespace("")
  return tea.Cmd: Save() + toast("saved context X not found")
  initView(stepContext); return that cmd

k2, err := kube.NewClient(cfg.LastContext())
if err != nil:
  return toast(err); initView(stepContext)    # don't crash; user can pick another

e.kube = k2
e.ctx = cfg.LastContext()

if cfg.LastNamespace() == "":
  e.step = stepNamespace
  return e.loadNamespaces()

e.step = stepPod
return e.loadPods()                            # if ns is gone, falls into existing
                                               # loadErr path (inline error + 'r retry')
```

The existing `loadErr` inline-error pattern (per `Explorer.View`) absorbs
the "namespace was deleted in the cluster" failure mode without special
handling — the user gets a Failed-to-load panel with `r: retry • esc: back`,
and `esc` walks them back to `stepNamespace`.

### View: breadcrumb

```
AppModel.View():
  tabRow      := <as today>
  divider     := <as today>
  ctx, ns     := m.explorer.Selection()
  breadcrumb  := renderBreadcrumb(ctx, ns)
  divider2    := <same style as divider>
  header      := JoinVertical(tabRow, divider, breadcrumb, divider2)
  content     := <as today>
  ...

renderBreadcrumb(ctx, ns):
  if ctx == "" && ns == "":
    return styles.Muted.Render("  (no context selected)")
  parts := []
  if ctx != "": parts = append(parts, styles.Accent.Render("ctx: ") + ctx)
  if ns  != "": parts = append(parts, styles.Accent.Render("ns: ")  + ns)
  return "  " + strings.Join(parts, "  •  ")
```

`paneSize()` subtracts 2 additional rows from height (breadcrumb +
second divider) to keep content inside the rounded window border.

### View: help modal

`overlays.Help` exposes a `Visible bool` field and a `View(termW, termH, styles)`
method. `AppModel.View()` always renders the standard composition first;
when `helpOverlay.Visible`, it overlays the modal via `lipgloss.Place(...)`
centered on the inner area (same pattern as `overlays.Pick`).

Help content (subject to ratification from `forwards.go` at implementation
time):

- **Global** — `?`, `q`/`ctrl+c`, `[` / `]`, `r`
- **Navigation in lists** — `enter`, `/`, `esc`, `1–9`, `↑↓` / `kj`
- **Tables (Pod step)** — `/`, `enter`, `esc`
- **Port-forwards tab** — `s` (stop), `↑↓` (cursor)

Strings come from `key.WithHelp(...)` on `core.Keys` where applicable,
so a future binding change is reflected automatically.

### Update routing while modal is open

```
AppModel.Update on tea.KeyMsg:
  if m.helpOverlay.Visible:
    switch key:
      case "?", "esc", "q":
        m.helpOverlay.Visible = false
      default:
        # absorbed — do nothing
    return m, nil
  ... existing routing ...
```

`q` closes the modal rather than quitting — this prevents the surprise of
"I opened help and accidentally quit". `ctrl+c` is **not** trapped: it
still quits, matching universal terminal expectation.

## Edge cases

| Case | Behavior |
|---|---|
| Kubeconfig invalid / `core.LoadKube` errors | Existing error view; resume is never invoked. |
| `LastContext` empty | `stepContext` as today. |
| `LastContext` valid, `LastNamespace` empty | `stepNamespace` + `loadNamespaces()`. |
| `LastContext` not in kubeconfig | Clear both fields, persist, toast, `stepContext`. |
| `LastNamespace` deleted in cluster | `loadPods()` fails → existing `loadErr` inline view + `r`/`esc`. |
| `Save()` fails (disk full, readonly) | Toast error. In-memory state is not rolled back; the next selection retries. |
| Terminal too narrow to fit breadcrumb | Existing height-truncation in `AppModel.View` already handles overflow at the bottom; breadcrumb itself is a single line and will wrap or be cut by the lipgloss frame — acceptable. |
| Help modal open + window resize | `Help.View` re-centers using new `termW`/`termH` on every render — no state to update. |

## Sequencing

1. **config: add LastContext/LastNamespace + accessor methods + tests**
   Atomic; no behavioral change.
2. **overlays: add Help modal + unit test**
   Isolated; not yet wired.
3. **explorer: ResumeStore interface + auto-resume + persist on select + tests**
   Largest step. Existing tests need updating for new constructor signature.
4. **app: render breadcrumb + wire `?` to Help modal + adjust paneSize + tests**
   Final integration.

Each step ends with `make build && make test && make lint` green.

## Testing

| Level | Test |
|---|---|
| `internal/config/config_test.go` | round-trip with `LastContext`/`LastNamespace`; backwards-compat with yaml that lacks the new keys; empty values serialize correctly. |
| `internal/tui/panes/panes_test.go` | (a) `SetKubeClient` with empty cfg → `stepContext`; (b) ctx-only saved → `stepNamespace`; (c) ctx+ns saved → `stepPod`; (d) saved ctx not in `GetContexts()` → cleared + `stepContext`; (e) `handleSelect` on `stepContext` writes ctx and clears ns; (f) `handleSelect` on `stepNamespace` writes ns; (g) fake `ResumeStore` counts `Save` calls. |
| `internal/tui/overlays` | rendering test that `Help.View(80, 24, styles)` contains "Help", "Global", and a known binding like "enter". |
| `internal/tui/app_test.go` | (a) `?` opens modal (`helpOverlay.Visible == true`); (b) while open, `enter` is absorbed (Explorer state unchanged); (c) `esc`, `?`, `q` close; (d) `ctrl+c` still quits while open; (e) View contains the breadcrumb when ctx/ns set; (f) View shows `(no context selected)` placeholder when both empty. |

## Open decisions (resolved here)

- **Double client construction.** Auto-resume calls `kube.NewClient(LastContext)`
  even though `core.LoadKube("")` already produced a client. Accepted —
  client creation is cheap and threading a "context override" parameter
  through `core.LoadKube` would couple it to a concern (resume) it should
  not own.
- **`q` closes the modal.** Trade-off: discoverability ("how do I close
  this?") vs. accidental quit. Closing wins.
- **No tab persistence.** Restoring the active tab adds surface for little
  payoff — users mostly start on EXPLORER. Out of scope.
