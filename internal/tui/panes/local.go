package panes

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kapctl/internal/docker"
	"github.com/billygate/kapctl/internal/tui/core"
	"github.com/billygate/kapctl/internal/tui/overlays"
	"github.com/billygate/kapctl/internal/tui/styles"
)

// statusRow adapts docker.ContainerStatus to core.RowProvider so the
// status panel can be rendered through core.Table.
type statusRow struct{ docker.ContainerStatus }

func (s statusRow) Cells() table.Row    { return table.Row{s.Name, s.Status, s.Age} }
func (s statusRow) FilterValue() string { return s.Name }

// Local is the local-cluster control pane: a top status table (read-only,
// auto-refreshed via Loader) and a bottom action list (up/down/pause/resume).
//
// The docker client is supplied asynchronously: NewLocal can be called
// with a nil client; AppModel later calls SetDockerClient when the
// async core.LoadDocker completes. While the client is nil, the pane
// shows "Connecting to Docker…" instead of "No kind containers found".
type Local struct {
	list         list.Model // actions menu
	statusTable  *core.Table
	status       []docker.ContainerStatus
	statusLoaded bool
	docker       core.DockerClient
	styles       *styles.Styles
	width        int
	height       int
	inputBuf     string
	loader       core.Loader
}

// NewLocal builds the Local pane. The docker client may be nil; the
// pane will render a "Connecting…" placeholder until SetDockerClient
// is called with a live client.
func NewLocal(d core.DockerClient, s *styles.Styles) *Local {
	l := &Local{
		docker: d,
		styles: s,
		statusTable: core.NewTable(s, []table.Column{
			{Title: "NAME", Width: 30},
			{Title: "STATUS", Width: 24},
			{Title: "AGE", Width: 12},
		}),
	}
	l.initList(0, 0)
	return l
}

// Init returns the first status fetch command, or nil if the docker
// client hasn't been wired in yet.
func (l *Local) Init() tea.Cmd {
	if l.docker == nil {
		// Defer status fetch until SetDockerClient lands.
		return nil
	}
	return l.fetchStatus()
}

// SetDockerClient wires the docker client in asynchronously. Called by
// the root model when core.LoadDocker completes. Returns the tea.Cmd
// that performs the first status fetch.
func (l *Local) SetDockerClient(d core.DockerClient) tea.Cmd {
	l.docker = d
	if d == nil {
		return nil
	}
	return l.fetchStatus()
}

// fetchStatus is the Loader-wrapped GetStatus call shared by Init,
// SetDockerClient, and the Tick refresh. The client is snapshotted
// outside the goroutine so a concurrent SetDockerClient cannot make
// the closure observe a different value.
func (l *Local) fetchStatus() tea.Cmd {
	d := l.docker
	return l.loader.Start(context.Background(), func(ctx context.Context) tea.Msg {
		if d == nil {
			return core.LocalStatusMsg(nil)
		}
		stats, err := d.GetStatus(ctx)
		if err != nil {
			return overlays.ToastMsg{Kind: overlays.ToastError, Text: fmt.Sprintf("docker status: %v", err)}
		}
		return core.LocalStatusMsg(stats)
	})
}

// SetSize lays out the action list and status table within the
// available pane height (status capped to half the pane).
func (l *Local) SetSize(w, h int) {
	l.width, l.height = w, h
	l.list.SetSize(w, max(5, h-12))
	// Status table height: header + rows, capped so the actions list keeps room.
	statusH := min(h/2, max(2, len(l.status)+1))
	l.statusTable.SetSize(w, statusH)
}

// Update advances the pane state. The Local pane handles status
// refresh on Tick and routes key events to the action list.
func (l *Local) Update(msg tea.Msg) (*Local, tea.Cmd) {
	switch msg := msg.(type) {
	case core.Result:
		if !l.loader.Accept(msg.Generation) {
			return l, nil
		}
		switch payload := msg.Payload.(type) {
		case core.LocalStatusMsg:
			l.status = payload
			l.statusLoaded = true
			l.populateStatusTable()
		case overlays.ToastMsg:
			return l, func() tea.Msg { return payload }
		}
		return l, nil

	case core.TickMsg:
		if l.docker == nil {
			return l, nil
		}
		return l, l.fetchStatus()

	case tea.KeyMsg:
		if l.list.FilterState() == list.Filtering {
			if key.Matches(msg, core.Keys.Select) {
				updated, _ := l.list.Update(msg)
				l.list = updated
				return l.handleSelect()
			}
			var cmd tea.Cmd
			l.list, cmd = l.list.Update(msg)
			return l, cmd
		}

		keyStr := msg.String()
		if keyStr >= "0" && keyStr <= "9" {
			if model, cmd, handled := l.handleNumeric(keyStr); handled {
				return model, cmd
			}
		} else {
			l.inputBuf = ""
		}

		switch {
		case key.Matches(msg, core.Keys.Filter):
			l.list.ResetFilter()
		case key.Matches(msg, core.Keys.Select):
			return l.handleSelect()
		}
	}

	var cmd tea.Cmd
	l.list, cmd = l.list.Update(msg)
	return l, cmd
}

