# Explorer: persistent inline load-error display

## Problem

When the Explorer pane's async fetch fails (`GetNamespaces`, `GetPods`,
`GetPodPorts`), the error is surfaced only via `overlays.ToastMsg{ToastError}`.
The toast disappears after its TTL and the pane is left rendering an empty
list, indistinguishable from a "no items" state. Common triggers: VPN dropped,
SSO expired, DNS failure, kube API unreachable.

Users need a persistent, in-pane indication that the failure happened, plus a
way to retry without backing all the way out.

## Scope

In scope:

- `Explorer` pane only (the wizard: context → namespace → pod → action → port).

Out of scope (intentionally — separate scenarios, can ship later):

- `Local` pane / Docker daemon errors.
- Cobra subcommands (`ctrl`, `pgsql`) — they already print errors; tightening
  that output is its own task.
- Initial kube-client construction failure (`SetKubeClient(nil, err)`) — the
  existing `e.err != nil` View() branch already renders this fine.

## Design

### State

Add one field to `Explorer`:

```go
loadErr error // last async-load failure for the current step
```

### Update flow

In `Update`, when a `core.Result` arrives with payload `explorerLoadErrMsg`:

- Set `e.loadErr = payload.err`.
- Do **not** emit `overlays.ToastMsg`. The inline display replaces the toast
  for these specific load failures. The toast remains the right channel for
  one-shot errors that have no pane to live in (invalid port input, port
  conflict during the form submit, etc.).

Clear `e.loadErr = nil` at every point where a fresh load is kicked off or
data succeeds:

- Before each `e.loader.Start(...)` call in `handleSelect` (stepContext →
  GetNamespaces, stepNamespace → GetPods, stepAction[port-forward] →
  GetPodPorts), in `runAction("delete")`, and in the `core.TickMsg` branch
  that re-polls pods.
- On successful payloads `namespacesLoadedMsg`, `podsLoadedMsg`,
  `podPortsLoadedMsg`, `podDeletedMsg`.
- On `core.Keys.Back` (the step is being popped — the previous step's data is
  still valid).

### Retry key

Add `core.Keys.Retry` to the global key map (`core/core.go`), bound to `r`,
help "retry load". Listed in help-overlay output.

Behavior in `updateList`:

- When `e.loadErr != nil` and `core.Keys.Retry` matches, dispatch the load
  appropriate to the current step:
  - `stepNamespace` → `GetNamespaces`
  - `stepPod` → `GetPods`
  - `stepPort` → `GetPodPorts`
  - other steps: no-op (those steps don't perform async loads).
- The key has no effect when `e.loadErr == nil` (kept as a no-op rather than
  erroring — keeps the input handler simple and matches existing tolerance for
  unused keys per step).

The retry handler shares the exact `loader.Start` closures already used by
`handleSelect` — extract them into small helpers on `Explorer`:
`loadNamespaces()`, `loadPods()`, `loadPodPorts()`, each returning a
`tea.Cmd`. `handleSelect`/`Update` call the same helpers, removing the
duplicated `loader.Start(...) { items, err := kClient.GetX(...) ... }`
boilerplate currently inlined three times.

Filter-mode interaction: when `e.list.FilterState() == list.Filtering`, the
existing early-return in `updateList` continues to feed all keys (including
`r`) to the list. Retry only fires in normal navigation mode, which is the
mode the error view is shown in anyway.

### View

When `e.loadErr != nil`, `View()` returns an error pane instead of the
list/table — same structural pattern as the existing `e.err != nil` branch:

```
<viewTitle()>            ← styles.Title

Failed to load:           ← styles.Warn

<e.loadErr.Error()>       ← plain text

r: retry  •  esc: back    ← styles.Muted
```

The pane title (`viewTitle()`) is preserved so the user keeps context about
which step failed.

The check for `e.loadErr` is placed *after* the existing `e.err != nil` and
`!e.kubeReady` checks (config errors and not-yet-connected state are still
more fundamental than a transient load failure).

## Files touched

- `internal/tui/core/core.go` — add `Retry` to `Keys` map.
- `internal/tui/panes/explorer.go` — new field, helpers, Update/View changes.
- `internal/tui/panes/panes_test.go` — extend `mockKubeClient` with optional
  failure flags; new tests below.

## Tests

`internal/tui/panes/panes_test.go`:

1. `TestExplorerLoadErrorShownInline` — configure mock to return error from
   `GetNamespaces`; drive Explorer through `handleSelect(stepContext)`; pump
   the `core.Result` message through `Update`; assert `e.loadErr != nil` and
   `View()` contains the error text and the "r: retry" hint.
2. `TestExplorerLoadErrorClearedByBack` — same setup, then send `esc`
   KeyMsg; assert `e.loadErr == nil` and step popped to stepContext.
3. `TestExplorerLoadErrorClearedBySuccessfulRetry` — same setup, then flip
   the mock to success, send `r` KeyMsg, pump the resulting Cmd, deliver the
   `core.Result`; assert `e.loadErr == nil` and namespaces populated.
4. `TestExplorerLoadErrorReplacesToast` — assert that the Cmd returned by
   `Update` on `explorerLoadErrMsg` does **not** produce an `overlays.ToastMsg`
   (no Cmd is fine, or a Cmd that returns nil).

Existing tests (`TestExplorerInitListStepAction` etc.) must keep passing
unchanged.

## Non-goals / future

- No automatic retry / backoff — explicit `r` only.
- No structured error classification (network vs. auth vs. timeout). The raw
  `err.Error()` is shown verbatim. A future iteration could add a "check
  VPN/SSO" hint via `errors.Is` against typed network errors from `client-go`.
