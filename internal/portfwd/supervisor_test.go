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

func TestSupervisorReconnectsWhenPodGoesAway(t *testing.T) {
	m := NewManager(16, 64)

	// kubectl-fake: prints Forwarding from, then sleeps. Stays running
	// until killed by the supervisor.
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

	id, err := m.Start(StartOpts{
		Context: "ctx", Namespace: "ns", Target: "pg-0",
		Kind: KindPod, LocalPort: 5432, RemotePort: 5432,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitFor(t, m.Events(), StatusRunning, 3*time.Second)

	// Flip the prober: pod is gone.
	fp.mu.Lock()
	fp.getNotFound["ns/pg-0"] = true
	fp.mu.Unlock()

	// Advance the fake clock past the 5s liveness tick.
	clk.Advance(6 * time.Second)

	ev := waitFor(t, m.Events(), StatusReconnecting, 3*time.Second)
	if !strings.Contains(strings.ToLower(ev.Detail), "pod") {
		t.Errorf("Reconnecting detail = %q, want it to mention pod", ev.Detail)
	}

	_ = m.Stop(id)
}
