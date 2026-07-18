// Package core holds the small set of shared primitives used by both the
// root tui package and its sibling sub-packages (panes, overlays, etc.).
//
// Anything here must compile without importing tui — that's the whole
// point: putting it here breaks the import cycle between tui (which owns
// the root model and imports panes) and panes (which need access to keys,
// the item delegate, the kube/docker interfaces, and shared messages).
package core

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/kube"
	"github.com/billygate/kapctl/internal/portfwd"
	"github.com/billygate/kapctl/internal/tui/styles"
)

// ── Client interfaces ───────────────────────────────────────────────────

// KubeClient is the surface the TUI uses against a Kubernetes cluster.
// Satisfied by *kube.Client.
type KubeClient interface {
	GetContexts() []string
	GetCurrentContext() string
	GetNamespaces(ctx context.Context) ([]string, error)
	GetPods(ctx context.Context, namespace string) ([]kube.PodInfo, error)
	GetPodPorts(ctx context.Context, namespace, podName string) ([]kube.ContainerPort, error)
	GetPodRole(ctx context.Context, namespace, podName string) (string, error)
	DeletePod(ctx context.Context, namespace, podName string) error
}

// DockerClient is the surface the TUI uses against the local Docker daemon.
// Satisfied by *docker.Client.
type DockerClient interface {
	GetKindContainers(ctx context.Context, state string) ([]string, error)
	PauseContainers(ctx context.Context, names []string) error
	ResumeContainers(ctx context.Context, names []string) error
	RestartContainers(ctx context.Context, names []string) error
	GetStatus(ctx context.Context) ([]docker.ContainerStatus, error)
}

// Compile-time assertions.
var (
	_ KubeClient   = (*kube.Client)(nil)
	_ DockerClient = (*docker.Client)(nil)
)

// ── Messages ────────────────────────────────────────────────────────────

// LocalStatusMsg carries the latest structured container status for the Local pane.
type LocalStatusMsg []docker.ContainerStatus

// TickMsg is the periodic refresh trigger for the Local pane.
type TickMsg struct{}

// Tick returns the command that schedules the next TickMsg.
func Tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(_ time.Time) tea.Msg {
		return TickMsg{}
	})
}

// KubeReadyMsg is delivered when an async kube.NewClient call completes.
// Either Client or Err is set; the other is zero.
type KubeReadyMsg struct {
	Client *kube.Client
	Err    error
}

// DockerReadyMsg is delivered when an async docker.NewClient call completes.
// Client may be nil if construction failed; that case is non-fatal (the
// Local pane simply continues to shell out for actions).
type DockerReadyMsg struct {
	Client *docker.Client
}

// LoadKube returns a tea.Cmd that constructs a kube.Client off the
// startup synchronous path. The TUI renders immediately and the panes
// receive a KubeReadyMsg when this completes.
func LoadKube(contextName string) tea.Cmd {
	return func() tea.Msg {
		c, err := kube.NewClient(contextName)
		return KubeReadyMsg{Client: c, Err: err}
	}
}

// LoadDocker returns a tea.Cmd that constructs a docker.Client off the
// startup synchronous path.
func LoadDocker() tea.Cmd {
	return func() tea.Msg {
		c, _ := docker.NewClient()
		return DockerReadyMsg{Client: c}
	}
}

// PortForwardRequestMsg is emitted by panes that want to start a
// background port-forward. AppModel routes it to its *portfwd.Manager.
type PortForwardRequestMsg struct {
	Context    string
	Namespace  string
	Target     string // bare resource name
	Kind       portfwd.Kind
	LocalPort  int
	RemotePort int
}

// PortForwardEventMsg is the tea.Msg flavour of portfwd.Event — every
// status transition is delivered through this so the FORWARDS pane
// can re-render and Toasts can be raised.
type PortForwardEventMsg portfwd.Event

// DrainPortForwardEvents returns a tea.Cmd that blocks on the manager's
// event channel and produces a PortForwardEventMsg. Call it again from
// the model after handling each event to keep the drain alive.
func DrainPortForwardEvents(mgr *portfwd.Manager) tea.Cmd {
	if mgr == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-mgr.Events()
		if !ok {
			return nil
		}
		return PortForwardEventMsg(ev)
	}
}

// ── Key map ─────────────────────────────────────────────────────────────

// KeyMap is the global keybinding table consumed by panes and the
// help footer.
type KeyMap struct {
	Select        key.Binding
	Filter        key.Binding
	NextTab       key.Binding
	PrevTab       key.Binding
	Back          key.Binding
	Retry         key.Binding
	Help          key.Binding
	Quit          key.Binding
	JumpContext   key.Binding
	JumpNamespace key.Binding
}

// ShortHelp implements the help.KeyMap interface.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Select, k.Filter, k.NextTab, k.Help, k.Quit}
}

// FullHelp implements the help.KeyMap interface.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Select, k.Filter, k.Back, k.Retry},
		{k.NextTab, k.PrevTab},
		{k.Help, k.Quit},
	}
}

