package portfwd

import "sync"

// ringBuf is a fixed-capacity FIFO of strings. Pushing past capacity
// overwrites the oldest entry. Snapshot returns a copy in oldest-first
// order so the caller can render without holding the lock.
type ringBuf struct {
	mu   sync.Mutex
	data []string
	head int
	full bool
}

func newRingBuf(size int) *ringBuf {
	return &ringBuf{data: make([]string, size)}
}

func (r *ringBuf) Push(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[r.head] = s
	r.head = (r.head + 1) % len(r.data)
	if r.head == 0 {
		r.full = true
	}
}

func (r *ringBuf) Snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]string, r.head)
		copy(out, r.data[:r.head])
		return out
	}
	out := make([]string, 0, len(r.data))
	out = append(out, r.data[r.head:]...)
	out = append(out, r.data[:r.head]...)
	return out
}
