package portfwd

import (
	"context"
	"sort"
	"sync"
	"time"
)

// fakeClock implements Clock with manual advancement. Tests call
// Advance(d) to release pending After/Ticker channels.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
	tickers []*fakeTicker
}

type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{deadline: c.now.Add(d), ch: ch})
	return ch
}

// NewTicker returns a ticker driven by Advance(). The ticker keeps
// firing as long as Advance crosses a tick boundary.
func (c *fakeClock) NewTicker(d time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 16)
	ft := &fakeTicker{ch: ch, period: d, next: c.now.Add(d), clock: c}
	c.tickers = append(c.tickers, ft)
	return ft
}

type fakeTicker struct {
	ch     chan time.Time
	period time.Duration
	next   time.Time
	clock  *fakeClock
	stop   bool
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()               { t.stop = true }

// Advance moves time forward, firing any After-waiters and ticker
// boundaries that have come due. Call from the test goroutine; the
// supervisor goroutine will receive on the released channels.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	target := c.now

	// Fire After waiters whose deadlines have passed.
	remaining := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.deadline.After(target) {
			select {
			case w.ch <- target:
			default:
			}
		} else {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining

	// Advance tickers across all boundaries that have come due.
	for _, t := range c.tickers {
		if t.stop {
			continue
		}
		for !t.next.After(target) {
			select {
			case t.ch <- t.next:
			default:
			}
			t.next = t.next.Add(t.period)
		}
	}
	c.mu.Unlock()
}

// fakeProber is a programmable Prober for supervisor tests.
type fakeProber struct {
	mu sync.Mutex

	resolveResp  map[string]PodRef // key: ns/name
	getResp      map[string]PodRef
	getNotFound  map[string]bool
	findResp     map[string]string // key: ns/<sorted-labels>; value = pod name
	findNotFound map[string]bool
	findCalls    int
	getCalls     int
}

func newFakeProber() *fakeProber {
	return &fakeProber{
		resolveResp:  map[string]PodRef{},
		getResp:      map[string]PodRef{},
		getNotFound:  map[string]bool{},
		findResp:     map[string]string{},
		findNotFound: map[string]bool{},
	}
}

func (f *fakeProber) ResolvePod(_ context.Context, ns, name string) (PodRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := ns + "/" + name
	if f.getNotFound[key] {
		return PodRef{}, ErrPodNotFound
	}
	if r, ok := f.resolveResp[key]; ok {
		return r, nil
	}
	return PodRef{Name: name, UID: "uid-" + name, Phase: "Running", Ready: true, Labels: map[string]string{"app": "demo"}}, nil
}

func (f *fakeProber) GetPod(_ context.Context, ns, name string) (PodRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	key := ns + "/" + name
	if f.getNotFound[key] {
		return PodRef{}, ErrPodNotFound
	}
	if r, ok := f.getResp[key]; ok {
		return r, nil
	}
	return PodRef{Name: name, UID: "uid-" + name, Phase: "Running", Ready: true}, nil
}

func (f *fakeProber) FindReadyPodByLabels(_ context.Context, ns string, labels map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls++
	key := ns + "/" + labelKey(labels)
	if f.findNotFound[key] {
		return "", ErrPodNotFound
	}
	if name, ok := f.findResp[key]; ok {
		return name, nil
	}
	return "", ErrPodNotFound
}

func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		out += k + "=" + m[k] + ","
	}
	return out
}
