package panes

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/billygate/kap-toolsbox/internal/docker"
	"github.com/billygate/kap-toolsbox/internal/kube"
	"github.com/billygate/kap-toolsbox/internal/tui/core"
	"github.com/billygate/kap-toolsbox/internal/tui/overlays"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
	"github.com/billygate/kap-toolsbox/internal/tui/themes"
)

// mockDockerClient satisfies core.DockerClient for testing.
type mockDockerClient struct {
	status []docker.ContainerStatus
}

func (m *mockDockerClient) GetKindContainers(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockDockerClient) PauseContainers(_ context.Context, _ []string) error  { return nil }
func (m *mockDockerClient) ResumeContainers(_ context.Context, _ []string) error { return nil }
func (m *mockDockerClient) GetStatus(_ context.Context) ([]docker.ContainerStatus, error) {
	return m.status, nil
}

// mockKubeClient satisfies core.KubeClient for testing.
type mockKubeClient struct {
	contexts       []string
	currentContext string
	namespaces     []string
	pods           []kube.PodInfo
	ports          []kube.ContainerPort

	// Optional failure injection — when set, the corresponding Get*
	// method returns the error instead of the data slice.
	failNS       error
	failPods     error
	failPodPorts error
}

func (m *mockKubeClient) GetContexts() []string     { return m.contexts }
func (m *mockKubeClient) GetCurrentContext() string { return m.currentContext }
func (m *mockKubeClient) GetNamespaces(_ context.Context) ([]string, error) {
	if m.failNS != nil {
		return nil, m.failNS
	}
	return m.namespaces, nil
}
func (m *mockKubeClient) GetPods(_ context.Context, _ string) ([]kube.PodInfo, error) {
	if m.failPods != nil {
		return nil, m.failPods
	}
	return m.pods, nil
}
func (m *mockKubeClient) GetPodPorts(_ context.Context, _, _ string) ([]kube.ContainerPort, error) {
	if m.failPodPorts != nil {
		return nil, m.failPodPorts
	}
	return m.ports, nil
}
func (m *mockKubeClient) GetPodRole(_ context.Context, _, _ string) (string, error) {
	return "unknown", nil
}
func (m *mockKubeClient) DeletePod(_ context.Context, _, _ string) error {
	return nil
}

func newTestStyles() *styles.Styles {
	p, _ := themes.Get("catppuccin")
	return styles.New(p)
}

// ── Explorer pane tests ──────────────────────────────────────────────────────

func TestExplorerInitReturnsNilCmd(t *testing.T) {
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s)
	if cmd := e.Init(); cmd != nil {
		t.Error("Explorer.Init() should return nil when kube err is set")
	}
}

func TestExplorerSetSize(t *testing.T) {
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s)
	e.SetSize(100, 40)
	if e.width != 100 {
		t.Errorf("width = %d, want 100", e.width)
	}
	if e.height != 40 {
		t.Errorf("height = %d, want 40", e.height)
	}
}

func TestExplorerViewErrorState(t *testing.T) {
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("kubeconfig not found"), s)
	v := e.View(80, 20)
	if v == "" {
		t.Error("View should render error message when kube error is set")
	}
}

func TestExplorerUpdateWindowSize(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s)
	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	next, _ := e.Update(msg)
	_ = next
}

func TestExplorerUpdateBackKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s)
	// Back key on step 0 (context) is a no-op
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	next, _ := e.Update(msg)
	_ = next
}

func TestExplorerUpdateKeyForwardsToList(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s)
	msg := tea.KeyMsg{Type: tea.KeyDown}
	_, _ = e.Update(msg)
}

func TestExplorerUpdateSelectOnError(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s)
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	_, _ = e.Update(msg)
}

func TestExplorerWithMockKubeInitList(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{
		contexts:       []string{"ctx-a", "ctx-b"},
		currentContext: "ctx-a",
	}
	e := NewExplorer(mk, nil, s)
	if e.step != stepContext {
		t.Errorf("initial step = %v, want stepContext", e.step)
	}
	// Should have rendered 2 context items
	if len(e.list.Items()) != 2 {
		t.Errorf("list items = %d, want 2", len(e.list.Items()))
	}
}

func TestExplorerSetSizeWithMockKube(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.SetSize(120, 40)
	if e.width != 120 || e.height != 40 {
		t.Errorf("SetSize failed: width=%d height=%d", e.width, e.height)
	}
}

func TestExplorerViewWithMockKube(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a", "ctx-b"}}
	e := NewExplorer(mk, nil, s)
	e.SetSize(80, 20)
	v := e.View(80, 20)
	if v == "" {
		t.Error("View should return non-empty string")
	}
}

func TestExplorerUpdateFilterKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")}
	_, _ = e.Update(msg)
}

func TestExplorerUpdateNumericKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a", "ctx-b", "ctx-c"}}
	e := NewExplorer(mk, nil, s)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}
	_, _ = e.Update(msg)
}

func TestExplorerUpdateEscBack(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	// Pressing esc on step 0 is a no-op (step stays 0)
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	next, _ := e.Update(msg)
	if next.step != stepContext {
		t.Errorf("step = %v after esc on stepContext, want stepContext", next.step)
	}
}

func TestExplorerUpdateCoreResultStale(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	// Gen=0 matches initial loader gen, so it's accepted
	msg := core.Result{Generation: 0, Payload: namespacesLoadedMsg{items: []string{"ns-1", "ns-2"}}}
	next, _ := e.Update(msg)
	_ = next
}

func TestExplorerUpdateNonKeyMsg(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	// Non-key, non-result message should be forwarded to the list
	msg := tea.WindowSizeMsg{Width: 80, Height: 20}
	_, _ = e.Update(msg)
}

func TestExplorerInitListStepAction(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	// Jump to stepAction manually
	e.step = stepAction
	e.ctx = "ctx-a"
	e.ns = "billing"
	e.pod = "pg-0"
	e.initList(80, 20)
	// Should have 5 action items (logs, exec, describe, port-forward, delete)
	if len(e.list.Items()) != 5 {
		t.Errorf("stepAction list items = %d, want 5", len(e.list.Items()))
	}
}

func TestExplorerInitListStepNamespace(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.step = stepNamespace
	e.namespaces = []string{"billing", "default", "kube-system"}
	e.initList(80, 20)
	if len(e.list.Items()) != 3 {
		t.Errorf("stepNamespace list items = %d, want 3", len(e.list.Items()))
	}
}

func TestExplorerRenderPodTable(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.step = stepPod
	e.pods = []kube.PodInfo{
		{Name: "pg-0", Status: "Running", Restarts: 0, Age: "1h", Ports: []int32{5432}},
		{Name: "pg-1", Status: "Pending", Restarts: 2, Age: "30m", Ports: nil},
	}
	e.initList(80, 20)
	view := e.View(80, 20)
	if view == "" {
		t.Error("renderPodTable should return non-empty view")
	}
}

func TestExplorerBuildPortItems(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.podPorts = []kube.ContainerPort{{Name: "http", Port: 8080}}
	items := e.buildPortItems()
	if len(items) == 0 {
		t.Error("buildPortItems should return non-empty items")
	}
}

// ── Local pane tests ──────────────────────────────────────────────────────────

func TestLocalSetSize(t *testing.T) {
	s := newTestStyles()
	l := NewLocal(nil, s)
	l.SetSize(100, 30)
	if l.width != 100 {
		t.Errorf("width = %d, want 100", l.width)
	}
	if l.height != 30 {
		t.Errorf("height = %d, want 30", l.height)
	}
}

func TestLocalViewRenders(t *testing.T) {
	s := newTestStyles()
	l := NewLocal(nil, s)
	l.SetSize(80, 20)
	v := l.View(80, 20)
	if v == "" {
		t.Error("Local.View should return non-empty string")
	}
}

func TestLocalUpdateSelectKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	l := NewLocal(nil, s)
	l.SetSize(80, 20)
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	_, _ = l.Update(msg)
}

func TestLocalUpdateNumericKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	l := NewLocal(nil, s)
	l.SetSize(80, 20)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}
	_, _ = l.Update(msg)
}

func TestLocalUpdateFilterKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	l := NewLocal(nil, s)
	l.SetSize(80, 20)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")}
	_, _ = l.Update(msg)
}

func TestLocalUpdateWindowSizeMsg(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	l := NewLocal(nil, s)
	msg := tea.WindowSizeMsg{Width: 100, Height: 30}
	_, _ = l.Update(msg)
}

func TestLocalViewWithStatus(t *testing.T) {
	s := newTestStyles()
	l := NewLocal(nil, s)
	l.status = []docker.ContainerStatus{
		{Name: "kind-control-plane", Status: "Up 2 hours"},
	}
	l.SetSize(80, 30)
	v := l.View(80, 30)
	if v == "" {
		t.Error("Local.View should return non-empty string with status")
	}
}

func TestLocalUpdateCoreResultAccepted(t *testing.T) {
	s := newTestStyles()
	l := NewLocal(nil, s)
	// Generation 0 matches the initial loader gen, so the result is accepted.
	msg := core.Result{Generation: 0, Payload: core.LocalStatusMsg([]docker.ContainerStatus{{Name: "kind", Status: "running"}})}
	next, _ := l.Update(msg)
	if len(next.status) != 1 || next.status[0].Name != "kind" {
		t.Errorf("status = %+v, want one item 'kind'", next.status)
	}
}

