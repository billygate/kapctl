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

// supervise drives the lifecycle of a single entry as a reconnect loop:
// resolve → start → wait for failure → record + backoff → loop again until
// the reconnect budget (120s of continuous attempts since the last
// successful Running) is exhausted, at which point the entry settles
// in StatusErrored.
func (m *Manager) supervise(ctx context.Context, e *entry) {
	defer close(e.doneCh)

	for {
		e.mu.Lock()
		e.status = StatusStarting
		e.mu.Unlock()
		m.emit(Event{ID: e.id, Status: StatusStarting})

		reason, exitedClean := m.runOneAttempt(ctx, e)

		// Stop wins races with everything.
		if ctx.Err() != nil {
			e.mu.Lock()
			e.status = StatusStopped
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

		if m.recordFailure(ctx, e, reason) {
			return
		}
		// loop body restarts the attempt
	}
}

// runOneAttempt launches kubectl once. Returns (reason, exitedClean).
// On a successful "Forwarding from", clears reconnect bookkeeping
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

	readyCh := make(chan struct{}, 1)
	go m.readLogs(e, stderr, true, readyCh)
	go m.readLogs(e, stdout, false, nil)

	waitErr := make(chan error, 1)
	go func() { waitErr <- e.cmd.Wait() }()

	for {
		select {
		case <-readyCh:
			// Successfully reached Running — clear reconnect bookkeeping.
			e.mu.Lock()
			e.attempts = 0
			e.reconnectStartedAt = time.Time{}
			e.mu.Unlock()
			// Don't clear lastReconnectReason here — Task 14's toast policy
			// reads it to emit a "reconnected" success toast. recordFailure
			// will overwrite it on the next failure.
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

// recordFailure handles bookkeeping after a failed attempt: emit
// StatusReconnecting, decide whether to retry or settle in StatusErrored,
// and wait the backoff interval. Returns true if the supervisor should
// stop looping (budget exhausted OR user Stop during backoff).
func (m *Manager) recordFailure(ctx context.Context, e *entry, reason string) (stop bool) {
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
		e.mu.Lock()
		e.status = StatusStopped
		e.mu.Unlock()
		m.emit(Event{ID: e.id, Status: StatusStopped})
		return true
	}
}

// terminalError is kept for the rare case where a totally broken pipe/start
// path needs to settle into Errored without going through the reconnect loop.
// Currently unused after the loop refactor; left in for safety until probe
// tasks (8-10) add code paths that may need it.
func (m *Manager) terminalError(e *entry, err error) {
	e.mu.Lock()
	e.status = StatusErrored
	e.lastErr = err.Error()
	e.mu.Unlock()
	m.emit(Event{ID: e.id, Status: StatusErrored, Detail: err.Error()})
}

// readLogs scans a pipe line-by-line into the entry's ring buffer.
// On stderr we look for kubectl's "Forwarding from" line and flip the
// status from Starting → Running on first sight, also signalling on
// readyCh so the supervisor can clear reconnect bookkeeping.
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
