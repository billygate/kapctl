# Port-forward pre-check and custom port mapping

**Status:** Design
**Date:** 2026-05-02
**Scope:** `internal/tui/panes/explorer.go`, `internal/tui/overlays/portselect.go`, `internal/portfwd/`, `internal/kube/types.go`

## Problem

The Explorer wizard's port-forward flow today picks a port from a fixed list (priority / detected / common) and immediately starts `kubectl port-forward` with `LocalPort == RemotePort == picked`. There is no way to:

1. Forward a non-standard port that isn't in the picker (e.g. 9229 → 9229).
2. Forward a remote port to a different local port (e.g. 8080 → 18080).
3. Catch a host-level local-port collision before kubectl is launched.
4. Catch a typo where the chosen remote port isn't actually exposed by the pod.

The first kubectl-side failure mode surfaces eventually as a `StatusErrored` toast, but it is opaque ("exit status 1") and gives no guidance.

## Goal

Replace the "pick → start" flow with "pick → confirm/edit → start", with hard pre-checks for both ports and an auto-bump for the local port when it is already in use on the host.

## Non-goals

- Persisting chosen ports to `~/.config/kap/config.yaml`. The existing `Ports` map (keyed `<ctx>.<ns>` → port) is read by `cmd/kap/pgsql.go` only; this spec does not extend or read from it.
- Updating the `pgsql` Cobra subcommand. It continues to use `overlays.PickPort` directly without a confirm/edit form.
- Supporting `--address 0.0.0.0`. The local-port probe binds `127.0.0.1`, matching kubectl's default.

## UX

### Wizard flow

```
stepContext → stepNamespace → stepPod → stepAction → stepPort → stepPortForm
                                                       │           │
                                                       │           ├─ enter (valid)  → emit PortForwardRequestMsg, back to stepPort
                                                       │           ├─ enter (invalid)→ set formErr, stay on stepPortForm
                                                       │           └─ esc            → back to stepPort (no re-fetch)
                                                       │
                                                       └─ select any item → enterPortForm(local, remote)
```

`stepPortForm` is a new terminal step in the FSM. Selecting any picker entry — including the new `CUSTOM` entry — transitions into the form rather than emitting `PortForwardRequestMsg` directly.

### Picker addition

`overlays.BuildPortChoices` prepends a new group:

```
── CUSTOM ──
custom (edit ports)
── PRIORITY ──
5432 (postgresql)
8080 (http-alt)
── DETECTED ──
…
── COMMON ──
…
```

`CUSTOM` is first because (1) it visually marks the "deliberate" choice and (2) digit `1` lands on it for fast keyboard access.

A new sentinel detector lives in the same file:

```go
const customPortLabel = "custom (edit ports)"

// IsCustomPortChoice reports whether the picker label is the CUSTOM sentinel.
func IsCustomPortChoice(label string) bool
```

`ParsePort` is unchanged and continues to error on the custom sentinel — callers must check `IsCustomPortChoice` first.

### Form layout

```
Port-forward for <pod>

  Local  : [ 5433       ]
  Remote : [ 5432       ]

  ℹ  local 5432 was in use, using 5433        ← Muted, present only when auto-bump fired

  tab: switch field  •  enter: start  •  esc: back
```

When validation fails on submit, the bottom line is replaced by a `styles.Warn`-styled error (e.g. `local port 5433 in use`, `remote port 5432 not declared in pod spec`).

### Form keys

| Key             | Action                                                           |
| --------------- | ---------------------------------------------------------------- |
| `tab`/`shift+tab` | Switch focus between Local and Remote                          |
| `enter`         | Validate; on success emit `PortForwardRequestMsg` and reset wizard |
| `esc`           | Back to `stepPort` (picker), preserving the previously built list |
| digits          | Routed to focused `textinput`; non-digits filtered via `Validate` |

Empty field on submit → error `"port is required"`.

### Auto-bump on form entry

When transitioning from `stepPort` to `stepPortForm` via a non-custom pick, the chosen port is the initial value for **both** Local and Remote. Before populating the Local field:

