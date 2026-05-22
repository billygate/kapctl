# Port-forward auto-reconnect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every active port-forward survive transient pod restarts by adding a supervisor inside `portfwd.Manager` that detects breakage (subprocess exit, pod gone from k8s API, TCP probe fail) and auto-reconnects within a 120s budget, including label-based pod re-resolution.

**Architecture:** One goroutine per entry runs the full lifecycle as a loop: resolve target → start kubectl → wait for "Forwarding from" → healthy-watch loop (select on subprocess exit / 5s liveness tick / user Stop) → on failure, emit `StatusReconnecting`, backoff, loop again until budget exhausted. A new `Prober` interface (implemented by `internal/kube.Client`) handles pod label resolution and liveness checks. A new `Clock` interface lets tests drive time deterministically.

**Tech Stack:** Go 1.26, `k8s.io/client-go` (existing dependency, fake client for tests), Bubble Tea for the FORWARDS pane updates.

**Spec:** `docs/superpowers/specs/2026-05-22-portfwd-auto-reconnect-design.md`

---

## File Structure

**Create:**
- `internal/portfwd/prober.go` — `Prober` interface, `PodRef` type
- `internal/portfwd/clock.go` — `Clock` interface + `realClock` default
- `internal/portfwd/supervisor.go` — supervisor goroutine (extracted lifecycle driver)
- `internal/portfwd/supervisor_test.go` — state machine tests with fakes
- `internal/portfwd/fakes_test.go` — `fakeClock`, `fakeProber` shared test fixtures
- `internal/kube/prober.go` — `*kube.Client` implements `portfwd.Prober`
- `internal/kube/prober_test.go` — Prober tests with `fake.NewClientset`

**Modify:**
- `internal/portfwd/manager.go` — add `StatusReconnecting`, `Snapshot` fields, `SetProberFactory`/`SetClock` setters, delete `startProcess`/`waitDone` (now in supervisor)
- `internal/portfwd/manager_test.go` — update `TestManagerErrorPath` to expect new reconnect behavior
- `internal/tui/panes/forwards.go` — Reconnecting status string format, toast policy
- `internal/tui/panes/panes_test.go` — render test for reconnecting row
- `internal/tui/app.go` — pass prober factory when constructing `Manager`

---

## Task 1: Add `StatusReconnecting` and `Snapshot` fields

**Files:**
- Modify: `internal/portfwd/manager.go:25-45` (Status enum + String), `manager.go:74-87` (Snapshot)

- [ ] **Step 1: Add the new status value**

In `internal/portfwd/manager.go`, add `StatusReconnecting` between `StatusRunning` and `StatusErrored`:

```go
const (
    StatusStarting Status = iota
    StatusRunning
    StatusReconnecting
    StatusErrored
    StatusStopped
)
```

And extend `Status.String()`:

```go
func (s Status) String() string {
    switch s {
    case StatusStarting:
        return "starting"
    case StatusRunning:
        return "running"
    case StatusReconnecting:
        return "reconnecting"
    case StatusErrored:
        return "errored"
    case StatusStopped:
        return "stopped"
    }
    return "?"
}
```

- [ ] **Step 2: Extend `Snapshot`**

In the same file, add three fields to `Snapshot`:

```go
type Snapshot struct {
    ID         string
    Context    string
    Namespace  string
    Target     string
    Kind       Kind
    LocalPort  int
    RemotePort int
    Status     Status
    StartedAt  time.Time
    LastError  string

    Attempts            int       // reconnect attempts in current series; 0 when Running
    LastReconnectReason string    // populated while Reconnecting or after Errored-from-reconnect
    ReconnectStartedAt  time.Time // zero when not reconnecting
}
```

And update `entry.snapshot()` (around manager.go:169) to populate them — add fields `attempts int`, `lastReconnectReason string`, `reconnectStartedAt time.Time` to the `entry` struct:

```go
type entry struct {
    mu        sync.Mutex
    id        string
    opts      StartOpts
    status    Status
    startedAt time.Time
    lastErr   string
    logs      *ringBuf

    // Reconnect state
    attempts            int
    lastReconnectReason string
    reconnectStartedAt  time.Time

    // Resolved pod identity (for KindPod). Recorded on first successful
    // resolution; used to re-find the pod by labels and detect UID-change.
    podUID    string
    podLabels map[string]string

    cmd    *exec.Cmd
    cancel context.CancelFunc
    doneCh chan struct{}
}

func (e *entry) snapshot() Snapshot {
    e.mu.Lock()
    defer e.mu.Unlock()
    return Snapshot{
        ID:                  e.id,
        Context:             e.opts.Context,
        Namespace:           e.opts.Namespace,
        Target:              e.opts.Target,
        Kind:                e.opts.Kind,
        LocalPort:           e.opts.LocalPort,
        RemotePort:          e.opts.RemotePort,
        Status:              e.status,
        StartedAt:           e.startedAt,
        LastError:           e.lastErr,
        Attempts:            e.attempts,
        LastReconnectReason: e.lastReconnectReason,
        ReconnectStartedAt:  e.reconnectStartedAt,
    }
}
```

- [ ] **Step 3: Run existing tests to confirm no regression**

Run: `go test ./internal/portfwd/...`
Expected: PASS (status string `reconnecting` is new but no code path emits it yet; new Snapshot fields are zero in all existing assertions).

- [ ] **Step 4: Commit**

```bash
git add internal/portfwd/manager.go
git commit -m "portfwd: add StatusReconnecting and reconnect Snapshot fields"
```

---

## Task 2: Add `Prober` interface and `PodRef` type

**Files:**
- Create: `internal/portfwd/prober.go`

- [ ] **Step 1: Create the file with the interface**

`internal/portfwd/prober.go`:

```go
package portfwd

import "context"

// PodRef is the subset of pod state the supervisor needs.
type PodRef struct {
    Name   string
    UID    string
    Phase  string            // "Running", "Pending", ...
    Ready  bool              // all containers ready
    Labels map[string]string // selectable labels (no pod-template-hash etc.)
}

// Prober is the small slice of k8s API operations the supervisor uses
// for pod resolution and liveness checks. The real implementation lives
// in internal/kube; tests substitute a fake.
type Prober interface {
    // ResolvePod returns a stable label set + UID for the named pod.
    // Called once at Start to capture re-resolution inputs.
    ResolvePod(ctx context.Context, namespace, name string) (PodRef, error)

    // GetPod returns current state of the named pod. NotFound is signalled
    // via err.Is(err, ErrPodNotFound). Other errors are transient.
    GetPod(ctx context.Context, namespace, name string) (PodRef, error)

    // FindReadyPodByLabels returns the name of any Ready pod matching
    // labels in namespace, sorted by name for determinism. Returns
    // ErrPodNotFound when no candidate is Ready.
    FindReadyPodByLabels(ctx context.Context, namespace string, labels map[string]string) (string, error)
}

// ProberFactory builds a Prober for a given kube context. Each forward
// may target a different context, so the factory is invoked once per
// Start with the entry's context name.
type ProberFactory func(contextName string) (Prober, error)
```

- [ ] **Step 2: Define `ErrPodNotFound`**

Append to `internal/portfwd/prober.go`:

```go
import (
    "context"
    "errors"
)

// ErrPodNotFound is returned by Prober when the target pod is absent
// from the cluster. The supervisor treats this as a reconnect trigger
// (NOT as a fatal Start error after the initial resolution).
var ErrPodNotFound = errors.New("portfwd: pod not found")
```

(Adjust the imports block at the top to include `errors`.)

- [ ] **Step 3: Verify the package compiles**

Run: `go build ./internal/portfwd/...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add internal/portfwd/prober.go
git commit -m "portfwd: add Prober interface and PodRef type"
```

---

## Task 3: Add `Clock` abstraction

**Files:**
- Create: `internal/portfwd/clock.go`

- [ ] **Step 1: Define the interface and real impl**

`internal/portfwd/clock.go`:

```go
package portfwd

import "time"

// Clock abstracts time for the supervisor so tests can drive it
// deterministically. Production code uses realClock.
type Clock interface {
    Now() time.Time
    After(d time.Duration) <-chan time.Time
    NewTicker(d time.Duration) Ticker
}

// Ticker mirrors *time.Ticker's surface — channel + Stop.
type Ticker interface {
    C() <-chan time.Time
    Stop()
}

type realClock struct{}

func (realClock) Now() time.Time                        { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTicker(d time.Duration) Ticker       { return realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }
```

- [ ] **Step 2: Verify**

Run: `go build ./internal/portfwd/...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/portfwd/clock.go
git commit -m "portfwd: add Clock abstraction for testable timing"
```

---

## Task 4: Add `SetProberFactory` / `SetClock` setters to Manager

**Files:**
- Modify: `internal/portfwd/manager.go` (Manager struct, NewManager, add setters)

- [ ] **Step 1: Add the fields**

In `manager.go` Manager struct (around line 117), add `proberFactory` and `clock`:

