package portfwd

import (
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// fakeCmd uses /bin/sh to print a "Forwarding from" line, then sleep
// (so the test can observe the running state and then Stop the entry).
func fakeCmd(opts StartOpts) *exec.Cmd {
	if runtime.GOOS == "windows" {
		// Windows isn't a target environment for kap; skip rather than
		// pretend the test works.
		panic("portfwd tests require unix")
	}
	port := strconv.Itoa(opts.LocalPort)
	script := `printf 'Forwarding from 127.0.0.1:` + port + ` -> ` + port + `\n' >&2; sleep 5`
	return exec.Command("/bin/sh", "-c", script)
}

// failingCmd exits non-zero immediately to exercise the Errored path.
func failingCmd(_ StartOpts) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", "echo boom >&2; exit 7")
}

func waitFor(t *testing.T, ev <-chan Event, want Status, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case e := <-ev:
			if e.Status == want {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for status %v", want)
		}
	}
}

func TestManagerStartRunningStop(t *testing.T) {
	m := NewManager(8, 64)
	m.SetCmdBuilder(fakeCmd)

	id, err := m.Start(StartOpts{
		Context: "ctx", Namespace: "ns", Target: "pg-0",
		Kind: KindPod, LocalPort: 5432, RemotePort: 5432,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Should reach Running once kubectl prints "Forwarding from".
	waitFor(t, m.Events(), StatusRunning, 3*time.Second)

	list := m.List()
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("List() = %+v, want one entry with ID %s", list, id)
	}
	if list[0].Status != StatusRunning {
		t.Errorf("entry status = %v, want StatusRunning", list[0].Status)
	}

	// Logs should contain the Forwarding line.
	logs, err := m.Logs(id)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(logs) == 0 {
		t.Error("Logs() returned no lines, expected at least the Forwarding banner")
	}

	if err := m.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, m.Events(), StatusStopped, 3*time.Second)
}

func TestManagerErrorPath(t *testing.T) {
	m := NewManager(8, 64)
	m.SetCmdBuilder(failingCmd)
	id, err := m.Start(StartOpts{
		Context: "ctx", Namespace: "ns", Target: "x",
		Kind: KindPod, LocalPort: 1234, RemotePort: 1234,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, m.Events(), StatusErrored, 3*time.Second)
	list := m.List()
	if len(list) != 1 || list[0].ID != id || list[0].Status != StatusErrored {
		t.Errorf("List() = %+v, want one StatusErrored entry", list)
	}
}

func TestManagerLocalPortConflict(t *testing.T) {
	m := NewManager(8, 64)
	m.SetCmdBuilder(fakeCmd)
	if _, err := m.Start(StartOpts{
		Context: "ctx", Namespace: "ns", Target: "pg-0",
		Kind: KindPod, LocalPort: 5432, RemotePort: 5432,
	}); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	waitFor(t, m.Events(), StatusRunning, 3*time.Second)

	// Second Start on the same local port must be refused.
	if _, err := m.Start(StartOpts{
		Context: "ctx", Namespace: "ns", Target: "pg-1",
		Kind: KindPod, LocalPort: 5432, RemotePort: 5432,
	}); err == nil {
		t.Error("expected conflict error on duplicate local port, got nil")
	}
}

func TestManagerStopAllReleasesEverything(t *testing.T) {
	m := NewManager(16, 64)
	m.SetCmdBuilder(fakeCmd)
	for i := range 3 {
		if _, err := m.Start(StartOpts{
			Context: "ctx", Namespace: "ns", Target: "pg-" + strconv.Itoa(i),
			Kind: KindPod, LocalPort: 6000 + i, RemotePort: 5432,
		}); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
	}
	m.StopAll(2 * time.Second)
	for _, s := range m.List() {
		if s.Status != StatusStopped && s.Status != StatusErrored {
			t.Errorf("after StopAll: entry %s status = %v, want stopped/errored", s.ID, s.Status)
		}
	}
}