1. If `IsLocalPortFree(picked) == nil` → use `picked` as is, no hint shown.
2. Otherwise call `FindFreeLocalPort(picked, 100)`:
   - On success: populate Local with the bumped port; show the `ℹ  local <picked> was in use, using <bumped>` hint.
   - On failure (entire range busy): populate Local with `picked` and show a `Warn`-styled error `could not find a free local port in [<picked>, <picked>+100]`. The user can edit manually and submit.

The custom entry (`enterPortForm(0, 0)`) skips this — both fields start empty, no probe.

### Validation on submit

Both checks are synchronous. They run in this order; the first to fail sets `formErr` and returns:

1. Parse Local and Remote as integers in `[1, 65535]`. Empty or out-of-range → `formErr`, return.
2. `portfwd.IsLocalPortFree(local)` — error → `formErr = "local port <n> in use: <err>"`, return. Note: no auto-bump here. The user already saw and confirmed (or edited) the prefilled value; surprising them with a substitution at this point would defeat the purpose of the form.
3. `kube.HasContainerPort(podPorts, int32(remote))` — false → `formErr = "remote port <n> not declared in pod spec"`, return.
4. Emit `core.PortForwardRequestMsg{Context, Namespace, Target, Kind: KindPod, LocalPort: local, RemotePort: remote}` and transition back to `stepPort`. This matches today's behavior — `Explorer.handleSelect` does not reset the wizard after launching a forward (`explorer.go:422-423`); the user stays on the picker and can launch additional forwards or back out with `esc`.

`Manager.Start`'s in-memory conflict check (`manager.go:204-209`, refusing overlap on the same local port among our own forwards) is retained as defense-in-depth. The host-level `IsLocalPortFree` covers our forwards too — they bind locally — so the manager check is redundant in practice but cheap.

## Architecture

### File changes

| File | Change |
| --- | --- |
| `internal/portfwd/precheck.go` | **new** — `IsLocalPortFree`, `FindFreeLocalPort` |
| `internal/portfwd/precheck_test.go` | **new** — unit tests |
| `internal/kube/types.go` | add `HasContainerPort([]ContainerPort, int32) bool` |
| `internal/kube/types_test.go` | **new or extend** — test for `HasContainerPort` |
| `internal/tui/overlays/portselect.go` | prepend `CUSTOM` group; add `customPortLabel` constant; add `IsCustomPortChoice` |
| `internal/tui/overlays/portselect_test.go` | update `TestBuildPortChoices`; add `TestIsCustomPortChoice` |
| `internal/tui/panes/explorer.go` | add `stepPortForm`; form fields (`formLocal`, `formRemote`, `formFocus`, `formErr`, `formInfo`); `enterPortForm`, `submitPortForm`, form key routing; render in `View` and `viewTitle` |
| `internal/tui/panes/panes_test.go` | new test `TestExplorerPortFormFlow` |
| `CLAUDE.md` | one-paragraph note in the "Port-forward defaults" section |

### `internal/portfwd/precheck.go`

```go
package portfwd

import (
    "fmt"
    "net"
    "strconv"
)

// IsLocalPortFree probes whether 127.0.0.1:port can be bound. The
// listener is closed immediately. Returns nil if free, a non-nil error
// if the port is in use or the bind fails for any other reason.
func IsLocalPortFree(port int) error {
    l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
    if err != nil {
        return err
    }
    return l.Close()
}

// FindFreeLocalPort returns the first port in [start, start+span] for
// which IsLocalPortFree succeeds. Returns 0 and a non-nil error if no
// port in the range is free. span must be >= 0.
func FindFreeLocalPort(start, span int) (int, error) {
    if span < 0 {
        return 0, fmt.Errorf("portfwd: negative span %d", span)
    }
    for p := start; p <= start+span; p++ {
        if p < 1 || p > 65535 {
            continue
        }
        if err := IsLocalPortFree(p); err == nil {
            return p, nil
        }
    }
    return 0, fmt.Errorf("portfwd: no free port in [%d, %d]", start, start+span)
}
```

### `internal/kube/types.go` addition

```go
// HasContainerPort reports whether ports declares the given port number.
func HasContainerPort(ports []ContainerPort, port int32) bool {
    for _, p := range ports {
        if p.Port == port {
            return true
        }
    }
    return false
}
```

### Explorer state additions

