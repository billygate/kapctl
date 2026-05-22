package panes

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/kube"
	"github.com/billygate/kapctl/internal/portfwd"
	"github.com/billygate/kapctl/internal/tui/core"
	"github.com/billygate/kapctl/internal/tui/overlays"
	"github.com/billygate/kapctl/internal/tui/styles"
	"github.com/billygate/kapctl/internal/tui/themes"
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

// withFakeKubeClient swaps the package-level newKubeClient seam so
// tests don't depend on a real kubeconfig. Cleanup is registered via
// t.Cleanup — callers do not need to defer anything.
func withFakeKubeClient(t *testing.T, _ core.KubeClient) {
	t.Helper()
	orig := newKubeClient
	newKubeClient = func(_ string) (*kube.Client, error) {
		// We can't return a *kube.Client of our own — but tests don't need
		// the typed value, only that the call succeeds and the Explorer
		// then proceeds with the new client. The Explorer.SetKubeClient
		// path assigns the result to e.kube (which is core.KubeClient),
		// so we return a zero-value client and rely on later tests not
		// to call into it. The auto-resume tests assert only state
		// transitions (step) and selection, which do not require the
		// new client to actually work.
		return &kube.Client{}, nil
	}
	t.Cleanup(func() { newKubeClient = orig })
}

// ── Explorer pane tests ──────────────────────────────────────────────────────

func TestExplorerInitReturnsNilCmd(t *testing.T) {
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s, nil)
	if cmd := e.Init(); cmd != nil {
		t.Error("Explorer.Init() should return nil when kube err is set")
	}
}

func TestExplorerSetSize(t *testing.T) {
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s, nil)
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
	e := NewExplorer(nil, errors.New("kubeconfig not found"), s, nil)
	v := e.View(80, 20)
	if v == "" {
		t.Error("View should render error message when kube error is set")
	}
}

func TestExplorerUpdateWindowSize(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s, nil)
	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	next, _ := e.Update(msg)
	_ = next
}

func TestExplorerUpdateBackKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s, nil)
	// Back key on step 0 (context) is a no-op
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	next, _ := e.Update(msg)
	_ = next
}

func TestExplorerUpdateKeyForwardsToList(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s, nil)
	msg := tea.KeyMsg{Type: tea.KeyDown}
	_, _ = e.Update(msg)
}

func TestExplorerUpdateSelectOnError(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	e := NewExplorer(nil, errors.New("no kube"), s, nil)
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	_, _ = e.Update(msg)
}

func TestExplorerWithMockKubeInitList(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{
		contexts:       []string{"ctx-a", "ctx-b"},
		currentContext: "ctx-a",
	}
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
	e.SetSize(120, 40)
	if e.width != 120 || e.height != 40 {
		t.Errorf("SetSize failed: width=%d height=%d", e.width, e.height)
	}
}

func TestExplorerViewWithMockKube(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a", "ctx-b"}}
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")}
	_, _ = e.Update(msg)
}

func TestExplorerUpdateNumericKey(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a", "ctx-b", "ctx-c"}}
	e := NewExplorer(mk, nil, s, nil)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}
	_, _ = e.Update(msg)
}

func TestExplorerUpdateEscBack(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
	// Gen=0 matches initial loader gen, so it's accepted
	msg := core.Result{Generation: 0, Payload: namespacesLoadedMsg{items: []string{"ns-1", "ns-2"}}}
	next, _ := e.Update(msg)
	_ = next
}

func TestExplorerUpdateNonKeyMsg(t *testing.T) {
	t.Helper()
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s, nil)
	// Non-key, non-result message should be forwarded to the list
	msg := tea.WindowSizeMsg{Width: 80, Height: 20}
	_, _ = e.Update(msg)
}

