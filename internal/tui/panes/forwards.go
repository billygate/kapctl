package panes

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kapctl/internal/portfwd"
	"github.com/billygate/kapctl/internal/tui/core"
	"github.com/billygate/kapctl/internal/tui/overlays"
	"github.com/billygate/kapctl/internal/tui/styles"
)

// fwdRow is the RowProvider adapter for portfwd.Snapshot. The captured
// `now` lets tests assert deterministic age + countdown text.
type fwdRow struct {
	portfwd.Snapshot
	now time.Time
}

func (f fwdRow) Cells() table.Row {
	target := f.Kind.Prefix() + f.Target
	age := f.now.Sub(f.StartedAt).Round(time.Second).String()
	status := f.Status.String()
	if f.Status == portfwd.StatusReconnecting && !f.ReconnectStartedAt.IsZero() {
		remaining := (120 * time.Second) - f.now.Sub(f.ReconnectStartedAt)
		if remaining < 0 {
			remaining = 0
		}
		status = fmt.Sprintf("reconnecting %d/%ds", f.Attempts, int(remaining.Seconds()))
	}
	return table.Row{
		fmt.Sprintf("%d", f.LocalPort),
		target,
		f.Namespace,
		status,
		age,
	}
}
func (f fwdRow) FilterValue() string { return f.Target }

// NewFwdRowForTest exposes the row constructor with a fixed `now` for
// deterministic tests of status-cell formatting. Production code calls
// refresh() which uses time.Now().
func NewFwdRowForTest(s portfwd.Snapshot, now time.Time) fwdRow {
	return fwdRow{Snapshot: s, now: now}
}

// Forwards is the pane that lists active port-forward processes and
// lets the user stop them. State is fully derived from the
// *portfwd.Manager handed in at construction; this pane never starts
// new forwards itself.
type Forwards struct {
	mgr      *portfwd.Manager
	tbl      *core.Table
	width    int
	height   int
	styles   *styles.Styles
	showLogs bool
	logsFor  string // entry ID currently displayed in the log view
}

// NewForwards builds the Forwards pane. The manager may be nil — in
// that case the pane just shows an empty state.
func NewForwards(mgr *portfwd.Manager, s *styles.Styles) *Forwards {
	return &Forwards{
		mgr:    mgr,
		styles: s,
		tbl: core.NewTable(s, []table.Column{
			{Title: "LOCAL", Width: 8},
			{Title: "TARGET", Width: 32},
			{Title: "NS", Width: 14},
			{Title: "STATUS", Width: 10},
			{Title: "AGE", Width: 8},
		}, core.WithRowNumbers()),
	}
}

// Init refreshes the table from the current manager state.
func (f *Forwards) Init() tea.Cmd {
	f.refresh()
	return nil
}

// SetSize lays the table out at the given dimensions.
func (f *Forwards) SetSize(w, h int) {
	f.width, f.height = w, h
	f.tbl.SetSize(w, h-4) // leave room for help line
}

// Update handles status-change messages and key events.
func (f *Forwards) Update(msg tea.Msg) (*Forwards, tea.Cmd) {
	switch msg := msg.(type) {
	case core.PortForwardEventMsg:
		f.refresh()
		return f, f.eventToToast(portfwd.Event(msg))

	case core.TickMsg:
		// Refresh the AGE column.
		f.refresh()
		return f, nil

	case tea.KeyMsg:
		// While typing in the table's filter input, the table owns keys.
		if f.tbl.FilterState() == core.TableFiltering {
			var cmd tea.Cmd
			f.tbl, cmd = f.tbl.Update(msg)
			return f, cmd
		}

		switch msg.String() {
		case "d":
			return f, f.stopSelected()
		case "x":
			return f, f.removeSelected()
		case "l":
			f.toggleLogs()
			return f, nil
		}
		switch {
		case key.Matches(msg, core.Keys.Back):
			if f.showLogs {
				f.showLogs = false
				return f, nil
			}
		}

		var cmd tea.Cmd
		f.tbl, cmd = f.tbl.Update(msg)
		return f, cmd
	}
	return f, nil
}

// View renders either the table or the log view of the selected entry.
func (f *Forwards) View(_, _ int) string {
	title := f.styles.Title.Render("Active Port-Forwards")
	if f.mgr == nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			f.styles.Muted.Render("Port-forward manager unavailable."),
		)
	}
	if f.showLogs {
		return f.viewLogs(title)
	}

	body := f.tbl.View()
	if f.tbl.Len() == 0 {
		body = f.styles.Muted.Render("No active forwards. Start one from EXPLORER → pod → port-forward.")
	}
	help := f.styles.Muted.Render("d stop  •  x remove  •  l logs  •  / filter")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)
}