```go
type Manager struct {
    mu       sync.Mutex
    entries  map[string]*entry
    nextID   atomic.Uint64
    events   chan Event
    builder  CmdBuilder
    logCap   int
    stopping bool

    proberFactory ProberFactory // nil → no pod re-resolution; KindPod uses original Target each attempt
    clock         Clock         // nil → realClock
}
```

- [ ] **Step 2: Initialise `clock` in `NewManager`**

Update `NewManager` (around line 129):

```go
func NewManager(eventsCap, logCap int) *Manager {
    if eventsCap <= 0 {
        eventsCap = 32
    }
    if logCap <= 0 {
        logCap = 256
    }
    return &Manager{
        entries: map[string]*entry{},
        events:  make(chan Event, eventsCap),
        builder: DefaultCmdBuilder,
        logCap:  logCap,
        clock:   realClock{},
    }
}
```

- [ ] **Step 3: Add the setters**

Below `SetCmdBuilder` (around line 145):

```go
// SetProberFactory installs the factory used to build Probers for new
// entries. When nil (the default), KindPod entries do not re-resolve
// the target — they keep retrying the original pod name.
func (m *Manager) SetProberFactory(f ProberFactory) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.proberFactory = f
}

// SetClock overrides the time source. Used by tests.
func (m *Manager) SetClock(c Clock) {
    m.mu.Lock()
    defer m.mu.Unlock()
    if c == nil {
        c = realClock{}
    }
    m.clock = c
}
```

- [ ] **Step 4: Compile + run existing tests**

Run: `go test ./internal/portfwd/...`
Expected: PASS (no behavior change yet).

- [ ] **Step 5: Commit**

```bash
git add internal/portfwd/manager.go
git commit -m "portfwd: add SetProberFactory and SetClock setters"
```

---

## Task 5: Create shared test fakes

**Files:**
- Create: `internal/portfwd/fakes_test.go`

- [ ] **Step 1: Write the file**

`internal/portfwd/fakes_test.go`:

```go
package portfwd

import (
    "context"
    "sort"
    "sync"
    "time"
)

// fakeClock implements Clock with manual advancement. Tests call
// Advance(d) to release pending After/Ticker channels.
type fakeClock struct {
    mu      sync.Mutex
    now     time.Time
    waiters []fakeWaiter
}

type fakeWaiter struct {
    deadline time.Time
    ch       chan time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
    c.mu.Lock()
    defer c.mu.Unlock()
    ch := make(chan time.Time, 1)
    c.waiters = append(c.waiters, fakeWaiter{deadline: c.now.Add(d), ch: ch})
    return ch
}

// NewTicker returns a ticker driven by Advance(). The ticker keeps
// firing as long as Advance crosses a tick boundary.
func (c *fakeClock) NewTicker(d time.Duration) Ticker {
    ch := make(chan time.Time, 16)
    ft := &fakeTicker{ch: ch, period: d, next: c.Now().Add(d), clock: c}
    c.mu.Lock()
    c.tickers = append(c.tickers, ft)
    c.mu.Unlock()
    return ft
}

type fakeTicker struct {
    ch     chan time.Time
    period time.Duration
    next   time.Time
    clock  *fakeClock
    stop   bool
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()               { t.stop = true }

// Advance moves time forward, firing any After-waiters and ticker
// boundaries that have come due. Call from the test goroutine; the
// supervisor goroutine will receive on the released channels.
func (c *fakeClock) Advance(d time.Duration) {
    c.mu.Lock()
    c.now = c.now.Add(d)
    target := c.now

    // Fire After waiters.
    remaining := c.waiters[:0]
    for _, w := range c.waiters {
        if !w.deadline.After(target) {
            select {
            case w.ch <- target:
            default:
            }
        } else {
            remaining = append(remaining, w)
        }
    }
    c.waiters = remaining

    // Advance tickers.
    for _, t := range c.tickers {
        if t.stop {
            continue
        }
        for !t.next.After(target) {
            select {
            case t.ch <- t.next:
            default:
            }
            t.next = t.next.Add(t.period)
        }
    }
    c.mu.Unlock()
}

// fakeClock needs a tickers field; add it above.

// fakeProber is a programmable Prober for supervisor tests.
type fakeProber struct {
    mu sync.Mutex

    resolveResp map[string]PodRef // key: ns/name
    getResp     map[string]PodRef
    getNotFound map[string]bool
    findResp    map[string]string // key: ns/<sorted-labels>; value = pod name
    findNotFound map[string]bool
    findCalls   int
    getCalls    int
}

func newFakeProber() *fakeProber {
    return &fakeProber{
        resolveResp:  map[string]PodRef{},
        getResp:      map[string]PodRef{},
        getNotFound:  map[string]bool{},
        findResp:     map[string]string{},
        findNotFound: map[string]bool{},
    }
}

func (f *fakeProber) ResolvePod(_ context.Context, ns, name string) (PodRef, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    key := ns + "/" + name
    if f.getNotFound[key] {
        return PodRef{}, ErrPodNotFound
    }
    if r, ok := f.resolveResp[key]; ok {
        return r, nil
    }
    return PodRef{Name: name, UID: "uid-" + name, Phase: "Running", Ready: true, Labels: map[string]string{"app": "demo"}}, nil
}

func (f *fakeProber) GetPod(_ context.Context, ns, name string) (PodRef, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.getCalls++
    key := ns + "/" + name
    if f.getNotFound[key] {
        return PodRef{}, ErrPodNotFound
    }
    if r, ok := f.getResp[key]; ok {
        return r, nil
    }
    return PodRef{Name: name, UID: "uid-" + name, Phase: "Running", Ready: true}, nil
}

func (f *fakeProber) FindReadyPodByLabels(_ context.Context, ns string, labels map[string]string) (string, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.findCalls++
    key := ns + "/" + labelKey(labels)
    if f.findNotFound[key] {
        return "", ErrPodNotFound
    }
    if name, ok := f.findResp[key]; ok {
        return name, nil
    }
    return "", ErrPodNotFound
}

func labelKey(m map[string]string) string {
    keys := make([]string, 0, len(m))
    for k := range m {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    out := ""
    for _, k := range keys {
        out += k + "=" + m[k] + ","
    }
    return out
}
```

Also add `tickers []*fakeTicker` to the `fakeClock` struct definition (the inline comment notes this — fix it now):

```go
type fakeClock struct {
    mu      sync.Mutex
    now     time.Time
    waiters []fakeWaiter
    tickers []*fakeTicker
}
```

- [ ] **Step 2: Verify the file compiles in the test context**

Run: `go test ./internal/portfwd/ -run XXX_does_not_match -count=1`
Expected: PASS (no tests match the filter; compilation succeeds).

- [ ] **Step 3: Commit**

```bash
git add internal/portfwd/fakes_test.go
git commit -m "portfwd: add fakeClock and fakeProber test fixtures"
```

---

## Task 6: Extract supervisor goroutine (behavior-preserving refactor)

**Files:**
- Create: `internal/portfwd/supervisor.go`
- Modify: `internal/portfwd/manager.go` (replace `startProcess`/`waitDone` call sites; keep helpers private to supervisor)

This task moves the existing lifecycle (Start → Running on "Forwarding from" → Stopped/Errored on cmd exit) into one goroutine in a loop body — **but only one iteration runs**. No reconnect yet. Existing tests must still pass.

- [ ] **Step 1: Write `supervisor.go`**

`internal/portfwd/supervisor.go`:

```go
package portfwd

import (
    "bufio"
    "context"
    "errors"
    "io"
    "strings"
    "syscall"
    "time"
)

// supervise drives the lifecycle of a single entry. With reconnect
// disabled (this task) it runs exactly one iteration: start kubectl,
// stream logs, wait for exit, emit terminal status.
func (m *Manager) supervise(ctx context.Context, e *entry) {
    defer close(e.doneCh)

    e.cmd = m.builder(e.opts)

    stderr, err := e.cmd.StderrPipe()
    if err != nil {
        m.terminalError(e, err)
        return
    }
    stdout, err := e.cmd.StdoutPipe()
    if err != nil {
        m.terminalError(e, err)
        return
    }
    if err := e.cmd.Start(); err != nil {
        m.terminalError(e, err)
        return
    }

    // Cancel watcher: on ctx.Done() send SIGTERM, escalate to SIGKILL after 2s.
    procDone := make(chan struct{})
    go func() {
        select {
        case <-ctx.Done():
            if p := e.cmd.Process; p != nil {
                _ = p.Signal(syscall.SIGTERM)
                select {
                case <-procDone:
                case <-time.After(2 * time.Second):
                    _ = p.Kill()
                }
            }
        case <-procDone:
        }
    }()

    go m.readLogs(e, stderr, true)
    go m.readLogs(e, stdout, false)

    err = e.cmd.Wait()
    close(procDone)

    e.mu.Lock()
    final := StatusStopped
    detail := ""
    if err != nil && !errors.Is(err, context.Canceled) {
        if e.status != StatusStopped {
            final = StatusErrored
            detail = err.Error()
            e.lastErr = detail
        }
    }
    e.status = final
    e.mu.Unlock()
    m.emit(Event{ID: e.id, Status: final, Detail: detail})
}

// terminalError is the early-exit path when StderrPipe/StdoutPipe/Start
// itself fails before we ever had a running process.
func (m *Manager) terminalError(e *entry, err error) {
    e.mu.Lock()
    e.status = StatusErrored
    e.lastErr = err.Error()
    e.mu.Unlock()
    m.emit(Event{ID: e.id, Status: StatusErrored, Detail: err.Error()})
}

// readLogs scans a pipe line-by-line into the entry's ring buffer.
// On stderr we look for kubectl's "Forwarding from" line and flip the
// status from Starting → Running on first sight.
func (m *Manager) readLogs(e *entry, r io.Reader, watchForReady bool) {
    scanner := bufio.NewScanner(r)
    scanner.Buffer(make([]byte, 4096), 1<<20)
    for scanner.Scan() {
        line := scanner.Text()
        e.logs.Push(line)
        if watchForReady && strings.Contains(line, "Forwarding from") {
            e.mu.Lock()
            if e.status == StatusStarting {
                e.status = StatusRunning
                e.mu.Unlock()
                m.emit(Event{ID: e.id, Status: StatusRunning, Detail: line})
                continue
            }
            e.mu.Unlock()
        }
    }
}
```

