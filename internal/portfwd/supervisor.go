package portfwd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

const (
	reconnectBudget    = 120 * time.Second
	livenessTick       = 5 * time.Second
	tcpProbeTimeout    = 500 * time.Millisecond
	tcpProbeFailThresh = 3
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

		target, err := m.resolveTarget(ctx, e)
		if err != nil {
			if ctx.Err() != nil {
				e.mu.Lock()
				e.status = StatusStopped
				e.mu.Unlock()
				m.emit(Event{ID: e.id, Status: StatusStopped})
				return
			}
			if m.recordFailure(ctx, e, "resolve target: "+err.Error()) {
				return
			}
			continue
		}

		reason, exitedClean := m.runOneAttempt(ctx, e, target)

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
func (m *Manager) runOneAttempt(ctx context.Context, e *entry, target string) (reason string, exitedClean bool) {
	e.mu.Lock()
	e.tcpFailStreak = 0
	e.mu.Unlock()

	opts := e.opts
	opts.Target = target
	e.cmd = m.builder(opts)

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

	ticker := m.clock.NewTicker(livenessTick)
	defer ticker.Stop()

	for {
		select {
		case <-readyCh:
			// Successfully reached Running — clear reconnect bookkeeping.
			e.mu.Lock()
			e.attempts = 0
			e.reconnectStartedAt = time.Time{}
			e.tcpFailStreak = 0
			e.mu.Unlock()
			// Retain lastReconnectReason so eventToToast can emit the
			// "reconnected" success toast when status transitions to Running.
			// recordFailure overwrites it on the next failure.
		case <-ticker.C():
			if reason := m.probeOnce(ctx, e); reason != "" {
				return reason, false
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

// resolveTarget returns the pod/service name kubectl should target this
// attempt. KindService is always opts.Target (kubectl picks a fresh
// backing pod itself). For KindPod, the first attempt uses opts.Target;
// subsequent attempts re-find a Ready pod by labels saved at Start time.
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
	// Cache the new name for subsequent liveness probing this attempt.
	e.mu.Lock()
	e.opts.Target = name
	e.mu.Unlock()
	return name, nil
}

// probeOnce runs one liveness probe. Returns a non-empty reason if the
// forward should reconnect; empty string means the forward is still healthy.
//
// Probes only run while Status == StatusRunning (no probing during Starting
// or Reconnecting). For KindService the GetPod check is skipped — kubectl
// re-resolves the backing pod itself on restart.
func (m *Manager) probeOnce(ctx context.Context, e *entry) string {
	e.mu.Lock()
	status := e.status
	kind := e.opts.Kind
	ns := e.opts.Namespace
	target := e.opts.Target
	expectedUID := e.podUID
	e.mu.Unlock()
	if status != StatusRunning {
		return ""
	}
	if kind != KindPod {
		return ""
	}

	if prober, _ := m.getProberForEntry(e); prober != nil {
		pod, err := prober.GetPod(ctx, ns, target)
		if errors.Is(err, ErrPodNotFound) {
			return "pod gone"
		}
		if err == nil {
			if pod.Phase != "Running" {
				return "pod phase " + pod.Phase
			}
			if expectedUID != "" && pod.UID != expectedUID {
				return "pod recreated (UID changed)"
			}
		}
		// Transient API error — fall through to TCP probe.
	}

	// TCP probe localhost:LocalPort with 3-failure debounce.
	addr := fmt.Sprintf("127.0.0.1:%d", e.opts.LocalPort)
	conn, derr := net.DialTimeout("tcp", addr, tcpProbeTimeout)
	if derr != nil {
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

// getProberForEntry lazily builds a Prober for the entry's context.
// Returns nil if no factory is set (then probing is skipped — the
// supervisor still detects subprocess exit).
func (m *Manager) getProberForEntry(e *entry) (Prober, error) {
	m.mu.Lock()
	f := m.proberFactory
	m.mu.Unlock()
	if f == nil {
		return nil, nil
	}
	return f(e.opts.Context)
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