func TestLocalUpdateCoreResultToast(t *testing.T) {
	s := newTestStyles()
	l := NewLocal(nil, s)
	// A ToastMsg payload in a core.Result is forwarded as a new message.
	msg := core.Result{Generation: 0, Payload: overlays.ToastMsg{Kind: overlays.ToastError, Text: "oops"}}
	_, cmd := l.Update(msg)
	if cmd == nil {
		t.Error("ToastMsg payload should produce a non-nil cmd")
	}
}

func TestBuildLocalCmdKnownActions(t *testing.T) {
	tests := []struct {
		action  string
		wantNil bool
		wantBin string
	}{
		{"up", false, "spacebox"},
		{"down", false, "spacebox"},
		{"pause", false, "sh"},
		{"resume", false, "sh"},
		{"status", false, "docker"},
		{"unknown", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			cmd := buildLocalCmd(tt.action)
			if tt.wantNil {
				if cmd != nil {
					t.Errorf("buildLocalCmd(%q) = non-nil, want nil", tt.action)
				}
				return
			}
			if cmd == nil {
				t.Fatalf("buildLocalCmd(%q) = nil, want non-nil", tt.action)
			}
			if cmd.Path == "" {
				t.Fatalf("buildLocalCmd(%q).Path is empty", tt.action)
			}
		})
	}
}

func TestBuildExplorerCmdLogs(t *testing.T) {
	cmd := buildExplorerCmd("ctx-a", "billing", "pg-0", "logs", "")
	if cmd == nil {
		t.Fatal("expected non-nil cmd for logs")
	}
	args := cmd.Args
	found := false
	for _, a := range args {
		if a == "logs" {
			found = true
		}
	}
	if !found {
		t.Errorf("buildExplorerCmd logs: args %v missing 'logs'", args)
	}
}

func TestBuildExplorerCmdExec(t *testing.T) {
	cmd := buildExplorerCmd("ctx-a", "billing", "pg-0", "exec", "")
	if cmd == nil {
		t.Fatal("expected non-nil cmd for exec")
	}
	args := cmd.Args
	found := false
	for _, a := range args {
		if a == "exec" {
			found = true
		}
	}
	if !found {
		t.Errorf("buildExplorerCmd exec: args %v missing 'exec'", args)
	}
}

func TestBuildExplorerCmdDescribe(t *testing.T) {
	cmd := buildExplorerCmd("ctx-a", "billing", "pg-0", "describe", "")
	if cmd == nil {
		t.Fatal("expected non-nil cmd for describe")
	}
	args := cmd.Args
	found := false
	for _, a := range args {
		if a == "describe" {
			found = true
		}
	}
	if !found {
		t.Errorf("buildExplorerCmd describe: args %v missing 'describe'", args)
	}
}

func TestMaxHelper(t *testing.T) {
	if got := max(3, 5); got != 5 {
		t.Errorf("max(3,5) = %d, want 5", got)
	}
	if got := max(7, 2); got != 7 {
		t.Errorf("max(7,2) = %d, want 7", got)
	}
	if got := max(4, 4); got != 4 {
		t.Errorf("max(4,4) = %d, want 4", got)
	}
}

func TestExplorerRenderPodTableEmpty(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.step = stepPod
	e.initList(80, 20) // no pods loaded
	view := e.View(80, 20)
	if view == "" {
		t.Error("renderPodTable with empty pods should return non-empty view ('No pods found')")
	}
}

func TestLocalUpdateTickMsgNoDocker(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	l := NewLocal(nil, s)
	// With no docker client wired in, TickMsg is a no-op — there's
	// nothing to refresh until SetDockerClient lands.
	_, cmd := l.Update(core.TickMsg{})
	if cmd != nil {
		t.Error("TickMsg with nil docker should produce a nil Cmd")
	}
}

func TestLocalUpdateTickMsgWithDocker(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	l := NewLocal(&mockDockerClient{}, s)
	// TickMsg triggers a new loader.Start; cmd should be non-nil
	_, cmd := l.Update(core.TickMsg{})
	if cmd == nil {
		t.Error("TickMsg with docker client should produce a non-nil Cmd")
	}
}

func TestLocalSetDockerClientTriggersFetch(t *testing.T) {
	s := newTestStyles()
	l := NewLocal(nil, s)
	cmd := l.SetDockerClient(&mockDockerClient{})
	if cmd == nil {
		t.Error("SetDockerClient should return a non-nil Cmd to fetch status")
	}
}

func TestExplorerSetKubeClientReady(t *testing.T) {
	s := newTestStyles()
	e := NewExplorer(nil, nil, s)
	// Pane starts in not-ready state: View should render the connecting placeholder.
	if v := e.View(80, 20); v == "" {
		t.Fatal("View() should render even when kube is not yet ready")
	}
	mk := &mockKubeClient{contexts: []string{"ctx-a", "ctx-b"}}
	e.SetKubeClient(mk, nil)
	if !e.kubeReady {
		t.Error("kubeReady should be true after SetKubeClient with a non-nil client")
	}
	if len(e.list.Items()) != 2 {
		t.Errorf("after SetKubeClient: list items = %d, want 2", len(e.list.Items()))
	}
}