- [ ] **Step 2: Wire `Start` to use `supervise`**

In `internal/portfwd/manager.go`, replace the existing `Start` body (around line 189-233) with one that kicks off `supervise`:

```go
func (m *Manager) Start(opts StartOpts) (string, error) {
    if opts.LocalPort <= 0 || opts.RemotePort <= 0 {
        return "", errors.New("portfwd: invalid port (0)")
    }
    if opts.Target == "" {
        return "", errors.New("portfwd: empty target")
    }

    m.mu.Lock()
    if m.stopping {
        m.mu.Unlock()
        return "", errors.New("portfwd: manager closed")
    }
    for _, ex := range m.entries {
        if ex.opts.LocalPort == opts.LocalPort && ex.statusRead() != StatusStopped && ex.statusRead() != StatusErrored {
            m.mu.Unlock()
            return "", fmt.Errorf("portfwd: local port %d already forwarded by %s", opts.LocalPort, ex.id)
        }
    }
    id := strconv.FormatUint(m.nextID.Add(1), 10)
    ctx, cancel := context.WithCancel(context.Background())
    e := &entry{
        id:        id,
        opts:      opts,
        status:    StatusStarting,
        startedAt: time.Now(),
        logs:      newRingBuf(m.logCap),
        cancel:    cancel,
        doneCh:    make(chan struct{}),
    }
    m.entries[id] = e
    m.mu.Unlock()

    go m.supervise(ctx, e)
    return id, nil
}
```

- [ ] **Step 3: Delete the old `startProcess`, `readLogs`, `waitDone` from `manager.go`**

Remove them — they now live in `supervisor.go`. Keep `Stop`, `Remove`, `List`, `Logs`, `StopAll`, `emit` unchanged.

- [ ] **Step 4: Run existing tests**

Run: `go test ./internal/portfwd/... -count=1 -timeout 30s`
Expected: PASS — all four existing tests (`TestManagerStartRunningStop`, `TestManagerErrorPath`, `TestManagerLocalPortConflict`, `TestManagerStopAllReleasesEverything`) still pass.

- [ ] **Step 5: Run full test suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/portfwd/
git commit -m "portfwd: extract supervisor goroutine (no behavior change)"
```

---

## Task 7: Reconnect on subprocess exit (no probes yet)

**Files:**
- Modify: `internal/portfwd/supervisor.go` (wrap body in a loop, add reconnect bookkeeping)
- Create: `internal/portfwd/supervisor_test.go` (happy reconnect test)
- Modify: `internal/portfwd/manager_test.go` (update TestManagerErrorPath)

- [ ] **Step 1: Write the failing test**

Create `internal/portfwd/supervisor_test.go`:

```go
package portfwd

import (
    "os/exec"
    "strings"
    "testing"
    "time"
)

// flakyCmd exits non-zero the first N times, then prints "Forwarding from"
// and sleeps. Used to test reconnect-on-subprocess-exit.
func flakyCmdBuilder(failuresBeforeSuccess int) (CmdBuilder, func() int) {
    var calls int32
    var ptr = &calls
    return func(opts StartOpts) *exec.Cmd {
            n := atomicAddInt32(ptr, 1)
            port := "5432"
            if n <= int32(failuresBeforeSuccess) {
                return exec.Command("/bin/sh", "-c", "exit 1")
            }
            script := `printf 'Forwarding from 127.0.0.1:` + port + ` -> ` + port + `\n' >&2; sleep 5`
            return exec.Command("/bin/sh", "-c", script)
        }, func() int {
            return int(atomicLoadInt32(ptr))
        }
}

// atomic helpers without importing sync/atomic into every test file.
func atomicAddInt32(p *int32, d int32) int32 {
    *p += d
    return *p
}
func atomicLoadInt32(p *int32) int32 { return *p }