// viewLogs renders the captured stderr lines for the selected entry.
func (f *Forwards) viewLogs(title string) string {
	id := f.logsFor
	if id == "" {
		return lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			f.styles.Muted.Render("No entry selected. Press esc to return."),
		)
	}
	lines, err := f.mgr.Logs(id)
	if err != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			f.styles.Warn.Render(err.Error()),
			"",
			f.styles.Muted.Render("esc: back"),
		)
	}
	if len(lines) == 0 {
		lines = []string{"(no log lines yet)"}
	}
	out := []string{f.styles.Title.Render("Logs for forward " + id), ""}
	out = append(out, lines...)
	out = append(out, "", f.styles.Muted.Render("esc: back"))
	return lipgloss.JoinVertical(lipgloss.Left, out...)
}

// refresh syncs the table rows from the manager's current snapshot list.
func (f *Forwards) refresh() {
	if f.mgr == nil {
		return
	}
	now := time.Now()
	snaps := f.mgr.List()
	items := make([]core.RowProvider, 0, len(snaps))
	for _, s := range snaps {
		items = append(items, fwdRow{Snapshot: s, now: now})
	}
	f.tbl.SetItems(items)
}

func (f *Forwards) selectedID() string {
	sel := f.tbl.SelectedItem()
	if sel == nil {
		return ""
	}
	row, ok := sel.(fwdRow)
	if !ok {
		return ""
	}
	return row.ID
}

func (f *Forwards) stopSelected() tea.Cmd {
	id := f.selectedID()
	if id == "" || f.mgr == nil {
		return nil
	}
	if err := f.mgr.Stop(id); err != nil {
		return func() tea.Msg {
			return overlays.ToastMsg{Kind: overlays.ToastError, Text: err.Error()}
		}
	}
	return nil
}

func (f *Forwards) removeSelected() tea.Cmd {
	id := f.selectedID()
	if id == "" || f.mgr == nil {
		return nil
	}
	f.mgr.Remove(id)
	f.refresh()
	return nil
}

func (f *Forwards) toggleLogs() {
	if f.showLogs {
		f.showLogs = false
		return
	}
	id := f.selectedID()
	if id == "" {
		return
	}
	f.logsFor = id
	f.showLogs = true
}

// eventToToast lifts portfwd state transitions into user-visible toasts.
// Only the "interesting" transitions are surfaced — Starting is silent,
// Running confirms success (or reconnection), Errored is loud, Stopped
// is informational. Reconnect series produce at most one info toast
// (on the first attempt) to avoid spamming the queue.
func (f *Forwards) eventToToast(ev portfwd.Event) tea.Cmd {
	if f.mgr == nil {
		return nil
	}
	// Look up the current snapshot for state-dependent decisions.
	var snap portfwd.Snapshot
	for _, s := range f.mgr.List() {
		if s.ID == ev.ID {
			snap = s
			break
		}
	}

	var kind overlays.ToastKind
	var text string
	switch ev.Status {
	case portfwd.StatusRunning:
		if snap.LastReconnectReason != "" {
			kind = overlays.ToastSuccess
			text = "port-forward " + ev.ID + " reconnected"
		} else {
			kind = overlays.ToastSuccess
			text = "port-forward running (" + ev.ID + ")"
		}
	case portfwd.StatusReconnecting:
		// Only the first event of a reconnect series produces a toast;
		// subsequent attempts in the same series are silent to avoid spam.
		if snap.Attempts > 1 {
			return nil
		}
		kind = overlays.ToastInfo
		text = "port-forward " + ev.ID + " reconnecting: " + ev.Detail
	case portfwd.StatusErrored:
		kind = overlays.ToastError
		if ev.Detail != "" {
			text = "port-forward " + ev.ID + " errored: " + ev.Detail
		} else {
			text = "port-forward " + ev.ID + " errored"
		}
	case portfwd.StatusStopped:
		kind = overlays.ToastInfo
		text = "port-forward stopped (" + ev.ID + ")"
	default:
		return nil
	}
	return func() tea.Msg { return overlays.ToastMsg{Kind: kind, Text: text} }
}
