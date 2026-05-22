# Port-forward auto-reconnect with health probes

Date: 2026-05-22
Status: Draft, awaiting user review

## Problem

Once `kapctl` starts a `kubectl port-forward` subprocess, the forward
silently breaks the moment the underlying pod restarts, is rescheduled,
or the SPDY stream goes stale:

- For Deployment-backed workloads, the original pod name disappears
  entirely. The `kubectl` subprocess exits non-zero, the entry flips to
  `StatusErrored`, and the user discovers the breakage only when their
  app fails to connect.
- For StatefulSet-backed workloads, the same pod name comes back with a
  different `UID`, but the subprocess does not always notice — sometimes
  it stays alive while the SPDY stream is half-open and traffic silently
  stops flowing.
- The user has to manually go back to EXPLORER, re-select the pod (or a
  new one), and start the forward again.

The session expectation for a TUI that "manages port-forwards" is that a
forward survives transient pod churn — the same way `kubectl port-forward`
itself does not, but `kapctl` should.

## Goals

- Detect a broken forward via both passive (subprocess exit) and active
  (k8s API poll + TCP probe) signals.
- Auto-reconnect with a bounded retry budget so a permanently dead target
  does not loop forever.
- For pod-forwards where the original pod is gone, find a replacement
  pod by the original owner's labels and forward to that.
- Make the reconnect lifecycle visible in the FORWARDS pane: new
  `Reconnecting` status, attempt counter, last reason.
- Keep service-forwards working through the same code path (kubectl
  re-resolves the backing pod for us; supervisor logic is identical).

## Non-goals

- Adding a UI affordance to start a service-forward (currently
  `KindService` is unreachable from the UI; that is a separate spec).
- Replacing the `kubectl port-forward` subprocess with an in-process
  SPDY client. Considered and rejected — keeps kubectl's free service
  resolution and avoids rewriting the existing module.
- Persisting forwards across `kapctl` restarts. Reconnect only operates
  while the tool is running.
- Configurable backoff schedule or budget per-forward. Constants are
  global, hard-coded.

## Design

### Architecture

The supervisor lives inside `internal/portfwd.Manager` — reconnect is
part of the subprocess lifecycle and belongs in the same package that
owns the subprocess. The TUI remains a pure consumer of `Events()`.

Two new collaborators are introduced into `Manager`:

1. **`Prober` interface** — abstracts the small set of k8s API
   operations needed for pod resolution and liveness checks. Lives in
   `internal/portfwd`; implementation lives in `internal/kube`.
2. **`Clock` interface** — `Now() time.Time` and `After(d) <-chan time.Time`.
   Existing code uses `time.Now`/`time.After` directly; supervisor
   testing needs deterministic time. Default impl is a thin shim.

`NewManager` gains two parameters: `proberFactory func(contextName string) (Prober, error)`
(per-context, because each forward can target a different kube context)
and `clock Clock`. Both are optional via a small `Options` struct — when
nil, the real implementations are wired in. Tests substitute fakes.

### New types

```go
// In internal/portfwd:

const (
    StatusStarting Status = iota
    StatusRunning
    StatusReconnecting       // NEW
    StatusErrored
    StatusStopped
)

type Prober interface {
    // ResolvePod returns a stable label set + UID for the named pod.
    // Used once at Start to record what to look for on reconnect.
    ResolvePod(ctx context.Context, namespace, name string) (PodRef, error)

    // GetPod returns the current state of the named pod, or NotFound.
    // Used by the periodic liveness check.
    GetPod(ctx context.Context, namespace, name string) (PodRef, error)

    // FindReadyPodByLabels returns the name of any Ready pod matching
    // labels. If multiple match, sort by name and return the first
    // Ready one (deterministic across attempts).
    FindReadyPodByLabels(ctx context.Context, namespace string, labels map[string]string) (string, error)
}

type PodRef struct {
    Name   string
    UID    string
    Labels map[string]string  // selectable labels (no pod-template-hash etc.)
    Phase  string             // "Running", "Pending", ...
    Ready  bool               // all containers ready
}

type Snapshot struct {
    // ...existing fields...
    Attempts            int        // reconnect attempts in current series; 0 when Running
    LastReconnectReason string     // populated when Status == Reconnecting or Errored from reconnect
    ReconnectStartedAt  time.Time  // zero when not reconnecting
}
```

`StartOpts` is unchanged at the call site. The Explorer pane keeps
passing what it passes today — Manager calls `Prober.ResolvePod` on
start to populate the per-entry label set.

### Supervisor state machine

One goroutine per entry, owned by Manager. Replaces the existing
`startProcess` + `waitDone` pair as the lifecycle driver.

