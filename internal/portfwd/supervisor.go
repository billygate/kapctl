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
