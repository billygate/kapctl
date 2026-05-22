// Package portfwd manages background `kubectl port-forward` processes
// so the TUI can launch forwards, monitor their state, and shut them
// down on exit without ever blocking the Bubble Tea event loop.
package portfwd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Status describes the lifecycle of an Entry.
type Status int

// Status values.
const (
	StatusStarting Status = iota
	StatusRunning
	StatusReconnecting
	StatusErrored
	StatusStopped
)

// String renders the status as a short label suitable for display.
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

// Kind selects whether the forward targets a pod or a service.
type Kind int

// Kind values.
const (
	KindPod Kind = iota
	KindService
)

// Prefix is the kubectl-friendly target prefix ("pod/" or "service/").
func (k Kind) Prefix() string {
	if k == KindService {
		return "service/"
	}
	return "pod/"
}

// StartOpts is the input to Manager.Start.
type StartOpts struct {
	Context    string
	Namespace  string
	Target     string // bare name, no "pod/" prefix
	Kind       Kind
	LocalPort  int
	RemotePort int
}

// Snapshot is the value-typed view of an Entry returned by List() —
// safe to copy across goroutines.
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

// Event is published on the Manager.Events() channel whenever an
// Entry's status changes.
type Event struct {
	ID     string
	Status Status
	Detail string // optional: error text or "Forwarding from..." line
}

// CmdBuilder lets tests substitute a fake binary for kubectl. The
// returned *exec.Cmd is wired up by Manager.Start (stderr/stdout
// pipes, process group); callers should not set those.
type CmdBuilder func(opts StartOpts) *exec.Cmd

// DefaultCmdBuilder constructs the real `kubectl port-forward` command.
func DefaultCmdBuilder(opts StartOpts) *exec.Cmd {
	target := opts.Kind.Prefix() + opts.Target
	return exec.Command(
		"kubectl",
		"--context", opts.Context,
		"-n", opts.Namespace,
		"port-forward",
		target,
		fmt.Sprintf("%d:%d", opts.LocalPort, opts.RemotePort),
	)
}

// Manager owns the registry of running forwards. Methods are safe to
// call from any goroutine.
type Manager struct {
	mu       sync.Mutex
	entries  map[string]*entry
	nextID   atomic.Uint64
	events   chan Event
	builder  CmdBuilder
	logCap   int // ring buffer size per entry
	stopping bool

	proberFactory ProberFactory // nil → no pod re-resolution; KindPod uses original Target each attempt
	clock         Clock         // nil → realClock
}

// NewManager builds a Manager. eventsCap sizes the events channel
// (recommend ≥ 16 to absorb bursts). logCap caps per-entry log lines.
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

// SetCmdBuilder swaps the command builder. Used by tests.
func (m *Manager) SetCmdBuilder(b CmdBuilder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builder = b
}

// SetProberFactory installs the factory used to build Probers for new
// entries. When nil (the default), KindPod entries do not re-resolve
// the target — they keep retrying the original pod name.
func (m *Manager) SetProberFactory(f ProberFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proberFactory = f
}

// SetClock overrides the time source. Used by tests. Passing nil
// restores the default realClock.
func (m *Manager) SetClock(c Clock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c == nil {
		c = realClock{}
	}
	m.clock = c
}

// Events returns the channel of status transitions. Closed by Close().
func (m *Manager) Events() <-chan Event { return m.events }

// entry is the manager-private wrapper around a running cmd.
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

// Start launches a new port-forward. It returns immediately with the
// new entry's ID; the actual kubectl command runs in a goroutine and
// state transitions arrive via Events().
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
	// Conflict check: refuse to overlap on the same local port — the
	// existing entry would silently be the only one to actually own it.
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
	e.cmd = m.builder(opts)
	m.entries[id] = e
	m.mu.Unlock()

	if err := m.startProcess(ctx, e); err != nil {
		m.mu.Lock()
		delete(m.entries, id)
		m.mu.Unlock()
		cancel()
		return "", err
	}
	return id, nil
}

// statusRead is a small convenience for the conflict check that
// avoids re-locking.
func (e *entry) statusRead() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (m *Manager) startProcess(ctx context.Context, e *entry) error {
	stderr, err := e.cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdout, err := e.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := e.cmd.Start(); err != nil {
		return err
	}

	// Cancellation: when ctx is cancelled (Stop or StopAll), send
	// SIGTERM; if the child hasn't exited within 2s, escalate to SIGKILL.
	go func() {
		<-ctx.Done()
		if p := e.cmd.Process; p != nil {
			_ = p.Signal(syscall.SIGTERM)
			select {
			case <-e.doneCh:
			case <-time.After(2 * time.Second):
				_ = p.Kill()
			}
		}
	}()

	go m.readLogs(e, stderr, true)
	go m.readLogs(e, stdout, false)
	go m.waitDone(e)
	return nil
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

// waitDone blocks on cmd.Wait() and emits the terminal status.
func (m *Manager) waitDone(e *entry) {
	err := e.cmd.Wait()
	e.mu.Lock()
	final := StatusStopped
	detail := ""
	if err != nil && !errors.Is(err, context.Canceled) {
		// cmd.Wait returns a non-nil err on a non-zero exit, including
		// signal-induced termination. Distinguish "we asked for it" via
		// the existing status — if Stop set status to Stopped already,
		// keep that; otherwise treat as Errored.
		if e.status != StatusStopped {
			final = StatusErrored
			detail = err.Error()
			e.lastErr = detail
		}
	}
	e.status = final
	close(e.doneCh)
	e.mu.Unlock()
	m.emit(Event{ID: e.id, Status: final, Detail: detail})
}

// emit best-effort writes to the events channel; if no consumer is
// reading, the event is dropped rather than blocking the goroutine.
func (m *Manager) emit(ev Event) {
	select {
	case m.events <- ev:
	default:
	}
}

// Stop terminates the entry by ID. Safe to call on an already-stopped
// entry; returns nil in that case.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("portfwd: no such entry %q", id)
	}
	e.mu.Lock()
	if e.status == StatusStopped || e.status == StatusErrored {
		e.mu.Unlock()
		return nil
	}
	e.status = StatusStopped
	e.mu.Unlock()
	e.cancel()
	return nil
}

// Remove forgets a stopped or errored entry. No-op for running entries
// (use Stop first).
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[id]; ok {
		s := e.statusRead()
		if s == StatusStopped || s == StatusErrored {
			delete(m.entries, id)
		}
	}
}

// List returns a snapshot of every registered entry, sorted by start time.
func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.snapshot())
	}
	// Newest first — the most recently started forward is what the user
	// most likely wants to act on.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].StartedAt.After(out[i].StartedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Logs returns the recent log lines for the given entry, oldest first.
func (m *Manager) Logs(id string) ([]string, error) {
	m.mu.Lock()
	e, ok := m.entries[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("portfwd: no such entry %q", id)
	}
	return e.logs.Snapshot(), nil
}

// StopAll terminates every running entry and waits up to timeout for
// each to exit. Called on TUI shutdown.
func (m *Manager) StopAll(timeout time.Duration) {
	m.mu.Lock()
	m.stopping = true
	es := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		es = append(es, e)
	}
	m.mu.Unlock()
	for _, e := range es {
		e.cancel()
	}
	deadline := time.Now().Add(timeout)
	for _, e := range es {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		select {
		case <-e.doneCh:
		case <-time.After(remaining):
		}
	}
}