func TestSupervisorReconnectsAfterSubprocessExit(t *testing.T) {
    m := NewManager(16, 64)
    builder, _ := flakyCmdBuilder(1) // fail once, then succeed
    m.SetCmdBuilder(builder)

    clk := newFakeClock(time.Now())
    m.SetClock(clk)

    id, err := m.Start(StartOpts{
        Context: "ctx", Namespace: "ns", Target: "pg-0",
        Kind: KindPod, LocalPort: 5432, RemotePort: 5432,
    })
    if err != nil {
        t.Fatalf("Start: %v", err)
    }

    // First attempt fails → Reconnecting.
    ev := waitFor(t, m.Events(), StatusReconnecting, 3*time.Second)
    if ev.ID != id {
        t.Fatalf("Reconnecting event ID = %q, want %q", ev.ID, id)
    }
    if !strings.Contains(strings.ToLower(ev.Detail), "subprocess") {
        t.Errorf("Reconnecting Detail = %q, want it to mention subprocess", ev.Detail)
    }

    // Advance fake clock past backoff (1s for first attempt).
    clk.Advance(2 * time.Second)

    // Second attempt succeeds → Running.
    waitFor(t, m.Events(), StatusRunning, 3*time.Second)

    // Attempts should be reset.
    list := m.List()
    if len(list) != 1 || list[0].Attempts != 0 {
        t.Errorf("after recovery: Attempts = %d, want 0; list=%+v", list[0].Attempts, list)
    }

    _ = m.Stop(id)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/portfwd/ -run TestSupervisorReconnectsAfterSubprocessExit -v -timeout 30s`
Expected: FAIL — no reconnect logic yet; current code goes directly to StatusErrored on first failure.

- [ ] **Step 3: Wrap the supervisor body in a reconnect loop**

Update `internal/portfwd/supervisor.go` — replace the entire function:

```go
package portfwd

import (
    "bufio"
    "context"
    "errors"
    "io"
    "strings"
    "syscall"
    "time"
)

const (
    reconnectBudget = 120 * time.Second
)

var backoffSchedule = []time.Duration{
    1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
    15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second,
    15 * time.Second, 15 * time.Second, 15 * time.Second,
}

func (m *Manager) supervise(ctx context.Context, e *entry) {
    defer close(e.doneCh)

    for {
        // Reset per-iteration state.
        e.mu.Lock()
        e.status = StatusStarting
        e.mu.Unlock()
        m.emit(Event{ID: e.id, Status: StatusStarting})

        reason, exitedClean := m.runOneAttempt(ctx, e)

        // ctx was cancelled by Stop — exit cleanly without reconnect.
        if ctx.Err() != nil {
            e.mu.Lock()
            if e.status != StatusStopped {
                e.status = StatusStopped
            }
            e.mu.Unlock()
            m.emit(Event{ID: e.id, Status: StatusStopped})
            return
        }

        if exitedClean {
            e.mu.Lock()
            e.status = StatusStopped
            e.mu.Unlock()
            m.emit(Event{ID: e.id, Status: StatusStopped})
            return
        }

        // Failure path: record + emit Reconnecting, then back off.
        e.mu.Lock()
        if e.reconnectStartedAt.IsZero() {
            e.reconnectStartedAt = m.clock.Now()
        }
        e.attempts++
        e.lastReconnectReason = reason
        elapsed := m.clock.Now().Sub(e.reconnectStartedAt)
        attempts := e.attempts
        e.status = StatusReconnecting
        e.mu.Unlock()
        m.emit(Event{ID: e.id, Status: StatusReconnecting, Detail: reason})

        if elapsed > reconnectBudget {
            e.mu.Lock()
            e.status = StatusErrored
            e.lastErr = "reconnect budget exhausted: " + reason
            e.mu.Unlock()
            m.emit(Event{ID: e.id, Status: StatusErrored, Detail: "reconnect budget exhausted: " + reason})
            return
        }

        wait := backoffSchedule[len(backoffSchedule)-1]
        if attempts-1 < len(backoffSchedule) {
            wait = backoffSchedule[attempts-1]
        }
        select {
        case <-m.clock.After(wait):
            // loop
        case <-ctx.Done():
            e.mu.Lock()
            e.status = StatusStopped
            e.mu.Unlock()
            m.emit(Event{ID: e.id, Status: StatusStopped})
            return
        }
    }
}

// runOneAttempt launches kubectl once. Returns (reason, exitedClean):
//   - exitedClean=true: subprocess exited with no error AND was not interrupted (rare/impossible
//     for kubectl port-forward, but kept for completeness)
//   - exitedClean=false: subprocess failed/exited unexpectedly; reason describes why
//
// On successful "Forwarding from", clears Attempts and ReconnectStartedAt
// before settling into the wait-for-exit phase.
func (m *Manager) runOneAttempt(ctx context.Context, e *entry) (reason string, exitedClean bool) {
    e.cmd = m.builder(e.opts)

    stderr, err := e.cmd.StderrPipe()
    if err != nil {
        return "stderr pipe: " + err.Error(), false
    }
    stdout, err := e.cmd.StdoutPipe()
    if err != nil {
        return "stdout pipe: " + err.Error(), false
    }
    if err := e.cmd.Start(); err != nil {
        return "kubectl start: " + err.Error(), false
    }

    procDone := make(chan struct{})
    go func() {
        select {
        case <-ctx.Done():
            if p := e.cmd.Process; p != nil {
                _ = p.Signal(syscall.SIGTERM)
                select {
                case <-procDone:
                case <-time.After(2 * time.Second):
                    _ = p.Kill()
                }
            }
        case <-procDone:
        }
    }()

    // Watcher channel for "Forwarding from".
    readyCh := make(chan struct{}, 1)
    go m.readLogs(e, stderr, true, readyCh)
    go m.readLogs(e, stdout, false, nil)

    waitErr := make(chan error, 1)
    go func() { waitErr <- e.cmd.Wait() }()

    for {
        select {
        case <-readyCh:
            // Forwarding succeeded — clear reconnect bookkeeping.
            e.mu.Lock()
            e.attempts = 0
            e.reconnectStartedAt = time.Time{}
            e.lastReconnectReason = ""
            e.mu.Unlock()
        case err := <-waitErr:
            close(procDone)
            if ctx.Err() != nil {
                return "", false // Stop wins
            }
            if err != nil && !errors.Is(err, context.Canceled) {
                return "subprocess exited: " + err.Error(), false
            }
            return "subprocess exited cleanly", false
        }
    }
}

func (m *Manager) readLogs(e *entry, r io.Reader, watchForReady bool, readyCh chan<- struct{}) {
    scanner := bufio.NewScanner(r)
    scanner.Buffer(make([]byte, 4096), 1<<20)
    for scanner.Scan() {
        line := scanner.Text()
        e.logs.Push(line)
        if watchForReady && strings.Contains(line, "Forwarding from") {
            e.mu.Lock()
            if e.status == StatusStarting {
                e.status = StatusRunning
                e.mu.Unlock()
                m.emit(Event{ID: e.id, Status: StatusRunning, Detail: line})
                if readyCh != nil {
                    select {
                    case readyCh <- struct{}{}:
                    default:
                    }
                }
                continue
            }
            e.mu.Unlock()
        }
    }
}
```

- [ ] **Step 4: Update `TestManagerErrorPath` for the new behavior**

In `internal/portfwd/manager_test.go`, `failingCmd` now triggers reconnect; on a real clock the budget exhausts after 120s — too slow for unit tests. Rewrite `TestManagerErrorPath` to use a fake clock and advance past the budget:

```go
func TestManagerErrorPath(t *testing.T) {
    m := NewManager(8, 64)
    m.SetCmdBuilder(failingCmd)
    clk := newFakeClock(time.Now())
    m.SetClock(clk)

    id, err := m.Start(StartOpts{
        Context: "ctx", Namespace: "ns", Target: "x",
        Kind: KindPod, LocalPort: 1234, RemotePort: 1234,
    })
    if err != nil {
        t.Fatalf("Start: %v", err)
    }

    // Each iteration emits Reconnecting; advance clock so we drain
    // multiple iterations until budget is exhausted (120s).
    deadline := time.After(5 * time.Second)
    for {
        select {
        case ev := <-m.Events():
            if ev.Status == StatusErrored {
                if !strings.Contains(ev.Detail, "budget") {
                    t.Errorf("expected budget-exhausted detail, got %q", ev.Detail)
                }
                list := m.List()
                if len(list) != 1 || list[0].ID != id || list[0].Status != StatusErrored {
                    t.Errorf("List() = %+v, want one StatusErrored entry", list)
                }
                return
            }
            if ev.Status == StatusReconnecting {
                // Push clock forward past the next backoff window.
                clk.Advance(20 * time.Second)
            }
        case <-deadline:
            t.Fatalf("timed out waiting for StatusErrored")
        }
    }
}
```

Also add `"strings"` to the import block if not present.

- [ ] **Step 5: Run the new test**

Run: `go test ./internal/portfwd/ -run TestSupervisorReconnectsAfterSubprocessExit -v -timeout 30s`
Expected: PASS.

- [ ] **Step 6: Run the updated old test**

Run: `go test ./internal/portfwd/ -run TestManagerErrorPath -v -timeout 30s`
Expected: PASS.

- [ ] **Step 7: Run all portfwd tests**

Run: `go test ./internal/portfwd/ -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/portfwd/
git commit -m "portfwd: auto-reconnect on subprocess exit with 120s budget"
```

---

## Task 8: Periodic liveness check via Prober

**Files:**
- Modify: `internal/portfwd/supervisor.go` (add ticker-driven Prober.GetPod call)
- Modify: `internal/portfwd/supervisor_test.go` (add pod-gone test)

- [ ] **Step 1: Write the failing test**

Add to `internal/portfwd/supervisor_test.go`:

```go
func TestSupervisorReconnectsWhenPodGoesAway(t *testing.T) {
    m := NewManager(16, 64)

    // Builder: success on every attempt (kubectl stays running until killed).
    okBuilder := func(_ StartOpts) *exec.Cmd {
        return exec.Command("/bin/sh", "-c",
            `printf 'Forwarding from 127.0.0.1:5432 -> 5432\n' >&2; sleep 30`)
    }
    m.SetCmdBuilder(okBuilder)

    clk := newFakeClock(time.Now())
    m.SetClock(clk)

    fp := newFakeProber()
    fp.resolveResp["ns/pg-0"] = PodRef{
        Name: "pg-0", UID: "uid-1", Phase: "Running", Ready: true,
        Labels: map[string]string{"app": "pg"},
    }
    m.SetProberFactory(func(string) (Prober, error) { return fp, nil })

    _, err := m.Start(StartOpts{
        Context: "ctx", Namespace: "ns", Target: "pg-0",
        Kind: KindPod, LocalPort: 5432, RemotePort: 5432,
    })
    if err != nil {
        t.Fatalf("Start: %v", err)
    }

    waitFor(t, m.Events(), StatusRunning, 3*time.Second)

    // Now flip the prober: pod is gone.
    fp.mu.Lock()
    fp.getNotFound["ns/pg-0"] = true
    fp.mu.Unlock()

    // Make the new attempt also succeed at finding a replacement.
    fp.mu.Lock()
    fp.findResp["ns/app=pg,"] = "pg-1"
    fp.mu.Unlock()

    // Advance clock past the liveness tick (5s).
    clk.Advance(6 * time.Second)

    ev := waitFor(t, m.Events(), StatusReconnecting, 3*time.Second)
    if !strings.Contains(strings.ToLower(ev.Detail), "pod") {
        t.Errorf("Reconnecting detail = %q, want it to mention pod", ev.Detail)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/portfwd/ -run TestSupervisorReconnectsWhenPodGoesAway -v -timeout 30s`
Expected: FAIL — no liveness check yet.

- [ ] **Step 3: Add liveness ticker to `runOneAttempt`**

In `internal/portfwd/supervisor.go`, wire the ticker:

```go
const (
    reconnectBudget = 120 * time.Second
    livenessTick    = 5 * time.Second
)

// In runOneAttempt, replace the existing for-select with:
    ticker := m.clock.NewTicker(livenessTick)
    defer ticker.Stop()

    for {
        select {
        case <-readyCh:
            e.mu.Lock()
            e.attempts = 0
            e.reconnectStartedAt = time.Time{}
            e.lastReconnectReason = ""
            e.mu.Unlock()
        case <-ticker.C():
            if e.opts.Kind != KindPod {
                continue // service-forward: kubectl handles backing pod
            }
            if r := m.probeOnce(ctx, e); r != "" {
                return r, false
            }
        case err := <-waitErr:
            close(procDone)
            if ctx.Err() != nil {
                return "", false
            }
            if err != nil && !errors.Is(err, context.Canceled) {
                return "subprocess exited: " + err.Error(), false
            }
            return "subprocess exited cleanly", false
        }
    }
}

// probeOnce runs one liveness probe. Returns a non-empty reason if the
// forward should reconnect.
func (m *Manager) probeOnce(ctx context.Context, e *entry) string {
    e.mu.Lock()
    status := e.status
    e.mu.Unlock()
    if status != StatusRunning {
        return ""
    }

    prober, _ := m.getProberForEntry(e)
    if prober == nil {
        return ""
    }

    podName := e.opts.Target
    pod, err := prober.GetPod(ctx, e.opts.Namespace, podName)
    if errors.Is(err, ErrPodNotFound) {
        return "pod gone"
    }
    if err == nil {
        if pod.Phase != "Running" {
            return "pod phase " + pod.Phase
        }
        e.mu.Lock()
        expectedUID := e.podUID
        e.mu.Unlock()
        if expectedUID != "" && pod.UID != expectedUID {
            return "pod recreated (UID changed)"
        }
    }
    // Transient GetPod errors → ignored (logged to ring buffer if desired).
    return ""
}

// getProberForEntry lazily builds a Prober for the entry's context.
// Returns nil if no factory is set.
func (m *Manager) getProberForEntry(e *entry) (Prober, error) {
    m.mu.Lock()
    f := m.proberFactory
    m.mu.Unlock()
    if f == nil {
        return nil, nil
    }
    return f(e.opts.Context)
}
```

Also, when the entry's first attempt succeeds at "Forwarding from" with `KindPod` and a prober is available, populate `e.podUID` and `e.podLabels`. Do this once, when the readyCh path fires for the first time after a fresh `ResolvePod`. Hook the ResolvePod call into Start (Task 9) — for now, just leave `podUID`/`podLabels` empty if `ResolvePod` hasn't been called; the UID check guards on `expectedUID != ""`.

- [ ] **Step 4: Run the test**

Run: `go test ./internal/portfwd/ -run TestSupervisorReconnectsWhenPodGoesAway -v -timeout 30s`
Expected: PASS.

- [ ] **Step 5: Run all portfwd tests**

Run: `go test ./internal/portfwd/ -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/portfwd/
git commit -m "portfwd: liveness check via Prober.GetPod every 5s"
```

---

## Task 9: ResolvePod on Start + label-based re-resolution

**Files:**
- Modify: `internal/portfwd/manager.go` (Start calls ResolvePod)
- Modify: `internal/portfwd/supervisor.go` (`resolveTarget`)
- Modify: `internal/portfwd/supervisor_test.go` (re-resolution test)

- [ ] **Step 1: Write the failing test**

Add to `supervisor_test.go`:

```go
func TestSupervisorReResolvesPodByLabels(t *testing.T) {
    m := NewManager(16, 64)

    // Builder: succeeds on every attempt. We assert the Target the
    // second attempt receives matches the re-resolved pod.
    var calls []string
    builder := func(opts StartOpts) *exec.Cmd {
        calls = append(calls, opts.Target)
        return exec.Command("/bin/sh", "-c",
            `printf 'Forwarding from 127.0.0.1:5432 -> 5432\n' >&2; sleep 30`)
    }
    m.SetCmdBuilder(builder)

    clk := newFakeClock(time.Now())
    m.SetClock(clk)

    fp := newFakeProber()
    fp.resolveResp["ns/pg-0"] = PodRef{
        Name: "pg-0", UID: "uid-1", Phase: "Running", Ready: true,
        Labels: map[string]string{"app": "pg"},
    }
    fp.getNotFound["ns/pg-0"] = true // pod gone on the very first liveness tick
    fp.findResp["ns/app=pg,"] = "pg-1"
    m.SetProberFactory(func(string) (Prober, error) { return fp, nil })

    _, err := m.Start(StartOpts{
        Context: "ctx", Namespace: "ns", Target: "pg-0",
        Kind: KindPod, LocalPort: 5432, RemotePort: 5432,
    })
    if err != nil {
        t.Fatalf("Start: %v", err)
    }

    waitFor(t, m.Events(), StatusRunning, 3*time.Second)
    clk.Advance(6 * time.Second) // trigger liveness tick → pod gone
    waitFor(t, m.Events(), StatusReconnecting, 3*time.Second)
    clk.Advance(2 * time.Second) // past first-attempt backoff (1s)
    waitFor(t, m.Events(), StatusRunning, 3*time.Second)

    if len(calls) < 2 {
        t.Fatalf("builder called %d times, want >= 2", len(calls))
    }
    if calls[0] != "pg-0" {
        t.Errorf("first call target = %q, want pg-0", calls[0])
    }
    if calls[1] != "pg-1" {
        t.Errorf("second call target = %q, want pg-1 (re-resolved)", calls[1])
    }
}
```

- [ ] **Step 2: Run the test to confirm failure**

Run: `go test ./internal/portfwd/ -run TestSupervisorReResolvesPodByLabels -v -timeout 30s`
Expected: FAIL — supervisor currently always rebuilds with the original Target.

- [ ] **Step 3: Hook `ResolvePod` into `Start`**

In `internal/portfwd/manager.go`, after the conflict check and before launching `supervise`, call ResolvePod for KindPod when a factory is set:

```go
// Existing: build entry e, register in m.entries, unlock.
// New: best-effort resolution. Failure here aborts Start.
if opts.Kind == KindPod {
    if prober, err := m.getProberForEntry(e); err != nil {
        m.mu.Lock(); delete(m.entries, id); m.mu.Unlock()
        cancel()
        return "", fmt.Errorf("portfwd: prober factory: %w", err)
    } else if prober != nil {
        ref, perr := prober.ResolvePod(context.Background(), opts.Namespace, opts.Target)
        if perr != nil {
            m.mu.Lock(); delete(m.entries, id); m.mu.Unlock()
            cancel()
            return "", fmt.Errorf("portfwd: resolve pod: %w", perr)
        }
        e.mu.Lock()
        e.podUID = ref.UID
        e.podLabels = ref.Labels
        e.mu.Unlock()
    }
}

go m.supervise(ctx, e)
return id, nil
```

- [ ] **Step 4: Add `resolveTarget` to `supervisor.go`**

Add this helper and call it at the top of the `for` loop in `supervise` (replacing the use of `e.opts.Target` in subsequent builder calls):

```go
// resolveTarget returns the pod/service name kubectl should target this
// iteration. For KindService it is always opts.Target (kubectl picks a
// fresh backing pod itself). For KindPod, the first iteration uses
// opts.Target; subsequent iterations re-find a Ready pod by labels.
func (m *Manager) resolveTarget(ctx context.Context, e *entry) (string, error) {
    if e.opts.Kind == KindService {
        return e.opts.Target, nil
    }
    e.mu.Lock()
    firstAttempt := e.attempts == 0
    labels := e.podLabels
    origTarget := e.opts.Target
    e.mu.Unlock()
    if firstAttempt || len(labels) == 0 {
        return origTarget, nil
    }
    prober, err := m.getProberForEntry(e)
    if err != nil || prober == nil {
        return origTarget, nil
    }
    name, err := prober.FindReadyPodByLabels(ctx, e.opts.Namespace, labels)
    if err != nil {
        return "", err
    }
    // Cache the new name for liveness probing in this attempt.
    e.mu.Lock()
    e.opts.Target = name
    e.mu.Unlock()
    return name, nil
}
```

In `supervise`, at the top of the loop body, call:

```go
target, err := m.resolveTarget(ctx, e)
if err != nil {
    // Treat as a reconnect-triggering reason — fall through to the
    // failure-bookkeeping block instead of running the subprocess.
    reason := "resolve target: " + err.Error()
    // (Use the same failure-recording code path as a subprocess exit.)
    ...
}
// Use `target` when building the kubectl command. Easiest: replace
// e.cmd = m.builder(e.opts) with a temp copy of opts:
opts := e.opts
opts.Target = target
e.cmd = m.builder(opts)
```

Refactor the failure-bookkeeping block out of the inline `if` after `runOneAttempt`, into a small helper, so resolve-failure and runOneAttempt-failure share it:

```go
func (m *Manager) recordFailure(ctx context.Context, e *entry, reason string) (stop bool) {
    if ctx.Err() != nil {
        return true
    }
    e.mu.Lock()
    if e.reconnectStartedAt.IsZero() {
        e.reconnectStartedAt = m.clock.Now()
    }
    e.attempts++
    e.lastReconnectReason = reason
    elapsed := m.clock.Now().Sub(e.reconnectStartedAt)
    attempts := e.attempts
    e.status = StatusReconnecting
    e.mu.Unlock()
    m.emit(Event{ID: e.id, Status: StatusReconnecting, Detail: reason})
    if elapsed > reconnectBudget {
        e.mu.Lock()
        e.status = StatusErrored
        e.lastErr = "reconnect budget exhausted: " + reason
        e.mu.Unlock()
        m.emit(Event{ID: e.id, Status: StatusErrored, Detail: "reconnect budget exhausted: " + reason})
        return true
    }
    wait := backoffSchedule[len(backoffSchedule)-1]
    if attempts-1 < len(backoffSchedule) {
        wait = backoffSchedule[attempts-1]
    }
    select {
    case <-m.clock.After(wait):
        return false
    case <-ctx.Done():
        return true
    }
}
```

Update the supervise loop to call `recordFailure` from both the resolve-error path and the post-`runOneAttempt`-failure path.

Move `runOneAttempt`'s builder call to use the temp-opts copy:

```go
func (m *Manager) runOneAttempt(ctx context.Context, e *entry, target string) (reason string, exitedClean bool) {
    opts := e.opts
    opts.Target = target
    e.cmd = m.builder(opts)
    // ... rest unchanged
}
```

And in supervise:

```go
reason, exitedClean := m.runOneAttempt(ctx, e, target)
```

- [ ] **Step 5: Run the new test**

Run: `go test ./internal/portfwd/ -run TestSupervisorReResolvesPodByLabels -v -timeout 30s`
Expected: PASS.

- [ ] **Step 6: Run all portfwd tests**

Run: `go test ./internal/portfwd/ -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/portfwd/
git commit -m "portfwd: re-resolve pod by labels after reconnect"
```

---

## Task 10: TCP probe with 3-failure debounce

**Files:**
- Modify: `internal/portfwd/supervisor.go` (TCP probe in liveness tick)
- Modify: `internal/portfwd/supervisor_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSupervisorTCPProbeDebouncesSingleFailure(t *testing.T) {
    m := NewManager(16, 64)

    // Reserve a port the kubectl-fake will listen on; we close it to
    // simulate stall, but only briefly.
    lis, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatalf("listen: %v", err)
    }
    port := lis.Addr().(*net.TCPAddr).Port

    builder := func(_ StartOpts) *exec.Cmd {
        // Builder forks a real listener so the TCP probe can connect.
        // We mimic kubectl by holding the listener open.
        return exec.Command("/bin/sh", "-c",
            `printf 'Forwarding from 127.0.0.1:`+strconv.Itoa(port)+` -> 5432\n' >&2; sleep 30`)
    }
    m.SetCmdBuilder(builder)
    _ = lis // keep until end of test
    defer lis.Close()

    clk := newFakeClock(time.Now())
    m.SetClock(clk)

    id, err := m.Start(StartOpts{
        Context: "ctx", Namespace: "ns", Target: "pg-0",
        Kind: KindPod, LocalPort: port, RemotePort: 5432,
    })
    if err != nil {
        t.Fatalf("Start: %v", err)
    }
    waitFor(t, m.Events(), StatusRunning, 3*time.Second)

    // One missed probe (close listener) shouldn't trigger reconnect.
    lis.Close()
    clk.Advance(6 * time.Second)
    select {
    case ev := <-m.Events():
        if ev.Status == StatusReconnecting {
            t.Fatalf("got reconnect after a single TCP failure, want debounce")
        }
    case <-time.After(500 * time.Millisecond):
        // good — no reconnect yet
    }

    _ = m.Stop(id)
    _ = id
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/portfwd/ -run TestSupervisorTCPProbeDebouncesSingleFailure -v -timeout 30s`
Expected: Currently passes because no TCP probe exists yet. To make it fail-then-pass meaningfully, this task also adds a second test that requires 3 failures to reconnect — write that one too:

```go
func TestSupervisorTCPProbeReconnectsAfterThreeFailures(t *testing.T) {
    m := NewManager(16, 64)

    // Builder uses a port that is never bound — every TCP probe fails.
    port := 1 // privileged, almost certainly closed
    builder := func(_ StartOpts) *exec.Cmd {
        return exec.Command("/bin/sh", "-c",
            `printf 'Forwarding from 127.0.0.1:`+strconv.Itoa(port)+` -> 5432\n' >&2; sleep 30`)
    }
    m.SetCmdBuilder(builder)

    clk := newFakeClock(time.Now())
    m.SetClock(clk)

    // No prober factory → GetPod check is skipped; only TCP probe fires.
    _, err := m.Start(StartOpts{
        Context: "ctx", Namespace: "ns", Target: "pg-0",
        Kind: KindPod, LocalPort: port, RemotePort: 5432,
    })
    if err != nil {
        t.Fatalf("Start: %v", err)
    }
    waitFor(t, m.Events(), StatusRunning, 3*time.Second)

    // Three liveness ticks (5s each) → three TCP failures → reconnect.
    clk.Advance(6 * time.Second)
    clk.Advance(6 * time.Second)
    clk.Advance(6 * time.Second)

    ev := waitFor(t, m.Events(), StatusReconnecting, 3*time.Second)
    if !strings.Contains(strings.ToLower(ev.Detail), "tcp") {
        t.Errorf("reconnect reason = %q, want it to mention tcp", ev.Detail)
    }
}
```

This second test is what enforces the probe. Run it; expect FAIL.

- [ ] **Step 3: Add TCP probe and counter to `probeOnce`**

In supervisor.go, extend the entry:

```go
type entry struct {
    // ...existing fields...
    tcpFailStreak int
}
```

Update `probeOnce`:

```go
const (
    tcpProbeTimeout    = 500 * time.Millisecond
    tcpProbeFailThresh = 3
)

func (m *Manager) probeOnce(ctx context.Context, e *entry) string {
    e.mu.Lock()
    status := e.status
    e.mu.Unlock()
    if status != StatusRunning {
        return ""
    }

    // Pod liveness via prober.
    prober, _ := m.getProberForEntry(e)
    if prober != nil && e.opts.Kind == KindPod {
        pod, err := prober.GetPod(ctx, e.opts.Namespace, e.opts.Target)
        if errors.Is(err, ErrPodNotFound) {
            return "pod gone"
        }
        if err == nil {
            if pod.Phase != "Running" {
                return "pod phase " + pod.Phase
            }
            e.mu.Lock()
            expectedUID := e.podUID
            e.mu.Unlock()
            if expectedUID != "" && pod.UID != expectedUID {
                return "pod recreated (UID changed)"
            }
        }
    }

    // TCP probe localhost:LocalPort with debounce.
    addr := fmt.Sprintf("127.0.0.1:%d", e.opts.LocalPort)
    conn, err := net.DialTimeout("tcp", addr, tcpProbeTimeout)
    if err != nil {
        e.mu.Lock()
        e.tcpFailStreak++
        streak := e.tcpFailStreak
        e.mu.Unlock()
        if streak >= tcpProbeFailThresh {
            return fmt.Sprintf("tcp probe failed %d times", streak)
        }
    } else {
        _ = conn.Close()
        e.mu.Lock()
        e.tcpFailStreak = 0
        e.mu.Unlock()
    }
    return ""
}
```

Add imports: `"fmt"`, `"net"` to supervisor.go.

Also reset `tcpFailStreak` to 0 when `runOneAttempt` enters a new attempt and when readyCh fires.

- [ ] **Step 4: Run both TCP tests**

Run: `go test ./internal/portfwd/ -run TestSupervisorTCPProbe -v -timeout 30s`
Expected: PASS.

- [ ] **Step 5: Run all portfwd tests**

Run: `go test ./internal/portfwd/ -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/portfwd/
git commit -m "portfwd: TCP probe with 3-fail debounce on liveness tick"
```

---

## Task 11: User Stop wins during backoff

**Files:**
- Modify: `internal/portfwd/supervisor_test.go`

This test asserts behavior that should already work (`recordFailure`'s select on `ctx.Done()`). It exists to lock in the contract.

- [ ] **Step 1: Add the test**

```go
func TestSupervisorStopDuringBackoffExits(t *testing.T) {
    m := NewManager(16, 64)
    m.SetCmdBuilder(failingCmd) // always exits non-zero

    clk := newFakeClock(time.Now())
    m.SetClock(clk)

    id, err := m.Start(StartOpts{
        Context: "ctx", Namespace: "ns", Target: "x",
        Kind: KindPod, LocalPort: 1234, RemotePort: 1234,
    })
    if err != nil {
        t.Fatalf("Start: %v", err)
    }
    waitFor(t, m.Events(), StatusReconnecting, 3*time.Second)

    // Don't advance the clock. Call Stop while we're in backoff.
    if err := m.Stop(id); err != nil {
        t.Fatalf("Stop: %v", err)
    }
    waitFor(t, m.Events(), StatusStopped, 3*time.Second)

    list := m.List()
    if len(list) != 1 || list[0].Status != StatusStopped {
        t.Errorf("after Stop during backoff: list=%+v, want Stopped", list)
    }
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/portfwd/ -run TestSupervisorStopDuringBackoffExits -v -timeout 30s`
Expected: PASS (no production change required).

- [ ] **Step 3: Commit**

```bash
git add internal/portfwd/supervisor_test.go
git commit -m "portfwd: lock in Stop-during-backoff contract via test"
```

---

## Task 12: Implement `kube.Client` as `portfwd.Prober`

**Files:**
- Create: `internal/kube/prober.go`
- Create: `internal/kube/prober_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/kube/prober_test.go`:

```go
package kube

import (
    "context"
    "errors"
    "testing"

    "github.com/billygate/kap-toolsbox/internal/portfwd"
    appsv1 "k8s.io/api/apps/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
)

func newProberWithObjects(objs ...runtime.Object) *Client {
    c := newTestClient(objs...)
    return c
}

func TestResolvePodUsesOwnerSelectorLabels(t *testing.T) {
    rs := &appsv1.ReplicaSet{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "demo-rs",
            Namespace: "ns",
            UID:       "rs-uid",
        },
        Spec: appsv1.ReplicaSetSpec{
            Selector: &metav1.LabelSelector{
                MatchLabels: map[string]string{"app": "demo"},
            },
        },
    }
    pod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "demo-pod",
            Namespace: "ns",
            UID:       "pod-uid",
            Labels: map[string]string{
                "app":               "demo",
                "pod-template-hash": "abc123",
            },
            OwnerReferences: []metav1.OwnerReference{
                {Kind: "ReplicaSet", Name: "demo-rs", UID: types.UID("rs-uid"), Controller: ptrBool(true)},
            },
        },
        Status: corev1.PodStatus{Phase: corev1.PodRunning},
    }

    c := newProberWithObjects(pod, rs)
    ref, err := c.ResolvePod(context.Background(), "ns", "demo-pod")
    if err != nil {
        t.Fatalf("ResolvePod: %v", err)
    }
    if ref.Labels["app"] != "demo" {
        t.Errorf("missing app=demo: %+v", ref.Labels)
    }
    if _, ok := ref.Labels["pod-template-hash"]; ok {
        t.Errorf("pod-template-hash should be stripped: %+v", ref.Labels)
    }
    if ref.UID != "pod-uid" {
        t.Errorf("UID = %q, want pod-uid", ref.UID)
    }
}