// Keys is the global key map.
var Keys = KeyMap{
	Select: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Filter: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter"),
	),
	NextTab: key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "next tab"),
	),
	PrevTab: key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "prev tab"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	),
	Retry: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "retry load"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	JumpContext: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "jump to context step"),
	),
	JumpNamespace: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "jump to namespace step"),
	),
}

// ── List items ──────────────────────────────────────────────────────────

type itemKind int

const (
	itemNormal itemKind = iota
	itemSeparator
)

// ListItem is a normal or separator entry in a bubbles/list.
type ListItem struct {
	Text string
	kind itemKind
}

// FilterValue implements list.Item: separators have no filter text.
func (i ListItem) FilterValue() string {
	if i.kind == itemSeparator {
		return ""
	}
	return i.Text
}

// IsSeparator reports whether this entry is a non-selectable section header.
func (i ListItem) IsSeparator() bool { return i.kind == itemSeparator }

// Item builds a normal selectable entry.
func Item(s string) ListItem { return ListItem{Text: s, kind: itemNormal} }

// Separator builds a non-selectable section heading.
func Separator(s string) ListItem { return ListItem{Text: s, kind: itemSeparator} }

// AsListItem narrows a bubbles list.Item to a ListItem if possible.
func AsListItem(it list.Item) (ListItem, bool) {
	li, ok := it.(ListItem)
	return li, ok
}

// ── Item delegate ───────────────────────────────────────────────────────

type itemDelegate struct {
	styles *styles.Styles
}

func (d itemDelegate) Height() int  { return 1 }
func (d itemDelegate) Spacing() int { return 0 }

func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	// Skip separators when navigating.
	i, ok := m.SelectedItem().(ListItem)
	if ok && i.kind == itemSeparator {
		if msg, ok := msg.(tea.KeyMsg); ok {
			switch msg.String() {
			case "up", "k":
				m.CursorUp()
			case "down", "j":
				m.CursorDown()
			}
		}
	}
	return nil
}

func (d itemDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
	var str string
	var isSeparator bool

	if i, ok := it.(ListItem); ok {
		str = i.Text
		isSeparator = i.kind == itemSeparator
	} else if i, ok := it.(interface{ FilterValue() string }); ok {
		str = i.FilterValue()
	} else {
		return
	}

	if isSeparator {
		var sepStyle lipgloss.Style
		if d.styles != nil {
			sepStyle = d.styles.Separator
		} else {
			sepStyle = lipgloss.NewStyle().Faint(true)
		}
		_, _ = fmt.Fprint(w, sepStyle.Render("  "+str))
		return
	}

	// Find display index for numeric labels (excluding separators).
	displayIdx := 0
	for j := 0; j <= index; j++ {
		itemIt := m.Items()[j]
		if li, ok := itemIt.(ListItem); ok {
			if li.kind == itemNormal {
				displayIdx++
			}
		} else {
			displayIdx++
		}
	}
	idxStr := fmt.Sprintf("%d ", displayIdx)

	if d.styles != nil {
		if index == m.Index() {
			_, _ = fmt.Fprint(w, d.styles.SelectedItem.Render("❯ "+idxStr+str))
		} else {
			_, _ = fmt.Fprint(w, d.styles.Item.Render("  "+idxStr+str))
		}
	} else {
		if index == m.Index() {
			_, _ = fmt.Fprint(w, lipgloss.NewStyle().Bold(true).Render("❯ "+idxStr+str))
		} else {
			_, _ = fmt.Fprint(w, "  "+idxStr+str)
		}
	}
}

// NewItemDelegate returns the shared item delegate used by all panes.
func NewItemDelegate(s *styles.Styles) list.ItemDelegate {
	return itemDelegate{styles: s}
}

// ── Layout helpers ──────────────────────────────────────────────────────

// ShouldShowTwoColumns is true when the list is long enough and narrow enough
// per item that a two-column layout is worth showing.
func ShouldShowTwoColumns(width int, items []list.Item) bool {
	if len(items) < 10 {
		return false
	}
	maxW := 0
	for _, it := range items {
		if w := lipgloss.Width(it.FilterValue()); w > maxW {
			maxW = w
		}
	}
	// Column: "❯ 123 pod-name-here  " -> approx maxW + 10.
	return (maxW+10)*2 < width-10
}

// RenderTwoColumns renders the list as two side-by-side columns.
func RenderTwoColumns(l list.Model, width, height int) string {
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetSize(width, height*2)

	view := l.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var col1, col2 []string
	for i := 0; i < height && i < len(lines); i++ {
		col1 = append(col1, lines[i])
	}
	for i := height; i < 2*height && i < len(lines); i++ {
		col2 = append(col2, lines[i])
	}

	for len(col1) < height {
		col1 = append(col1, "")
	}
	for len(col2) < height {
		col2 = append(col2, "")
	}

	colWidth := width / 2
	style := lipgloss.NewStyle().Width(colWidth).MaxWidth(colWidth)

	col1View := style.Render(lipgloss.JoinVertical(lipgloss.Left, col1...))
	col2View := style.Render(lipgloss.JoinVertical(lipgloss.Left, col2...))

	return lipgloss.JoinHorizontal(lipgloss.Top, col1View, col2View)
}
