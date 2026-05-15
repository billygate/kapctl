package core

import (
	"context"
	"errors"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// Result is the message produced by a Loader-backed tea.Cmd. The model
// must check Loader.Accept(Generation) before applying Payload, to drop
// stale results from superseded operations.
type Result struct {
	Generation uint64
	Payload    tea.Msg
}

// Loader sequences async fetches so that starting a new operation
// cancels the previous one and any in-flight result from the previous
// operation is dropped at the message boundary.
type Loader struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	gen    uint64
}

// Start cancels any in-flight load, increments the generation, and
// returns a tea.Cmd that runs fn under a fresh context. The returned
// Cmd produces nil if its context was cancelled, otherwise a Result.
func (l *Loader) Start(parent context.Context, fn func(context.Context) tea.Msg) tea.Cmd {
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
	}
	l.gen++
	myGen := l.gen
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.mu.Unlock()

	return func() tea.Msg {
		msg := fn(ctx)
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		return Result{Generation: myGen, Payload: msg}
	}
}

// Accept returns true if the given generation matches the current
// generation. Models call this in their Update to decide whether to
// apply or drop a Result.
func (l *Loader) Accept(gen uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return gen == l.gen
}