func (l *Local) populateStatusTable() {
	items := make([]core.RowProvider, 0, len(l.status))
	for _, s := range l.status {
		items = append(items, statusRow{ContainerStatus: s})
	}
	l.statusTable.SetItems(items)
	if l.width > 0 && l.height > 0 {
		l.SetSize(l.width, l.height)
	}
}

func (l *Local) handleNumeric(digit string) (*Local, tea.Cmd, bool) {
	l.inputBuf += digit
	idx, _ := strconv.Atoi(l.inputBuf)
	listSize := len(l.list.VisibleItems())

	if listSize < 10 {
		if idx > 0 && idx <= listSize {
			l.list.Select(idx - 1)
			l.inputBuf = ""
			m, c := l.handleSelect()
			return m, c, true
		}
		l.inputBuf = ""
		return l, nil, true
	}

	if len(l.inputBuf) == 2 {
		if idx > 0 && idx <= listSize {
			l.list.Select(idx - 1)
			l.inputBuf = ""
			m, c := l.handleSelect()
			return m, c, true
		}
		l.inputBuf = ""
		return l, nil, true
	}
	if idx*10 > listSize {
		if idx > 0 && idx <= listSize {
			l.list.Select(idx - 1)
			l.inputBuf = ""
			m, c := l.handleSelect()
			return m, c, true
		}
		l.inputBuf = ""
		return l, nil, true
	}
	return l, nil, true
}

func (l *Local) handleSelect() (*Local, tea.Cmd) {
	li, ok := core.AsListItem(l.list.SelectedItem())
	if !ok || li.IsSeparator() {
		return l, nil
	}
	return l, l.runAction(li.Text)
}

func (l *Local) initList(w, availableH int) {
	items := []list.Item{
		core.Item("up"),
		core.Item("down"),
		core.Item("pause"),
		core.Item("resume"),
	}
	l.list = list.New(items, core.NewItemDelegate(l.styles), w, availableH)
	l.list.Title = "Local Cluster Actions"
	l.list.SetShowStatusBar(false)
	l.list.SetFilteringEnabled(true)
	l.list.Styles.Title = l.styles.Title
	l.list.SetShowHelp(false)
	l.list.SetShowTitle(false)
}

// View renders the status table (top) and the action list (bottom).
func (l *Local) View(_, _ int) string {
	filterInfo := ""
	if l.list.FilterValue() != "" {
		filterInfo = l.styles.Muted.Render(" [filter: " + l.list.FilterValue() + "]")
	}

	statusTitle := l.styles.Title.Render("Cluster Status")
	var statusBody string
	switch {
	case l.docker == nil:
		statusBody = l.styles.Muted.Render("Connecting to Docker…")
	case !l.statusLoaded:
		statusBody = l.styles.Muted.Render("Loading container status…")
	case len(l.status) == 0:
		statusBody = l.styles.Muted.Render("No kind containers found")
	default:
		statusBody = l.statusTable.View()
	}

	actionsTitle := l.styles.Title.Render("Actions") + filterInfo

	return lipgloss.JoinVertical(lipgloss.Left,
		statusTitle,
		statusBody,
		"",
		actionsTitle,
		"",
		l.list.View(),
	)
}

func (l *Local) runAction(action string) tea.Cmd {
	cmd := buildLocalCmd(action)
	if cmd == nil {
		return nil
	}
	return tea.ExecProcess(cmd, func(_ error) tea.Msg {
		return nil
	})
}

func buildLocalCmd(action string) *exec.Cmd {
	var c *exec.Cmd
	switch action {
	case "up":
		c = exec.Command("spacebox", "cluster", "up")
	case "down":
		c = exec.Command("spacebox", "cluster", "down")
	case "status":
		c = exec.Command("docker", "ps", "-a", "--filter", "label=io.x-k8s.kind.cluster")
	case "pause":
		c = exec.Command("sh", "-c", "docker pause $(docker ps -q --filter label=io.x-k8s.kind.cluster)")
	case "resume":
		c = exec.Command("sh", "-c", "docker unpause $(docker ps -q --filter label=io.x-k8s.kind.cluster)")
	}
	if c != nil {
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Stdin = os.Stdin
	}
	return c
}