`explorerStep` gets a new value appended to the existing iota block (`stepContext, stepNamespace, stepPod, stepAction, stepPort, stepPortForm`).

```go
type Explorer struct {
    // …existing fields…
    formLocal  textinput.Model
    formRemote textinput.Model
    formFocus  int    // 0 = local, 1 = remote
    formErr    string // styles.Warn — blocks submit
    formInfo   string // styles.Muted — informational (auto-bump hint)
}

func (e *Explorer) enterPortForm(local, remote int) {
    // Build textinputs with digit-only validators.
    // If local > 0: probe IsLocalPortFree; on miss, FindFreeLocalPort(local, 100).
    // Set formInfo on bump, formErr on range exhaustion.
    // step = stepPortForm
}

func (e *Explorer) submitPortForm() (*Explorer, tea.Cmd) {
    // Parse → validate Local (IsLocalPortFree) → validate Remote
    // (kube.HasContainerPort against e.podPorts) → emit
    // PortForwardRequestMsg and reset to stepContext.
}
```

The form is rendered when `e.step == stepPortForm`. The existing `usingTable()` returns false for the form step, so list-based rendering paths fall through to a custom branch in `View`.

## Tests

### `internal/portfwd/precheck_test.go`

1. `IsLocalPortFree` on a listener-occupied ephemeral port → error; after close → nil.
2. `IsLocalPortFree(0)` → error (bind error or invalid).
3. `FindFreeLocalPort` with three consecutive listeners on `start..start+2` → returns `start+3`.
4. `FindFreeLocalPort` with the entire range occupied → returns `0` and an error.
5. `FindFreeLocalPort` with negative span → error.

### `internal/kube/types_test.go`

1. `HasContainerPort` true for matching port.
2. False for missing port.
3. False for empty slice.

### `internal/tui/overlays/portselect_test.go`

Update `TestBuildPortChoices`: new first group is `── CUSTOM ──` followed by `custom (edit ports)`. Existing assertions for priority / detected / common shift down accordingly.

Add `TestIsCustomPortChoice`:
- `"custom (edit ports)"` → true
- `"5432 (postgresql)"` → false
- `"── PRIORITY ──"` → false
- `""` → false

### `internal/tui/panes/panes_test.go`

`TestExplorerPortFormFlow` — drive an Explorer with a fake `KubeClient` returning a pod with `containerPort 5432`:

1. Navigate to `stepPort`, select `5432 (postgresql)`. Assert `step == stepPortForm`, both fields = `"5432"`, `formInfo == ""`, `formErr == ""`.
2. New subtest: pre-bind a listener on a known free ephemeral port `P`, build picker choices that include `P`, simulate selecting `P`. Assert Local field shows `P+1` (or first free) and `formInfo` is non-empty.
3. From form with valid prefilled values, press `enter`. Assert one `PortForwardRequestMsg` emitted with `LocalPort` and `RemotePort` matching the field values; assert `step == stepPort` (back to picker).
4. Edit Remote to a port not in pod spec, press `enter`. Assert no message emitted, `formErr` non-empty, `step == stepPortForm`.
5. Select `custom (edit ports)`. Assert both fields empty, `formInfo == ""`, `formErr == ""`.

## Compatibility

- `Manager.Start` interface unchanged. `LocalPort != RemotePort` already works (`fmt.Sprintf("%d:%d", LocalPort, RemotePort)` in `DefaultCmdBuilder`).
- `cmd/kap/pgsql.go` is untouched. It continues to call `overlays.PickPort` and apply the picked port as both local and remote.
- `Manager`'s in-memory conflict check is retained.
- The `Ports` config map is not read or written by this change.

## Risks and follow-ups

- **TOCTOU between probe and kubectl bind.** Between `IsLocalPortFree(p) == nil` and kubectl's actual bind, another process can grab the port. The pre-check is best-effort; manager surfaces the kubectl error as a toast in that case. Acceptable.
- **`net.Listen` in unit tests is platform-dependent.** macOS and Linux both honor `127.0.0.1` listener ephemeral allocation; if CI ever runs Windows, revisit.
- **Form reusability.** If `pgsql` later needs custom mapping, the form should be extracted into `internal/tui/overlays/portform.go`. Out of scope here.
