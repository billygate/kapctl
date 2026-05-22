// Package tui hosts the root tabbed Bubble Tea program (EXPLORER + LOCAL)
// and the `RunApp` entrypoint invoked from the kapctl CLI.
package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kap-toolsbox/internal/config"
	"github.com/billygate/kap-toolsbox/internal/kube"
	"github.com/billygate/kap-toolsbox/internal/portfwd"
	"github.com/billygate/kap-toolsbox/internal/spacebox"
	"github.com/billygate/kap-toolsbox/internal/tui/core"
	"github.com/billygate/kap-toolsbox/internal/tui/overlays"
	"github.com/billygate/kap-toolsbox/internal/tui/panes"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
	"github.com/billygate/kap-toolsbox/internal/tui/themes"
)

// AppModel holds top-level state: which tab is active, the panes,
// terminal dimensions, help, the shared styles, and the port-forward
// manager whose lifecycle is bound to the TUI. Per-pane state (lists,
// filters, numeric jump, kube/docker handles) lives inside the panes.
type AppModel struct {
	tabs        []string
	activeTab   int
	explorer    *panes.Explorer
	local       *panes.Local
	forwards    *panes.Forwards
	pfManager   *portfwd.Manager
	width       int
	height      int
	quitting    bool
	footerHelp  help.Model
	helpOverlay *overlays.Help
	styles      *styles.Styles
	cfg         *config.Config
	toasts      *overlays.Toasts
}

// NewAppModel wires up the palette/styles and the two panes. Kube and
// docker clients are NOT constructed here — they are loaded
// asynchronously from Init() so the TUI renders immediately on startup
// while clients spin up in the background.
func NewAppModel(cfg *config.Config) (*AppModel, error) {
	palette, ok := themes.Get(cfg.Theme)
	if !ok {
		palette, _ = themes.Get("catppuccin")
	}
	s := styles.New(palette)

	mgr := portfwd.NewManager(0, 0)
	mgr.SetProberFactory(func(contextName string) (portfwd.Prober, error) {
		return kube.NewClient(contextName)
	})

	// LOCAL tab is gated on the spacebox CLI being on PATH. Without it
	// the cluster-lifecycle actions in the pane have no backend, so we
	// drop the tab entirely rather than show a half-working surface.
	tabs := []string{"EXPLORER"}
	if spacebox.IsInstalled() {
		tabs = append(tabs, "LOCAL")
	}
	tabs = append(tabs, "FORWARDS")

	return &AppModel{
		tabs:        tabs,
		activeTab:   0,
		explorer:    panes.NewExplorer(nil, nil, s, cfg),
		local:       panes.NewLocal(nil, s),
		forwards:    panes.NewForwards(mgr, s),
		pfManager:   mgr,
		footerHelp:  help.New(),
		helpOverlay: &overlays.Help{},
		cfg:         cfg,
		styles:      s,
		toasts:      &overlays.Toasts{},
	}, nil
}

// Init dispatches the async client loaders, the per-pane Init cmds,
// and the periodic Tick. Returning here means the TUI renders before
// any kube/docker work completes.
func (m *AppModel) Init() tea.Cmd {
	return tea.Batch(
		core.LoadKube(""),
		core.LoadDocker(),
		m.explorer.Init(),
		m.local.Init(),
		m.forwards.Init(),
		core.DrainPortForwardEvents(m.pfManager),
		core.Tick(),
	)
}