func TestExplorerEnterPortFormWithBumpedLocal(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}

	// Pick a free ephemeral port and occupy it so enterPortForm has to bump.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	picked, _ := strconv.Atoi(portStr)

	e.enterPortForm(picked, picked)

	if e.step != stepPortForm {
		t.Errorf("step = %v, want stepPortForm", e.step)
	}
	if e.formLocal.Value() == strconv.Itoa(picked) {
		t.Errorf("formLocal = %q, want bumped value", e.formLocal.Value())
	}
	if e.formRemote.Value() != strconv.Itoa(picked) {
		t.Errorf("formRemote = %q, want %d", e.formRemote.Value(), picked)
	}
	if e.formInfo == "" {
		t.Error("formInfo should describe the bump")
	}
}

func TestExplorerViewRendersPortForm(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.SetSize(80, 24)
	e.pod = "pg-0"
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}
	e.enterPortForm(5432, 5432)

	v := e.View(80, 24)
	if v == "" {
		t.Fatal("View returned empty string for stepPortForm")
	}
	for _, want := range []string{"Local", "Remote", "tab", "enter", "esc"} {
		if !strings.Contains(v, want) {
			t.Errorf("View missing %q\nview=\n%s", want, v)
		}
	}
}

func TestExplorerLoadErrorShownInline(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.SetSize(80, 20)
	e.step = stepNamespace
	e.ctx = "ctx-a"

	loadErr := errors.New("dial tcp 10.0.0.1:6443: i/o timeout")
	// Generation 0 matches the initial loader gen, so the result is accepted.
	next, _ := e.Update(core.Result{Generation: 0, Payload: explorerLoadErrMsg{err: loadErr}})
	if next.loadErr == nil {
		t.Fatal("loadErr should be set after explorerLoadErrMsg")
	}

	v := next.View(80, 20)
	for _, want := range []string{"Failed to load", "dial tcp", "retry", "esc"} {
		if !strings.Contains(v, want) {
			t.Errorf("View missing %q\nview=\n%s", want, v)
		}
	}
}

func TestExplorerLoadErrorClearedByBack(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.SetSize(80, 20)
	e.step = stepNamespace
	e.ctx = "ctx-a"
	e.loadErr = errors.New("boom")

	next, _ := e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.loadErr != nil {
		t.Errorf("loadErr = %v, want nil after esc", next.loadErr)
	}
	if next.step != stepContext {
		t.Errorf("step = %v, want stepContext after esc", next.step)
	}
}

func TestExplorerLoadErrorClearedBySuccessfulRetry(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{
		contexts:   []string{"ctx-a"},
		namespaces: []string{"ns-a", "ns-b"},
	}
	e := NewExplorer(mk, nil, s)
	e.SetSize(80, 20)
	e.step = stepNamespace
	e.ctx = "ctx-a"
	e.loadErr = errors.New("transient")

	_, cmd := e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Fatal("retry should produce a non-nil cmd")
	}
	if e.loadErr != nil {
		t.Errorf("loadErr = %v, want nil after retry kick-off", e.loadErr)
	}

	msg := cmd()
	next, _ := e.Update(msg)
	if next.loadErr != nil {
		t.Errorf("loadErr = %v, want nil after successful retry result", next.loadErr)
	}
	if len(next.namespaces) != 2 {
		t.Errorf("namespaces = %v, want 2 items after retry", next.namespaces)
	}
}

func TestExplorerLoadErrorReplacesToast(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.step = stepNamespace
	e.ctx = "ctx-a"

	msg := core.Result{Generation: 0, Payload: explorerLoadErrMsg{err: errors.New("boom")}}
	_, cmd := e.Update(msg)
	if cmd == nil {
		return
	}
	out := cmd()
	if _, isToast := out.(overlays.ToastMsg); isToast {
		t.Errorf("explorerLoadErrMsg should not emit a ToastMsg; got %T", out)
	}
}

func TestExplorerEnterPortFormCustomEmpty(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}

	e.enterPortForm(0, 0)

	if e.step != stepPortForm {
		t.Errorf("step = %v, want stepPortForm", e.step)
	}
	if e.formLocal.Value() != "" {
		t.Errorf("formLocal = %q, want empty (custom)", e.formLocal.Value())
	}
	if e.formRemote.Value() != "" {
		t.Errorf("formRemote = %q, want empty (custom)", e.formRemote.Value())
	}
	if e.formInfo != "" {
		t.Errorf("formInfo = %q, want empty for custom", e.formInfo)
	}
}