func TestGetPodReturnsErrPodNotFound(t *testing.T) {
    c := newProberWithObjects()
    _, err := c.GetPod(context.Background(), "ns", "nope")
    if !errors.Is(err, portfwd.ErrPodNotFound) {
        t.Errorf("err = %v, want ErrPodNotFound", err)
    }
}

func TestFindReadyPodByLabelsPicksFirstReady(t *testing.T) {
    notReady := makePod("pg-0", "ns", map[string]string{"app": "pg"}, corev1.PodRunning, false)
    ready := makePod("pg-1", "ns", map[string]string{"app": "pg"}, corev1.PodRunning, true)
    c := newProberWithObjects(notReady, ready)
    name, err := c.FindReadyPodByLabels(context.Background(), "ns", map[string]string{"app": "pg"})
    if err != nil {
        t.Fatalf("FindReadyPodByLabels: %v", err)
    }
    if name != "pg-1" {
        t.Errorf("name = %q, want pg-1", name)
    }
}

func ptrBool(b bool) *bool { return &b }

func makePod(name, ns string, labels map[string]string, phase corev1.PodPhase, ready bool) *corev1.Pod {
    conds := []corev1.PodCondition{}
    if ready {
        conds = append(conds, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
    }
    return &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      name,
            Namespace: ns,
            Labels:    labels,
        },
        Status: corev1.PodStatus{
            Phase:      phase,
            Conditions: conds,
        },
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/kube/ -run "TestResolvePod|TestGetPod|TestFindReadyPod" -v -timeout 30s`
Expected: FAIL — methods don't exist yet.

- [ ] **Step 3: Implement `internal/kube/prober.go`**

```go
package kube

import (
    "context"
    "errors"
    "sort"

    "github.com/billygate/kap-toolsbox/internal/portfwd"
    appsv1 "k8s.io/api/apps/v1"
    corev1 "k8s.io/api/core/v1"
    apierrors "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var labelBlacklist = map[string]bool{
    "pod-template-hash":                        true,
    "controller-revision-hash":                 true,
    "statefulset.kubernetes.io/pod-name":       true,
}

// ResolvePod looks up the pod and returns selectable labels (preferring
// the owner's MatchLabels when available) + UID.
func (c *Client) ResolvePod(ctx context.Context, namespace, name string) (portfwd.PodRef, error) {
    pod, err := c.k8s.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
    if err != nil {
        if apierrors.IsNotFound(err) {
            return portfwd.PodRef{}, portfwd.ErrPodNotFound
        }
        return portfwd.PodRef{}, err
    }

    labels := c.ownerSelectorLabels(ctx, namespace, pod)
    if labels == nil {
        labels = filterBlacklist(pod.Labels)
    }

    return portfwd.PodRef{
        Name:   pod.Name,
        UID:    string(pod.UID),
        Phase:  string(pod.Status.Phase),
        Ready:  podReady(pod),
        Labels: labels,
    }, nil
}

// GetPod returns current state without owner-label lookup (faster).
func (c *Client) GetPod(ctx context.Context, namespace, name string) (portfwd.PodRef, error) {
    pod, err := c.k8s.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
    if err != nil {
        if apierrors.IsNotFound(err) {
            return portfwd.PodRef{}, portfwd.ErrPodNotFound
        }
        return portfwd.PodRef{}, err
    }
    return portfwd.PodRef{
        Name:  pod.Name,
        UID:   string(pod.UID),
        Phase: string(pod.Status.Phase),
        Ready: podReady(pod),
    }, nil
}

// FindReadyPodByLabels lists pods matching labels, sorted by name, returns
// the first Ready one. ErrPodNotFound when nothing matches Ready.
func (c *Client) FindReadyPodByLabels(ctx context.Context, namespace string, labels map[string]string) (string, error) {
    selector := metav1.LabelSelector{MatchLabels: labels}
    sel, err := metav1.LabelSelectorAsSelector(&selector)
    if err != nil {
        return "", err
    }
    list, err := c.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
    if err != nil {
        return "", err
    }
    names := make([]string, 0, len(list.Items))
    byName := map[string]corev1.Pod{}
    for _, p := range list.Items {
        names = append(names, p.Name)
        byName[p.Name] = p
    }
    sort.Strings(names)
    for _, n := range names {
        p := byName[n]
        if p.Status.Phase == corev1.PodRunning && podReady(&p) {
            return n, nil
        }
    }
    return "", portfwd.ErrPodNotFound
}

func (c *Client) ownerSelectorLabels(ctx context.Context, namespace string, pod *corev1.Pod) map[string]string {
    for _, ref := range pod.OwnerReferences {
        if ref.Controller == nil || !*ref.Controller {
            continue
        }
        switch ref.Kind {
        case "ReplicaSet":
            rs, err := c.k8s.AppsV1().ReplicaSets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
            if err != nil || rs.Spec.Selector == nil {
                continue
            }
            return cloneStringMap(rs.Spec.Selector.MatchLabels)
        case "StatefulSet":
            ss, err := c.k8s.AppsV1().StatefulSets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
            if err != nil || ss.Spec.Selector == nil {
                continue
            }
            return cloneStringMap(ss.Spec.Selector.MatchLabels)
        case "DaemonSet":
            ds, err := c.k8s.AppsV1().DaemonSets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
            if err != nil || ds.Spec.Selector == nil {
                continue
            }
            return cloneStringMap(ds.Spec.Selector.MatchLabels)
        }
    }
    _ = appsv1.SchemeGroupVersion // keep import in case of build pruning
    _ = errors.New
    return nil
}

func filterBlacklist(in map[string]string) map[string]string {
    out := map[string]string{}
    for k, v := range in {
        if labelBlacklist[k] {
            continue
        }
        out[k] = v
    }
    return out
}

func cloneStringMap(m map[string]string) map[string]string {
    out := make(map[string]string, len(m))
    for k, v := range m {
        out[k] = v
    }
    return out
}

func podReady(p *corev1.Pod) bool {
    for _, c := range p.Status.Conditions {
        if c.Type == corev1.PodReady {
            return c.Status == corev1.ConditionTrue
        }
    }
    return false
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/kube/ -count=1 -timeout 30s`
Expected: PASS.

- [ ] **Step 5: Verify `*kube.Client` satisfies `portfwd.Prober`**

Add a compile-time check at the bottom of `internal/kube/prober.go`:

```go
var _ portfwd.Prober = (*Client)(nil)
```

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add internal/kube/
git commit -m "kube: implement portfwd.Prober (ResolvePod, GetPod, FindReadyPodByLabels)"
```

---

## Task 13: Wire `ProberFactory` from app.go

**Files:**
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Inject prober factory**

In `internal/tui/app.go`, find the `mgr := portfwd.NewManager(0, 0)` line and follow it with:

```go
mgr := portfwd.NewManager(0, 0)
mgr.SetProberFactory(func(contextName string) (portfwd.Prober, error) {
    return kube.NewClient(contextName)
})
```

Add `kube` to the imports if missing:

```go
import (
    // ...
    "github.com/billygate/kap-toolsbox/internal/kube"
    "github.com/billygate/kap-toolsbox/internal/portfwd"
)
```

- [ ] **Step 2: Build + run existing app-level test**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS.

- [ ] **Step 3: Build the full binary**

Run: `make build`
Expected: `bin/kapctl` built successfully.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app.go
git commit -m "tui: wire portfwd Prober factory to kube.Client"
```

---

## Task 14: FORWARDS pane — Reconnecting status display + toast policy

**Files:**
- Modify: `internal/tui/panes/forwards.go`
- Modify: `internal/tui/panes/panes_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/panes/panes_test.go`:

```go
func TestForwardsRendersReconnectingWithCountdown(t *testing.T) {
    s := styles.New(catppuccin.New()) // adjust import if different
    mgr := portfwd.NewManager(8, 64)
    f := panes.NewForwards(mgr, s)
    f.SetSize(80, 20)

    now := time.Now()
    // Fake snapshot: pane reads via mgr.List(), so insert via fakeCmdBuilder?
    // Simpler: just test the cell formatting helper directly.
    snap := portfwd.Snapshot{
        ID: "1", LocalPort: 5432, Target: "pg-0", Namespace: "ns",
        Kind: portfwd.KindPod, Status: portfwd.StatusReconnecting,
        StartedAt: now.Add(-30 * time.Second),
        Attempts: 2, ReconnectStartedAt: now.Add(-30 * time.Second),
    }
    row := panes.NewFwdRowForTest(snap, now).Cells()
    if row[3] != "reconnecting 2/90s" {
        t.Errorf("status cell = %q, want %q", row[3], "reconnecting 2/90s")
    }
}
```

(This requires exposing a test constructor — `NewFwdRowForTest(snap, now)`. Add it.)

- [ ] **Step 2: Update the Cells() formatter**

In `internal/tui/panes/forwards.go`, change `fwdRow` to compute reconnect-aware status text. Inject a clock function so tests can fix "now":

```go
type fwdRow struct {
    portfwd.Snapshot
    now time.Time // captured at refresh time; tests inject via NewFwdRowForTest
}

func (f fwdRow) Cells() table.Row {
    target := f.Kind.Prefix() + f.Target
    age := f.now.Sub(f.StartedAt).Round(time.Second).String()
    status := f.Status.String()
    if f.Status == portfwd.StatusReconnecting && !f.ReconnectStartedAt.IsZero() {
        remaining := (120 * time.Second) - f.now.Sub(f.ReconnectStartedAt)
        if remaining < 0 {
            remaining = 0
        }
        status = fmt.Sprintf("reconnecting %d/%ds", f.Attempts, int(remaining.Seconds()))
    }
    return table.Row{
        fmt.Sprintf("%d", f.LocalPort),
        target,
        f.Namespace,
        status,
        age,
    }
}
```

Update `refresh()` to pass `time.Now()`:

```go
func (f *Forwards) refresh() {
    if f.mgr == nil {
        return
    }
    now := time.Now()
    snaps := f.mgr.List()
    items := make([]core.RowProvider, 0, len(snaps))
    for _, s := range snaps {
        items = append(items, fwdRow{Snapshot: s, now: now})
    }
    f.tbl.SetItems(items)
}

// NewFwdRowForTest is exported only for tests.
func NewFwdRowForTest(s portfwd.Snapshot, now time.Time) fwdRow {
    return fwdRow{Snapshot: s, now: now}
}
```

- [ ] **Step 3: Run the new test**

Run: `go test ./internal/tui/panes/ -run TestForwardsRendersReconnecting -v`
Expected: PASS.

- [ ] **Step 4: Update toast policy**

In `forwards.go`, replace `eventToToast`:

```go
func (f *Forwards) eventToToast(ev portfwd.Event) tea.Cmd {
    // Look up current entry state to decide first-of-series vs. spam.
    var snap portfwd.Snapshot
    for _, s := range f.mgr.List() {
        if s.ID == ev.ID {
            snap = s
            break
        }
    }

    var kind overlays.ToastKind
    var text string
    switch ev.Status {
    case portfwd.StatusRunning:
        if snap.Attempts == 0 && !snap.ReconnectStartedAt.IsZero() {
            // Just-cleared reconnect: pane bookkeeping zeroes Attempts on
            // success, so detect via the prior series via heuristic — too brittle.
            // Use a simpler rule: any Running with a non-empty LastReconnectReason
            // recorded is a reconnect-success toast.
        }
        if snap.LastReconnectReason != "" {
            kind = overlays.ToastSuccess
            text = "port-forward " + ev.ID + " reconnected"
        } else {
            kind = overlays.ToastSuccess
            text = "port-forward running (" + ev.ID + ")"
        }
    case portfwd.StatusReconnecting:
        if snap.Attempts > 1 {
            return nil // silent: only first-of-series toasts
        }
        kind = overlays.ToastInfo
        text = "port-forward " + ev.ID + " reconnecting: " + ev.Detail
    case portfwd.StatusErrored:
        kind = overlays.ToastError
        if ev.Detail != "" {
            text = "port-forward " + ev.ID + " errored: " + ev.Detail
        } else {
            text = "port-forward " + ev.ID + " errored"
        }
    case portfwd.StatusStopped:
        kind = overlays.ToastInfo
        text = "port-forward stopped (" + ev.ID + ")"
    default:
        return nil
    }
    return func() tea.Msg { return overlays.ToastMsg{Kind: kind, Text: text} }
}
```

Note: after a successful reconnect, `LastReconnectReason` from the prior series is still set. We need to clear it on the readyCh path in supervisor.go (Task 7 already cleared it). Double-check by reading that block. If it doesn't clear, add `e.lastReconnectReason = ""` next to the existing `e.attempts = 0` reset — but **don't clear it BEFORE emitting the Running event** that triggers the reconnected toast. The correct order is: emit Running first, then clear bookkeeping on next failure. Adjust supervisor.go if needed: defer the reconnect-bookkeeping reset to **after** Running emit by moving the clear into runOneAttempt's readyCh handler, **after** the e.status = StatusRunning emit.

Re-read supervisor.go's readyCh case from Task 7 — order should be:

```go
case <-readyCh:
    // status flip and Running emit happened inside readLogs already.
    e.mu.Lock()
    e.attempts = 0
    e.reconnectStartedAt = time.Time{}
    // Keep lastReconnectReason until next failure — toast policy reads it.
    e.mu.Unlock()
```

That preserves `LastReconnectReason` through the success-toast emission. Clear it at the **next** failure inside `recordFailure` instead, before overwriting with the new reason — which is already what `recordFailure` does (`e.lastReconnectReason = reason`).

- [ ] **Step 5: Run all TUI tests**

Run: `go test ./internal/tui/... -count=1`
Expected: PASS.

- [ ] **Step 6: Run full suite**

Run: `make test && make lint`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/panes/ internal/portfwd/
git commit -m "tui: render reconnecting status with countdown; reconnect toasts"
```

---

## Task 15: Manual verification

**Files:** none (manual smoke test)

- [ ] **Step 1: Build and run**

```bash
make build
./bin/kapctl
```

- [ ] **Step 2: Start a port-forward via EXPLORER on any Deployment pod.**

Switch to FORWARDS tab. Verify the row shows `running`.

- [ ] **Step 3: Trigger pod restart externally**

In a second terminal:

```bash
kubectl --context <same-ctx> -n <same-ns> delete pod <pod-name>
```

Within ~5s the row should flip to `reconnecting 1/115s`, then within ~5–15s back to `running`. A reconnect-info toast and a reconnect-success toast should appear.

- [ ] **Step 4: Test budget exhaustion**

Delete the entire Deployment (so no replacement pod comes up):

```bash
kubectl --context <ctx> -n <ns> delete deployment <deploy>
```

After ~120s the row should flip to `errored` with `reconnect budget exhausted` toast.

- [ ] **Step 5: Test explicit Stop during reconnecting**

Re-create the deployment. Start a forward. Delete the pod. While the row reads `reconnecting`, press `d` on it. Status flips to `stopped` and stays there — no further attempts.

- [ ] **Step 6: Final commit (if anything tweaked)**

```bash
git status
# If clean, no commit needed.
```

---

## Self-review

- [x] **Spec coverage:**
  - Reconnect policy (always, cancel via `d`) → Task 7 (reconnect loop) + Task 11 (Stop-during-backoff).
  - Pod re-resolution by labels → Task 9 (re-resolution) + Task 12 (kube impl).
  - k8s API liveness check → Task 8.
  - TCP probe with 3-fail debounce → Task 10.
  - 120s budget reset on success → Task 7 (reset cleared on readyCh).
  - `StatusReconnecting` + Snapshot fields → Task 1.
  - Prober interface → Task 2.
  - Clock abstraction → Task 3.
  - UI: status display with attempt/countdown → Task 14.
  - UI: toast policy (first-of-series only) → Task 14.
  - app.go wiring → Task 13.
- [x] **Placeholder scan:** No TBD/TODO/"similar to" left. Every step has either runnable commands or full code blocks.
- [x] **Type consistency:**
  - `Status.String()` returns `"reconnecting"` (Task 1) — matches the format string in Task 14 (`"reconnecting %d/%ds"` uses literal, not `Status.String()`, so no drift).
  - `Snapshot.Attempts`, `LastReconnectReason`, `ReconnectStartedAt` declared in Task 1, referenced in Tasks 7/9/14 consistently.
  - `Prober` methods `ResolvePod`, `GetPod`, `FindReadyPodByLabels` declared in Task 2 and implemented in Task 12 with matching signatures.
  - `ErrPodNotFound` introduced in Task 2, returned by Task 12, checked by Task 8.
  - `entry.podUID`/`podLabels`/`tcpFailStreak` added incrementally; Task 1 declares the reconnect fields, Task 9 adds `podUID`/`podLabels`, Task 10 adds `tcpFailStreak` — each task adds only what it needs to that task's struct edit. Final shape is consistent.

Plan complete.
