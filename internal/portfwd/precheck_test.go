package portfwd

import (
	"net"
	"strconv"
	"testing"
)

// listenLocal binds 127.0.0.1:0 and returns the listener and the
// allocated port. Caller must Close() the listener.
func listenLocal(t *testing.T) (net.Listener, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		_ = l.Close()
		t.Fatalf("split host/port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		_ = l.Close()
		t.Fatalf("atoi: %v", err)
	}
	return l, port
}

func TestIsLocalPortFree_Occupied(t *testing.T) {
	l, port := listenLocal(t)
	defer func() { _ = l.Close() }()
	if err := IsLocalPortFree(port); err == nil {
		t.Errorf("IsLocalPortFree(%d) = nil, want error", port)
	}
}

func TestIsLocalPortFree_AfterClose(t *testing.T) {
	l, port := listenLocal(t)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := IsLocalPortFree(port); err != nil {
		t.Errorf("IsLocalPortFree(%d) after close = %v, want nil", port, err)
	}
}

func TestFindFreeLocalPort_StartFree(t *testing.T) {
	l, port := listenLocal(t)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := FindFreeLocalPort(port, 10)
	if err != nil {
		t.Fatalf("FindFreeLocalPort: %v", err)
	}
	if got != port {
		t.Errorf("got %d, want %d (start was free)", got, port)
	}
}

func TestFindFreeLocalPort_BumpsPastOccupied(t *testing.T) {
	// Occupy three consecutive ports: start, start+1, start+2.
	l1, start := listenLocal(t)
	defer func() { _ = l1.Close() }()
	l2, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(start+1)))
	if err != nil {
		t.Skipf("could not bind start+1=%d: %v", start+1, err)
	}
	defer func() { _ = l2.Close() }()
	l3, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(start+2)))
	if err != nil {
		t.Skipf("could not bind start+2=%d: %v", start+2, err)
	}
	defer func() { _ = l3.Close() }()

	got, err := FindFreeLocalPort(start, 10)
	if err != nil {
		t.Fatalf("FindFreeLocalPort: %v", err)
	}
	if got <= start+2 {
		t.Errorf("got %d, want > %d", got, start+2)
	}
}

func TestFindFreeLocalPort_NegativeSpan(t *testing.T) {
	if _, err := FindFreeLocalPort(8080, -1); err == nil {
		t.Error("FindFreeLocalPort(_, -1) = nil error, want non-nil")
	}
}
