package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/help"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/billygate/kapctl/internal/config"
	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/portfwd"
	"github.com/billygate/kapctl/internal/tui/core"
	"github.com/billygate/kapctl/internal/tui/overlays"
	"github.com/billygate/kapctl/internal/tui/panes"
	"github.com/billygate/kapctl/internal/tui/styles"
	"github.com/billygate/kapctl/internal/tui/themes"
)

// newTestAppModel builds an AppModel with no real kube/docker (nil clients are
// tolerated by NewExplorer/NewLocal respectively).
func newTestAppModel(t *testing.T) *AppModel {
	t.Helper()
	cfg := &config.Config{Theme: "catppuccin", Ports: map[string]int{}}
	palette, ok := themes.Get("catppuccin")
	if !ok {
		t.Fatal("catppuccin theme not registered")
	}
	s := styles.New(palette)

	// Pass a non-nil kubeErr so NewExplorer skips initList (which calls kube.GetContexts).
	kubeErr := errors.New("no kube in tests")

	mgr := portfwd.NewManager(0, 0)
	return &AppModel{
		tabs:        []string{"EXPLORER", "LOCAL", "FORWARDS"},
		activeTab:   0,
		explorer:    panes.NewExplorer(nil, kubeErr, s, cfg),
		local:       panes.NewLocal(nil, s),
		forwards:    panes.NewForwards(mgr, s),
		pfManager:   mgr,
		styles:      s,
		cfg:         cfg,
		footerHelp:  help.New(),
		helpOverlay: &overlays.Help{},
		toasts:      &overlays.Toasts{},
	}
}

func TestAppModelWindowSizeMsg(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	next, _ := m.Update(msg)
	am := next.(*AppModel)
	if am.width != 120 {
		t.Errorf("width = %d, want 120", am.width)
	}
	if am.height != 40 {
		t.Errorf("height = %d, want 40", am.height)
	}
}

func TestAppModelQuitKey(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}
	next, cmd := m.Update(msg)
	am := next.(*AppModel)
	if !am.quitting {
		t.Error("q key should set quitting = true")
	}
	if cmd == nil {
		t.Error("q key should return tea.Quit cmd")
	}
}

func TestAppModelTabNavigation(t *testing.T) {
	m := newTestAppModel(t)
	// "]" → next tab
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")}
	next, _ := m.Update(msg)
	am := next.(*AppModel)
	if am.activeTab != 1 {
		t.Errorf("activeTab = %d after next-tab, want 1", am.activeTab)
	}
	// "[" → prev tab
	msg2 := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")}
	next2, _ := am.Update(msg2)
	am2 := next2.(*AppModel)
	if am2.activeTab != 0 {
		t.Errorf("activeTab = %d after prev-tab, want 0", am2.activeTab)
	}
}

func TestAppModelToastMsg(t *testing.T) {
	m := newTestAppModel(t)
	msg := overlays.ToastMsg{Kind: overlays.ToastError, Text: "boom"}
	next, _ := m.Update(msg)
	am := next.(*AppModel)
	if am.toasts.Empty() {
		t.Error("toasts should not be empty after ToastMsg")
	}
}

func TestAppModelTickMsg(t *testing.T) {
	m := newTestAppModel(t)
	next, cmd := m.Update(core.TickMsg{})
	_ = next
	if cmd == nil {
		t.Error("TickMsg should return a non-nil Cmd (re-arms tick)")
	}
}

func TestAppModelViewQuitting(t *testing.T) {
	m := newTestAppModel(t)
	m.quitting = true
	if v := m.View(); v != "" {
		t.Errorf("View when quitting = %q, want empty", v)
	}
}

func TestAppModelViewNormalTab0(t *testing.T) {
	m := newTestAppModel(t)
	m.width = 100
	m.height = 30
	// Explorer pane has a nil kube client — it renders an error view, but should not panic.
	v := m.View()
	if v == "" {
		t.Error("View should return non-empty string for normal state (tab 0)")
	}
}

func TestAppModelViewTab1(t *testing.T) {
	m := newTestAppModel(t)
	m.activeTab = 1
	m.width = 100
	m.height = 30
	v := m.View()
	if v == "" {
		t.Error("View should return non-empty string for normal state (tab 1)")
	}
}

func TestAppModelHelpToggle(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}
	next, _ := m.Update(msg)
	am := next.(*AppModel)
	if !am.helpOverlay.Visible {
		t.Error("? key should open the help modal")
	}
}

func TestAppModelLocalStatusMsg(t *testing.T) {
	m := newTestAppModel(t)
	// Should not panic; message is forwarded to the local pane.
	_, _ = m.Update(core.LocalStatusMsg([]docker.ContainerStatus{{Name: "kind", Status: "running"}}))
}

func TestAppModelPaneSize(t *testing.T) {
	m := newTestAppModel(t)
	m.width = 120
	m.height = 40
	w, h := m.paneSize()
	if w <= 0 {
		t.Errorf("paneSize width = %d, want > 0", w)
	}
	if h <= 0 {
		t.Errorf("paneSize height = %d, want > 0", h)
	}
}

func TestAppModelHelpModalCloseKeys(t *testing.T) {
	for _, k := range []string{"?", "esc", "q"} {
		t.Run(k, func(t *testing.T) {
			m := newTestAppModel(t)
			m.helpOverlay.Visible = true

			var msg tea.KeyMsg
			if k == "esc" {
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			} else {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
			}
			next, _ := m.Update(msg)
			am := next.(*AppModel)
			if am.helpOverlay.Visible {
				t.Errorf("%q should close the help modal", k)
			}
			if k == "q" && am.quitting {
				t.Error("q while modal is open should NOT quit")
			}
		})
	}
}

func TestAppModelHelpModalAbsorbsOtherKeys(t *testing.T) {
	m := newTestAppModel(t)
	m.helpOverlay.Visible = true
	before := m.activeTab

	// "]" would normally advance the tab.
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")}
	next, _ := m.Update(msg)
	am := next.(*AppModel)

	if am.activeTab != before {
		t.Errorf("tab should not change while modal is open: was %d, now %d", before, am.activeTab)
	}
	if !am.helpOverlay.Visible {
		t.Error("modal should stay open when an absorbed key is pressed")
	}
}

func TestAppModelViewShowsBreadcrumbPlaceholder(t *testing.T) {
	m := newTestAppModel(t)
	m.width = 100
	m.height = 30
	v := m.View()
	if !strings.Contains(v, "(no context selected)") {
		t.Errorf("View should render placeholder breadcrumb, got:\n%s", v)
	}
}