func TestExplorerInitListStepAction(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(nil, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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
	e := NewExplorer(mk, nil, s, nil)
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

// fakeResume is a ResumeStore for tests that records writes.
type fakeResume struct {
	ctx       string
	ns        string
	saveCalls int
	saveErr   error
}

func (f *fakeResume) LastContext() string        { return f.ctx }
func (f *fakeResume) LastNamespace() string      { return f.ns }
func (f *fakeResume) SetLastContext(ctx string)  { f.ctx = ctx }
func (f *fakeResume) SetLastNamespace(ns string) { f.ns = ns }
func (f *fakeResume) Save() error                { f.saveCalls++; return f.saveErr }

func TestExplorerPersistsContextOnSelect(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha", "beta"}}
	r := &fakeResume{ns: "leftover-ns"} // pre-existing ns to confirm it gets cleared
	e := NewExplorer(mk, nil, s, r)
	e.SetSize(80, 24)
	e.list.Select(0)

	// Pressing Enter on stepContext calls kube.NewClient(val), which may
	// fail in CI without a real kubeconfig — that's fine. Persist happens
	// before the client switch, so the assertion is independent of the
	// outcome of the connection attempt.
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	e.Update(enter)

	if r.ctx != "alpha" && r.ctx != "beta" {
		t.Errorf("ResumeStore.SetLastContext was not called with a known ctx, got %q", r.ctx)
	}
	if r.ns != "" {
		t.Errorf("ResumeStore.SetLastNamespace should clear when ctx changes, got %q", r.ns)
	}
}

func TestExplorerAutoResumeContextOnly(t *testing.T) {
	s := newTestStyles()
	r := &fakeResume{ctx: "alpha"}
	mk := &mockKubeClient{contexts: []string{"alpha", "beta"}, namespaces: []string{"x", "y"}}

	withFakeKubeClient(t, mk)
	// Construct in the lazy state (no kube yet), then deliver the client.
	e := NewExplorer(nil, nil, s, r)
	e.SetSize(80, 24)
	_ = e.SetKubeClient(mk, nil)

	if got := e.step; got != stepNamespace {
		t.Errorf("step = %v, want stepNamespace after auto-resume with ctx only", got)
	}
	if got, _ := e.Selection(); got != "alpha" {
		t.Errorf("Selection ctx = %q, want alpha", got)
	}
}

func TestExplorerAutoResumeContextAndNamespace(t *testing.T) {
	s := newTestStyles()
	r := &fakeResume{ctx: "alpha", ns: "payments"}
	mk := &mockKubeClient{contexts: []string{"alpha"}, pods: []kube.PodInfo{}}

	withFakeKubeClient(t, mk)
	e := NewExplorer(nil, nil, s, r)
	e.SetSize(80, 24)
	_ = e.SetKubeClient(mk, nil)

	if got := e.step; got != stepPod {
		t.Errorf("step = %v, want stepPod after auto-resume with ctx+ns", got)
	}
	ctx, ns := e.Selection()
	if ctx != "alpha" || ns != "payments" {
		t.Errorf("Selection = (%q,%q), want (alpha,payments)", ctx, ns)
	}
}

func TestExplorerAutoResumeUnknownContextClears(t *testing.T) {
	s := newTestStyles()
	r := &fakeResume{ctx: "gone", ns: "obsolete"}
	mk := &mockKubeClient{contexts: []string{"alpha"}}

	e := NewExplorer(nil, nil, s, r)
	e.SetSize(80, 24)
	_ = e.SetKubeClient(mk, nil)

	if r.ctx != "" || r.ns != "" {
		t.Errorf("ResumeStore should be cleared, got ctx=%q ns=%q", r.ctx, r.ns)
	}
	if e.step != stepContext {
		t.Errorf("step = %v, want stepContext when saved ctx is unknown", e.step)
	}
}

func TestExplorerAutoResumeNoStateStaysOnContext(t *testing.T) {
	s := newTestStyles()
	r := &fakeResume{}
	mk := &mockKubeClient{contexts: []string{"alpha"}}

	e := NewExplorer(nil, nil, s, r)
	e.SetSize(80, 24)
	_ = e.SetKubeClient(mk, nil)

	if e.step != stepContext {
		t.Errorf("step = %v, want stepContext when no saved state", e.step)
	}
}

func TestExplorerBackClearsNsFromPodStep(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha"}, namespaces: []string{"payments"}}
	e := NewExplorer(mk, nil, s, nil)
	e.SetSize(80, 24)
	e.step = stepPod
	e.ctx = "alpha"
	e.ns = "payments"
	e.initView(80, 24)

	next, _ := e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ctx, ns := next.Selection()
	if ns != "" {
		t.Errorf("ns = %q after esc from stepPod, want empty", ns)
	}
	if ctx != "alpha" {
		t.Errorf("ctx = %q after esc from stepPod, want alpha (preserved)", ctx)
	}
}

func TestExplorerBackClearsCtxFromNamespaceStep(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha", "beta"}}
	e := NewExplorer(mk, nil, s, nil)
	e.SetSize(80, 24)
	e.step = stepNamespace
	e.ctx = "alpha"
	e.initView(80, 24)

	next, _ := e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ctx, _ := next.Selection()
	if ctx != "" {
		t.Errorf("ctx = %q after esc from stepNamespace, want empty", ctx)
	}
}

func TestExplorerJumpContextFromPodStep(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha"}, namespaces: []string{"payments"}}
	e := NewExplorer(mk, nil, s, nil)
	e.SetSize(80, 24)
	e.step = stepPod
	e.ctx = "alpha"
	e.ns = "payments"
	e.initView(80, 24)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")}
	next, _ := e.Update(msg)

	if next.step != stepContext {
		t.Errorf("step = %v after c, want stepContext", next.step)
	}
	ctx, ns := next.Selection()
	if ctx != "" || ns != "" {
		t.Errorf("Selection = (%q,%q) after jump to context, want empty/empty", ctx, ns)
	}
}

func TestExplorerJumpNamespaceFromPodStep(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha"}, namespaces: []string{"payments", "infra"}}
	e := NewExplorer(mk, nil, s, nil)
	e.SetSize(80, 24)
	e.step = stepPod
	e.ctx = "alpha"
	e.ns = "payments"
	e.namespaces = []string{"payments", "infra"} // cached from previous load
	e.initView(80, 24)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
	next, _ := e.Update(msg)

	if next.step != stepNamespace {
		t.Errorf("step = %v after n, want stepNamespace", next.step)
	}
	ctx, ns := next.Selection()
	if ctx != "alpha" {
		t.Errorf("ctx = %q after jump to namespace, want alpha (preserved)", ctx)
	}
	if ns != "" {
		t.Errorf("ns = %q after jump to namespace, want empty", ns)
	}
}

func TestExplorerJumpNamespaceWithoutContextIsNoop(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha"}}
	e := NewExplorer(mk, nil, s, nil)
	e.SetSize(80, 24)
	// step == stepContext, ctx == "" (no selection yet)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
	next, _ := e.Update(msg)

	if next.step != stepContext {
		t.Errorf("step = %v after n with no ctx, want stepContext (unchanged)", next.step)
	}
}

func TestExplorerJumpKeysSilentInFilterMode(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha", "beta"}, namespaces: []string{"x"}}
	e := NewExplorer(mk, nil, s, nil)
	e.SetSize(80, 24)
	e.step = stepNamespace
	e.ctx = "alpha"
	e.namespaces = []string{"x"}
	e.initView(80, 24)

	// Enter filter mode, then type "c" — must not warp to stepContext.
	e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	next, _ := e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if next.step != stepNamespace {
		t.Errorf("c during filter should not jump; step = %v, want stepNamespace", next.step)
	}
}

func TestExplorerBackDoesNotTouchResumeStore(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"alpha"}, namespaces: []string{"payments"}}
	r := &fakeResume{ctx: "alpha", ns: "payments"}
	e := NewExplorer(mk, nil, s, r)
	e.SetSize(80, 24)
	e.step = stepPod
	e.ctx = "alpha"
	e.ns = "payments"
	e.initView(80, 24)

	e.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if r.ctx != "alpha" || r.ns != "payments" {
		t.Errorf("Back should not touch ResumeStore, got ctx=%q ns=%q (want alpha/payments)", r.ctx, r.ns)
	}
}

func TestForwardsRendersReconnectingWithCountdown(t *testing.T) {
	now := time.Now()
	snap := portfwd.Snapshot{
		ID: "1", LocalPort: 5432, Target: "pg-0", Namespace: "ns",
		Kind: portfwd.KindPod, Status: portfwd.StatusReconnecting,
		StartedAt:          now.Add(-30 * time.Second),
		Attempts:           2,
		ReconnectStartedAt: now.Add(-30 * time.Second),
	}
	row := NewFwdRowForTest(snap, now)
	cells := row.Cells()
	if cells[3] != "reconnecting 2/90s" {
		t.Errorf("status cell = %q, want %q", cells[3], "reconnecting 2/90s")
	}
}

func TestForwardsRendersRunningPlain(t *testing.T) {
	now := time.Now()
	snap := portfwd.Snapshot{
		ID: "1", LocalPort: 5432, Target: "pg-0", Namespace: "ns",
		Kind: portfwd.KindPod, Status: portfwd.StatusRunning,
		StartedAt: now.Add(-30 * time.Second),
	}
	row := NewFwdRowForTest(snap, now)
	cells := row.Cells()
	if cells[3] != "running" {
		t.Errorf("status cell = %q, want %q", cells[3], "running")
	}
}