// Update routes messages to the active pane, with two cross-cutting
// concerns: tab/help/quit keys and ToastMsg/ready-msg fan-out.
func (m *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		paneW, paneH := m.paneSize()
		m.footerHelp.Width = paneW
		m.explorer.SetSize(paneW, paneH)
		m.local.SetSize(paneW, paneH)
		m.forwards.SetSize(paneW, paneH)
		return m, nil

	case tea.KeyMsg:
		// While the help modal is open, intercept all keys. ?/esc/q
		// close it; everything else is absorbed so the pane underneath
		// doesn't react. ctrl+c still quits (handled separately because
		// the modal-close branch above absorbs the literal "q" rune).
		if m.helpOverlay.Visible {
			switch msg.String() {
			case "?", "esc", "q":
				m.helpOverlay.Visible = false
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, core.Keys.Quit):
			m.quitting = true
			return m, tea.Quit
		case key.Matches(msg, core.Keys.NextTab):
			m.activeTab = (m.activeTab + 1) % len(m.tabs)
			return m, nil
		case key.Matches(msg, core.Keys.PrevTab):
			m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
			return m, nil
		case key.Matches(msg, core.Keys.Help):
			m.helpOverlay.Visible = true
			return m, nil
		}

		var cmd tea.Cmd
		switch m.tabs[m.activeTab] {
		case "EXPLORER":
			m.explorer, cmd = m.explorer.Update(msg)
		case "LOCAL":
			m.local, cmd = m.local.Update(msg)
		case "FORWARDS":
			m.forwards, cmd = m.forwards.Update(msg)
		}
		return m, cmd

	case core.TickMsg:
		// Re-arm the tick, advance toasts, and forward to all panes.
		m.toasts.Tick(time.Now())
		var c1, c2, c3 tea.Cmd
		m.explorer, c1 = m.explorer.Update(msg)
		m.local, c2 = m.local.Update(msg)
		m.forwards, c3 = m.forwards.Update(msg)
		return m, tea.Batch(c1, c2, c3, core.Tick())

	case overlays.ToastMsg:
		m.toasts.Push(msg.Kind, msg.Text)
		return m, nil

	case core.LocalStatusMsg:
		var cmd tea.Cmd
		m.local, cmd = m.local.Update(msg)
		return m, cmd

	case core.KubeReadyMsg:
		cmd := m.explorer.SetKubeClient(msg.Client, msg.Err)
		return m, cmd

	case core.DockerReadyMsg:
		cmd := m.local.SetDockerClient(msg.Client)
		return m, cmd

	case core.PortForwardRequestMsg:
		// Start the forward and emit a toast for the immediate result.
		// Successful starts also produce a Running event later via the
		// drain Cmd, which the Forwards pane lifts to a success toast.
		if _, err := m.pfManager.Start(portfwd.StartOpts{
			Context: msg.Context, Namespace: msg.Namespace, Target: msg.Target,
			Kind: msg.Kind, LocalPort: msg.LocalPort, RemotePort: msg.RemotePort,
		}); err != nil {
			return m, func() tea.Msg {
				return overlays.ToastMsg{Kind: overlays.ToastError, Text: err.Error()}
			}
		}
		// Auto-switch to FORWARDS so the user sees the new entry land
		// in the table — the toast alone is too easy to miss.
		m.activeTab = m.tabIndex("FORWARDS")
		var cmd tea.Cmd
		m.forwards, cmd = m.forwards.Update(core.TickMsg{}) // refresh table
		toastText := "starting port-forward " + msg.Kind.Prefix() + msg.Target +
			" :" + strconv.Itoa(msg.LocalPort) + "→" + strconv.Itoa(msg.RemotePort)
		return m, tea.Batch(cmd, func() tea.Msg {
			return overlays.ToastMsg{Kind: overlays.ToastInfo, Text: toastText}
		})

	case core.PortForwardEventMsg:
		// Forward the event to the FORWARDS pane and re-arm the drain.
		var cmd tea.Cmd
		m.forwards, cmd = m.forwards.Update(msg)
		return m, tea.Batch(cmd, core.DrainPortForwardEvents(m.pfManager))

	default:
		// Forward unknown messages to all panes; panes ignore what they
		// do not recognize.
		var c1, c2, c3 tea.Cmd
		m.explorer, c1 = m.explorer.Update(msg)
		m.local, c2 = m.local.Update(msg)
		m.forwards, c3 = m.forwards.Update(msg)
		return m, tea.Batch(c1, c2, c3)
	}
}

// tabIndex returns the index of the named tab, or 0 if not found.
func (m *AppModel) tabIndex(name string) int {
	for i, t := range m.tabs {
		if t == name {
			return i
		}
	}
	return 0
}

// activeForwardCount returns the number of running or starting
// forwards — the count we surface as a tab badge.
func (m *AppModel) activeForwardCount() int {
	if m.pfManager == nil {
		return 0
	}
	n := 0
	for _, s := range m.pfManager.List() {
		if s.Status == portfwd.StatusRunning || s.Status == portfwd.StatusStarting {
			n++
		}
	}
	return n
}

