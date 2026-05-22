package portfwd

import (
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flakyCmdBuilder returns a CmdBuilder that fails `failuresBeforeSuccess`
// times, then succeeds (prints "Forwarding from", sleeps).
func flakyCmdBuilder(failuresBeforeSuccess int32) CmdBuilder {
	var calls atomic.Int32
	return func(opts StartOpts) *exec.Cmd {
		n := calls.Add(1)
		if n <= failuresBeforeSuccess {
			return exec.Command("/bin/sh", "-c", "exit 1")
		}
		// stable port template so the script reads cleanly
		return exec.Command("/bin/sh", "-c",
			`printf 'Forwarding from 127.0.0.1:5432 -> 5432\n' >&2; sleep 5`)
	}
}

func TestSupervisorReconnectsAfterSubprocessExit(t *testing.T) {
	m := NewManager(16, 64)
	m.SetCmdBuilder(flakyCmdBuilder(1)) // fail once, then succeed

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

	// Advance fake clock past the first backoff (1s).
	clk.Advance(2 * time.Second)

	// Second attempt succeeds → Running.
	waitFor(t, m.Events(), StatusRunning, 3*time.Second)

	// Attempts should be reset after success.
	list := m.List()
	if len(list) != 1 || list[0].Attempts != 0 {
		t.Errorf("after recovery: Attempts = %d, want 0; list=%+v", list[0].Attempts, list)
	}

	_ = m.Stop(id)
}
