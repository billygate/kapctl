package core

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type okMsg struct{ value string }
type errMsg struct{ err error }

func TestLoaderCancelsPrevious(t *testing.T) {
	var l Loader
	started := make(chan struct{}, 2)
	finished := make(chan tea.Msg, 2)

	slow := func(ctx context.Context) tea.Msg {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return errMsg{err: ctx.Err()}
		case <-time.After(500 * time.Millisecond):
			return okMsg{value: "slow"}
		}
	}
	fast := func(_ context.Context) tea.Msg {
		started <- struct{}{}
		return okMsg{value: "fast"}
	}

	cmd1 := l.Start(context.Background(), slow)
	go func() { finished <- cmd1() }()

	<-started
	cmd2 := l.Start(context.Background(), fast)
	go func() { finished <- cmd2() }()

	got := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case msg := <-finished:
			switch m := msg.(type) {
			case Result:
				if ok, isOk := m.Payload.(okMsg); isOk {
					got[ok.value] = true
				}
			case nil:
				got["nil"] = true
			default:
				t.Fatalf("unexpected msg type %T: %+v", msg, msg)
			}
		case <-timeout:
			t.Fatal("timed out waiting for cmd results")
		}
	}
	if !got["fast"] {
		t.Error("fast result missing")
	}
	if got["slow"] {
		t.Error("slow result leaked through despite cancellation")
	}
}

func TestLoaderGenerationDropsStale(t *testing.T) {
	var l Loader
	cmd1 := l.Start(context.Background(), func(_ context.Context) tea.Msg {
		return okMsg{value: "first"}
	})
	cmd2 := l.Start(context.Background(), func(_ context.Context) tea.Msg {
		return okMsg{value: "second"}
	})

	results := []tea.Msg{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	for _, c := range []tea.Cmd{cmd1, cmd2} {
		c := c
		go func() {
			defer wg.Done()
			if msg := c(); msg != nil {
				mu.Lock()
				results = append(results, msg)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for _, r := range results {
		res, ok := r.(Result)
		if !ok {
			continue
		}
		if !l.Accept(res.Generation) {
			continue
		}
		if got := res.Payload.(okMsg).value; got != "second" {
			t.Errorf("Accept'd payload = %q, want %q", got, "second")
		}
	}
}