```
loop:
  target := resolveTarget(ctx, entry)
    - KindService: returns entry.opts.Target verbatim
    - KindPod:
      - first iteration: use entry.opts.Target + record PodRef from ResolvePod
      - subsequent: FindReadyPodByLabels(entry.podLabels)
  if error → reconnectOrFail("resolve: " + err)

  startProcess(target) → wait for "Forwarding from" line on stderr
  if "Forwarding from" arrives:
    emit StatusRunning
    attempts = 0
    reconnectStartedAt = zero
  if subprocess exits before "Forwarding from":
    → reconnectOrFail("subprocess exited during startup")

  healthyLoop:
    select on:
      - subprocess exit (cmd.Wait done):
          → reconnectOrFail("subprocess exited: " + err)
      - tick every 5s:
          - GetPod(podName)
            - NotFound → reconnectOrFail("pod gone")
            - Phase != Running → reconnectOrFail("pod phase " + phase)
            - UID changed → reconnectOrFail("pod recreated")
          - TCP probe localhost:LocalPort (Dial with 500ms timeout)
            - failure → tcpFailCount++; success → tcpFailCount = 0
            - tcpFailCount >= 3 → reconnectOrFail("tcp probe failed x3")
      - ctx.Done() (user Stop):
          → killSubprocess; emit StatusStopped; return

reconnectOrFail(reason):
  killSubprocess (if still alive)
  if reconnectStartedAt is zero: reconnectStartedAt = clock.Now()
  attempts++
  lastReconnectReason = reason
  emit StatusReconnecting{Detail: reason, Attempts: attempts}

  if clock.Now().Sub(reconnectStartedAt) > 120s:
    emit StatusErrored{Detail: "reconnect budget exhausted: " + reason}
    return

  wait = backoffSchedule[min(attempts-1, len-1)]
  // backoffSchedule = [1s, 2s, 4s, 8s, 15s, 15s, 15s, ...]
  select { <-clock.After(wait): goto loop; <-ctx.Done(): emit Stopped; return }
```

#### State transition rules

1. **Budget resets on success.** Reaching `Running` (kubectl printed
   "Forwarding from") clears `attempts` and `reconnectStartedAt`. A
   later failure begins a fresh 120-second window. The 120s budget is
   "continuous attempt window", not "total forward lifetime".
2. **TCP probe only while `Running`.** No probing during `Starting`
   (nothing to hit yet) or `Reconnecting` (subprocess not running).
3. **Liveness tick interval: 5s.** Single `time.Ticker` drives both the
   k8s `GetPod` and the TCP probe — no separate timers.
4. **Pod considered gone if:** `GetPod` → NotFound, OR `Phase != "Running"`,
   OR `UID != entry.podUID`.
5. **TCP probe debounce: 3 consecutive failures.** One miss is not a
   signal (transient TCP timeout). Three misses (~15s) triggers
   reconnect.
6. **User Stop wins races.** `ctx.Done()` in any wait state cancels
   cleanly without one more reconnect iteration.

#### Resolving labels

`Prober.ResolvePod` returns selectable labels — not raw `pod.Labels`,
which include `pod-template-hash` and `controller-revision-hash`. The
implementation in `internal/kube`:

1. `Pods(ns).Get(name)` → if `OwnerReferences` empty, return the pod's
   own labels minus the blacklist (`pod-template-hash`,
   `controller-revision-hash`, `statefulset.kubernetes.io/pod-name`).
2. Otherwise look up the owner. For `ReplicaSet` → walk to `Deployment`
   if `Controller=true` parent exists, else use ReplicaSet's
   `Spec.Selector.MatchLabels`. For `StatefulSet`/`DaemonSet` → use
   their `Spec.Selector.MatchLabels` directly.
3. Errors at any owner step → fall back to the blacklisted-filter
   approach on the pod's own labels.

### UI changes (FORWARDS pane)

Three small changes to `internal/tui/panes/forwards.go`:

1. **New status string.** `Status.String()` for `StatusReconnecting`
   returns `"reconnecting"`.
2. **STATUS column shows attempt count and remaining budget when
   reconnecting.** Format: `"reconnecting 3/87s"` — attempt number and
   remaining seconds in the budget. When `Running`, just `"running"`.
3. **Toast policy** (`eventToToast`):
   - First `Reconnecting` event of a series → info toast
     `"forward N reconnecting: <reason>"`.
   - Subsequent `Reconnecting` events of the same series → silent
     (table reflects state, toast spam is hostile).
   - `Running` after `Reconnecting` → success toast
     `"forward N reconnected"`.
   - `Errored` after budget exhaustion → error toast
     `"forward N gave up after Ns: <reason>"`.

The first-of-series check uses `Attempts == 1` on the incoming Event.

No new keybindings. `d` (stop) already cancels the supervisor's ctx and
exits the loop without further reconnects.

