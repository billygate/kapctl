package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/kube"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeKube implements KubeAPI.
type fakeKube struct {
	contexts []string
	current  string
	pods     []kube.PodInfo
}

func (f *fakeKube) GetContexts() []string     { return f.contexts }
func (f *fakeKube) GetCurrentContext() string { return f.current }
func (f *fakeKube) GetNamespaces(context.Context) ([]string, error) {
	return []string{"default"}, nil
}
func (f *fakeKube) GetPods(context.Context, string) ([]kube.PodInfo, error) { return f.pods, nil }
func (f *fakeKube) GetPodRole(context.Context, string, string) (string, error) {
	return "master", nil
}
func (f *fakeKube) GetPodPorts(context.Context, string, string) ([]kube.ContainerPort, error) {
	return []kube.ContainerPort{{Name: "pg", Port: 5432}}, nil
}
func (f *fakeKube) GetPodLogs(context.Context, string, string, kube.LogOptions) (string, error) {
	return "hello\nworld", nil
}

// fakeDocker implements DockerAPI (unused fields return empty).
type fakeDocker struct{}

func (fakeDocker) GetStatus(context.Context) ([]docker.ContainerStatus, error) { return nil, nil }
func (fakeDocker) GetKindContainers(context.Context, string) ([]string, error) { return nil, nil }
func (fakeDocker) PauseContainers(context.Context, []string) error             { return nil }
func (fakeDocker) ResumeContainers(context.Context, []string) error            { return nil }

type fakeSpacebox struct{ installed bool }

func (f fakeSpacebox) IsInstalled() bool { return f.installed }
func (fakeSpacebox) Up() error           { return nil }
func (fakeSpacebox) Down() error         { return nil }

func newTestDeps(installed bool) Deps {
	return Deps{
		NewKube: func(string) (KubeAPI, error) {
			return &fakeKube{contexts: []string{"a", "b"}, current: "a"}, nil
		},
		Docker:   fakeDocker{},
		Spacebox: fakeSpacebox{installed: installed},
	}
}

func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
	}
	return names
}

func TestListContextsRoundTrip(t *testing.T) {
	srv := NewServer(newTestDeps(true), Options{})
	cs := connect(t, srv)
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_contexts"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res.Content)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	var payload struct {
		Current  string   `json:"current"`
		Contexts []string `json:"contexts"`
	}
	if err := json.Unmarshal([]byte(tc.Text), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Current != "a" || len(payload.Contexts) != 2 {
		t.Fatalf("payload = %+v, want current=a and 2 contexts", payload)
	}
}

func TestReadOnlyGating(t *testing.T) {
	srv := NewServer(newTestDeps(true), Options{AllowLocalControl: false})
	cs := connect(t, srv)
	defer func() { _ = cs.Close() }()
	names := toolNames(t, cs)

	for _, want := range []string{"list_contexts", "list_pods", "get_pod_logs", "local_status"} {
		if !names[want] {
			t.Errorf("expected tool %q to be registered", want)
		}
	}
	for _, absent := range []string{"local_pause", "local_resume", "local_up", "local_down"} {
		if names[absent] {
			t.Errorf("tool %q must be absent in read-only mode", absent)
		}
	}
}

func TestAllowLocalControlGating(t *testing.T) {
	srv := NewServer(newTestDeps(true), Options{AllowLocalControl: true})
	cs := connect(t, srv)
	defer func() { _ = cs.Close() }()
	names := toolNames(t, cs)
	for _, want := range []string{"local_pause", "local_resume", "local_up", "local_down"} {
		if !names[want] {
			t.Errorf("expected tool %q with AllowLocalControl", want)
		}
	}
}

func TestSpaceboxGatingWhenNotInstalled(t *testing.T) {
	srv := NewServer(newTestDeps(false), Options{AllowLocalControl: true})
	cs := connect(t, srv)
	defer func() { _ = cs.Close() }()
	names := toolNames(t, cs)
	if names["local_up"] || names["local_down"] {
		t.Errorf("local_up/local_down must be absent when spacebox is not installed")
	}
	if !names["local_pause"] {
		t.Errorf("local_pause should still be present")
	}
}