// paneSize is the inner content area available to a pane: terminal size
// minus the window border/padding minus the tab row + divider + footer.
func (m *AppModel) paneSize() (int, int) {
	h, v := m.styles.Window.GetFrameSize()
	// 6 = tab row + divider + footer line + spacing (as before).
	// 2 more = breadcrumb row + secondary divider.
	return m.width - h, m.height - v - 8
}

// View renders the tab strip, the active pane, the help footer, and
// any active toasts inside the rounded window border.
func (m *AppModel) View() string {
	if m.quitting || m.width <= 0 || m.height <= 0 {
		return ""
	}

	// When the help modal is open, skip rendering the pane underneath —
	// it would be discarded below anyway. Pane state is preserved in the
	// model; keys are absorbed in Update.
	if m.helpOverlay.Visible {
		return m.helpOverlay.View(m.width, m.height, m.styles)
	}

	// Tab row. The FORWARDS label carries a live badge of the number
	// of running/starting forwards so the user has a persistent
	// indicator regardless of which tab is focused.
	fwdActive := m.activeForwardCount()
	var tabs []string
	for i, t := range m.tabs {
		label := t
		if t == "FORWARDS" && fwdActive > 0 {
			label = t + "·" + strconv.Itoa(fwdActive)
		}
		if i == m.activeTab {
			tabs = append(tabs, m.styles.ActiveTab.Render(label))
		} else {
			tabs = append(tabs, m.styles.InactiveTab.Render(label))
		}
	}
	paneW, paneH := m.paneSize()

	tabRow := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	divider := lipgloss.NewStyle().
		Border(lipgloss.Border{Bottom: "─"}, false, false, true, false).
		BorderForeground(m.styles.Palette.Surface()).
		Width(paneW).
		Render("")
	ctx, ns := m.explorer.Selection()
	breadcrumb := renderBreadcrumb(ctx, ns, m.styles)
	divider2 := lipgloss.NewStyle().
		Border(lipgloss.Border{Bottom: "─"}, false, false, true, false).
		BorderForeground(m.styles.Palette.Surface()).
		Width(paneW).
		Render("")
	header := lipgloss.JoinVertical(lipgloss.Left, tabRow, divider, breadcrumb, divider2)

	var content string
	switch m.tabs[m.activeTab] {
	case "EXPLORER":
		content = m.explorer.View(paneW, paneH)
	case "LOCAL":
		content = m.local.View(paneW, paneH)
	case "FORWARDS":
		content = m.forwards.View(paneW, paneH)
	}

	helpView := m.styles.Footer.Render(m.footerHelp.View(core.Keys))

	toastBlock := m.toasts.View(paneW, m.styles)
	sections := []string{header, content, helpView}
	if toastBlock != "" {
		sections = append(sections, toastBlock)
	}

	joined := lipgloss.JoinVertical(lipgloss.Left, sections...)

	// Truncate to inner height to ensure content doesn't push the bottom border.
	_, vFrame := m.styles.Window.GetFrameSize()
	innerH := m.height - vFrame
	lines := strings.Split(joined, "\n")
	if len(lines) > innerH {
		joined = strings.Join(lines[:innerH], "\n")
	}

	return m.styles.Window.
		Width(m.width - 2).
		Height(m.height - 2).
		Render(joined)
}

// renderBreadcrumb formats the "ctx: X  •  ns: Y" line shown above the
// active pane. Returns a muted placeholder when nothing has been
// selected yet.
func renderBreadcrumb(ctx, ns string, s *styles.Styles) string {
	if ctx == "" && ns == "" {
		return "  " + s.Muted.Render("(no context selected)")
	}
	var parts []string
	if ctx != "" {
		parts = append(parts, s.Muted.Render("ctx: ")+ctx)
	}
	if ns != "" {
		parts = append(parts, s.Muted.Render("ns: ")+ns)
	}
	return "  " + strings.Join(parts, "  •  ")
}

// RunApp starts the Bubble Tea program for the given config and stops
// every running port-forward on exit so background kubectl processes
// don't leak.
func RunApp(cfg *config.Config) error {
	m, err := NewAppModel(cfg)
	if err != nil {
		return err
	}
	defer m.pfManager.StopAll(3 * time.Second)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
