package portfwd

import (
	"fmt"
	"net"
	"strconv"
)

// IsLocalPortFree probes whether 127.0.0.1:port can be bound. The
// listener is closed immediately. Returns nil if free, a non-nil error
// if the port is in use or the bind fails for any other reason.
func IsLocalPortFree(port int) error {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return l.Close()
}

// FindFreeLocalPort returns the first port in [start, start+span] for
// which IsLocalPortFree succeeds. Returns 0 and a non-nil error if no
// port in the range is free. Out-of-range candidates (<1 or >65535)
// are skipped. span must be >= 0.
func FindFreeLocalPort(start, span int) (int, error) {
	if span < 0 {
		return 0, fmt.Errorf("portfwd: negative span %d", span)
	}
	for p := start; p <= start+span; p++ {
		if p < 1 || p > 65535 {
			continue
		}
		if err := IsLocalPortFree(p); err == nil {
			return p, nil
		}
	}
	return 0, fmt.Errorf("portfwd: no free port in [%d, %d]", start, start+span)
}
