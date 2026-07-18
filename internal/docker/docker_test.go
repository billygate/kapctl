package docker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// writeContextMeta writes a Docker CLI context metadata file for name with the
// given docker endpoint host, mirroring ~/.docker/contexts/meta/<sha256>/meta.json.
func writeContextMeta(t *testing.T, configDir, name, host string) {
	t.Helper()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	dir := filepath.Join(configDir, "contexts", "meta", digest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"Name":%q,"Endpoints":{"docker":{"Host":%q}}}`, name, host)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHostForContextReadsMetaEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeContextMeta(t, dir, "orbstack", "unix:///Users/me/.orbstack/run/docker.sock")

	host, err := hostForContext(dir, "orbstack")
	if err != nil {
		t.Fatal(err)
	}
	if host != "unix:///Users/me/.orbstack/run/docker.sock" {
		t.Errorf("host = %q, want the orbstack socket", host)
	}
}

func TestHostForContextDefaultIsNoOverride(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", "default"} {
		host, err := hostForContext(dir, name)
		if err != nil {
			t.Fatalf("name %q: unexpected error: %v", name, err)
		}
		if host != "" {
			t.Errorf("name %q: host = %q, want empty (use SDK default)", name, host)
		}
	}
}

func TestHostForContextMissingContextErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := hostForContext(dir, "nonexistent"); err == nil {
		t.Error("expected error for missing context metadata, got nil")
	}
}

func TestResolveContextNameFromConfig(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"currentContext":"orbstack"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveContextName(dir); got != "orbstack" {
		t.Errorf("resolveContextName = %q, want orbstack", got)
	}
}

func TestResolveContextNameEnvWins(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "prod")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"currentContext":"orbstack"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveContextName(dir); got != "prod" {
		t.Errorf("resolveContextName = %q, want prod (DOCKER_CONTEXT wins)", got)
	}
}

func TestResolveContextNameNoConfig(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "")
	if got := resolveContextName(t.TempDir()); got != "" {
		t.Errorf("resolveContextName = %q, want empty when no config.json", got)
	}
}

type fakeAPI struct {
	listed     []container.Summary
	listFilter filters.Args
	paused     []string
	unpaused   []string
	restarted  []string
	pauseErr   error
	restartErr error
}

func (f *fakeAPI) ContainerRestart(_ context.Context, name string, _ container.StopOptions) error {
	if f.restartErr != nil {
		return f.restartErr
	}
	f.restarted = append(f.restarted, name)
	return nil
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

func TestRestartContainersSuccess(t *testing.T) {
	f := &fakeAPI{}
	c := &Client{cli: f}
	err := c.RestartContainers(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.restarted) != 2 || f.restarted[0] != "a" || f.restarted[1] != "b" {
		t.Errorf("restarted = %v, want [a b]", f.restarted)
	}
}

func TestRestartContainersStopsOnFirstError(t *testing.T) {
	f := &fakeAPI{restartErr: context.Canceled}
	c := &Client{cli: f}
	err := c.RestartContainers(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(f.restarted) != 0 {
		t.Errorf("restarted = %v, want none (first call errored)", f.restarted)
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
