package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

type fakeAPI struct {
	listed     []container.Summary
	listFilter filters.Args
	paused     []string
	unpaused   []string
	pauseErr   error
}

func (f *fakeAPI) ContainerList(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
	f.listFilter = opts.Filters
	return f.listed, nil
}

func (f *fakeAPI) ContainerPause(_ context.Context, name string) error {
	if f.pauseErr != nil {
		return f.pauseErr
	}
	f.paused = append(f.paused, name)
	return nil
}

func (f *fakeAPI) ContainerUnpause(_ context.Context, name string) error {
	f.unpaused = append(f.unpaused, name)
	return nil
}

func TestGetKindContainersStripsLeadingSlash(t *testing.T) {
	f := &fakeAPI{listed: []container.Summary{
		{Names: []string{"/kind-control-plane"}},
		{Names: []string{"/kind-worker"}},
	}}
	c := &Client{cli: f}
	names, err := c.GetKindContainers(context.Background(), "running")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	for _, n := range names {
		if strings.HasPrefix(n, "/") {
			t.Errorf("name %q still has leading slash", n)
		}
	}
}

func TestGetKindContainersAppliesLabelFilter(t *testing.T) {
	f := &fakeAPI{}
	c := &Client{cli: f}
	_, _ = c.GetKindContainers(context.Background(), "running")

	values := f.listFilter.Get("label")
	found := false
	for _, v := range values {
		if v == "io.x-k8s.kind.cluster" {
			found = true
		}
	}
	if !found {
		t.Errorf("label filter missing io.x-k8s.kind.cluster, got %v", values)
	}

	stateValues := f.listFilter.Get("status")
	if len(stateValues) != 1 || stateValues[0] != "running" {
		t.Errorf("status filter = %v, want [running]", stateValues)
	}
}

func TestGetKindContainersEmptyState(t *testing.T) {
	f := &fakeAPI{listed: []container.Summary{
		{Names: []string{"/kind-control-plane"}},
	}}
	c := &Client{cli: f}
	_, _ = c.GetKindContainers(context.Background(), "")

	stateValues := f.listFilter.Get("status")
	if len(stateValues) != 0 {
		t.Errorf("expected no status filter for empty state, got %v", stateValues)
	}
}

func TestPauseContainersStopsOnFirstError(t *testing.T) {
	f := &fakeAPI{pauseErr: context.Canceled}
	c := &Client{cli: f}
	err := c.PauseContainers(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(f.paused) != 0 {
		t.Errorf("paused = %v, want none (first call errored)", f.paused)
	}
}

func TestPauseContainersSuccess(t *testing.T) {
	f := &fakeAPI{}
	c := &Client{cli: f}
	err := c.PauseContainers(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.paused) != 2 {
		t.Errorf("paused = %v, want [a b]", f.paused)
	}
}

func TestResumeContainers(t *testing.T) {
	f := &fakeAPI{}
	c := &Client{cli: f}
	err := c.ResumeContainers(context.Background(), []string{"x", "y"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.unpaused) != 2 || f.unpaused[0] != "x" || f.unpaused[1] != "y" {
		t.Errorf("unpaused = %v, want [x y]", f.unpaused)
	}
}

func TestGetStatus(t *testing.T) {
	f := &fakeAPI{listed: []container.Summary{
		{Names: []string{"/kind-worker"}, Status: "Up 2 hours"},
	}}
	c := &Client{cli: f}
	statuses, err := c.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Name != "kind-worker" {
		t.Errorf("Name = %q, want kind-worker", statuses[0].Name)
	}
	if statuses[0].Status != "Up 2 hours" {
		t.Errorf("Status = %q, want 'Up 2 hours'", statuses[0].Status)
	}
}