### Config and defaults

No new config entries. Constants in `internal/portfwd`:

```go
const (
    reconnectBudget    = 120 * time.Second
    livenessTick       = 5 * time.Second
    tcpProbeTimeout    = 500 * time.Millisecond
    tcpProbeFailThresh = 3
)
var backoffSchedule = []time.Duration{
    1*time.Second, 2*time.Second, 4*time.Second, 8*time.Second,
    15*time.Second, 15*time.Second, 15*time.Second, 15*time.Second,
    15*time.Second, 15*time.Second, 15*time.Second,
}
```

Rationale for not exposing these in `~/.config/kapctl/config.yaml`:
YAGNI. We can promote them to config later if users actually ask. The
in-package constants make tests and the spec easier to reason about.

### Error handling

- **`ResolvePod` fails at Start.** `Manager.Start` returns the error
  synchronously (same shape as today's invalid-port error). Forward is
  not registered.
- **`FindReadyPodByLabels` finds nothing on reconnect.** Counts as one
  attempt; supervisor waits backoff and tries again. Within budget this
  gives the cluster ~10 retries to bring a pod up.
- **`GetPod` transient errors during liveness tick.** Logged to the
  entry ring buffer but not treated as "pod gone" — only NotFound
  triggers reconnect. Network blips should not cause forward churn.
- **kubectl subprocess refuses to start (e.g., kubectl missing).**
  Existing behavior preserved — `Manager.Start` propagates the error.
- **Prober factory returns error at Start.** `Manager.Start` returns
  it; forward not registered.

### Testing

Existing tests in `internal/portfwd/manager_test.go` already use a fake
`CmdBuilder` that points at a small Go binary controlled by env vars
(see `TestForwardLifecycle`). Extend this pattern:

1. **Unit tests for `Prober` (in `internal/kube`).** Use `fake.NewClientset`
   with crafted ReplicaSet/StatefulSet objects. Verify label filtering
   (drop `pod-template-hash`), owner traversal (ReplicaSet → Deployment),
   and the bare-pod fallback.
2. **Supervisor state-machine tests.** Use a `fakeClock` (channel-driven
   `After`) + `fakeProber` + the existing fake kubectl binary.
   Scenarios:
   - **Happy reconnect.** Start → Running → fake subprocess exits with
     code 1 → Reconnecting (attempt 1) → Running. Assert `Attempts=0`
     post-recovery and one info + one success toast event.
   - **Pod-gone via watch.** Start → Running → `fakeProber.GetPod`
     starts returning NotFound → Reconnecting → `FindReadyPodByLabels`
     returns new pod name → Running with new `Target` recorded.
   - **UID change.** Same as above but `GetPod` returns same name with
     different UID.
   - **TCP probe debounce.** Two consecutive TCP failures do not
     trigger reconnect; three do. (Use a `net.Listen` that the test
     closes mid-flight.)
   - **Budget exhaustion.** `FindReadyPodByLabels` always returns
     NotFound; advance fake clock past 120s; assert terminal
     `StatusErrored` with budget-exhausted detail.
   - **User Stop during backoff.** Enter Reconnecting, then `Stop(id)`;
     supervisor exits without another attempt; status is Stopped.
   - **Stop during healthy run.** Existing test, verify no regression.
3. **TUI snapshot.** Add a `Forwards` pane test that feeds a
   `core.PortForwardEventMsg` with `StatusReconnecting` and asserts the
   STATUS column renders `"reconnecting N/Ms"` shape.

No e2e test is added — these require a live cluster and are out of
scope for unit-level CI.

## Migration / compatibility

- `StartOpts` unchanged at API level.
- `Snapshot` gains fields; consumers (currently only `forwards.go`) get
  zero values when irrelevant.
- `Status` enum gains a value mid-range; `String()` updated. No callers
  switch on the integer value.
- `NewManager` signature changes to accept `Options{ProberFactory, Clock}`.
  Single caller is `cmd/kapctl/root.go` → minor edit.
- No on-disk format changes.

## Open questions resolved during brainstorming

- **Reconnect policy:** always auto-reconnect; only explicit `d`
  cancels. (Other options: limit by attempts, opt-in. Rejected.)
- **Pod re-resolution:** by labels of original owner. (Other options:
  same name only; auto-promote to service. Rejected.)
- **Health detection:** k8s API poll + TCP probe. (Other options:
  subprocess exit only; TCP only. Rejected — both signals are needed
  to cover the half-open SPDY case.)
- **Budget:** hard 120s window per reconnect series, resets on
  `Running`.
- **Unifying pod/service forwards in Manager:** one supervisor, branch
  only in `resolveTarget`. Two `Kind`s preserved at API surface because
  they mean different things to the user (specific pod vs any backing
  pod).
