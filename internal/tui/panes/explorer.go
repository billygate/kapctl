// Package panes contains the per-tab Bubble Tea sub-models that the root
// AppModel delegates to. Each pane owns its own list state, filter handling,
// and numeric jump logic; the root model is reduced to tab switching, window
// sizing, help, and message routing.
package panes

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kapctl/internal/kube"
	"github.com/billygate/kapctl/internal/portfwd"
	"github.com/billygate/kapctl/internal/tui/core"
	"github.com/billygate/kapctl/internal/tui/overlays"
	"github.com/billygate/kapctl/internal/tui/styles"
)

// newKubeClient is the package-level handle to kube.NewClient that
// tests in this package can override (it is unexported) to avoid
// depending on a real kubeconfig.
var newKubeClient = kube.NewClient

// ResumeStore is the subset of *config.Config that Explorer needs in
// order to persist and read the last-selected context/namespace.
// Declaring it here keeps panes free of a direct config import.
type ResumeStore interface {
	LastContext() string
	LastNamespace() string
	SetLastContext(string)
	SetLastNamespace(string)
	Save() error
}

// ── Explorer-local async message types ──────────────────────────────────────

type namespacesLoadedMsg struct{ items []string }
type podsLoadedMsg struct{ items []kube.PodInfo }
type podPortsLoadedMsg struct{ items []kube.ContainerPort }
type podDeletedMsg struct{ pod string }
type explorerLoadErrMsg struct{ err error }

type explorerStep int

const (
	stepContext explorerStep = iota
	stepNamespace
	stepPod
	stepAction
	stepPort
	stepPortForm
)

// podRow adapts kube.PodInfo to core.RowProvider for the Pod step Table.
type podRow struct{ kube.PodInfo }

func (p podRow) Cells() table.Row    { return table.Row{p.Name, p.Status, p.Age} }
func (p podRow) FilterValue() string { return p.Name }

// Explorer is the Kubernetes wizard pane: context → namespace → pod →
// action [→ port].
//
// Steps that show plain labels (context, namespace, action, port) are
// driven by a bubbles/list rendered through its own View(). The pod
// step shows tabular data and is driven by a core.Table.
//
// The kube client is supplied asynchronously: NewExplorer can be called
// with a nil client (and nil error), and AppModel later calls
// SetKubeClient when an async core.LoadKube completes. Until then the
// pane renders a "Connecting…" placeholder so the TUI shows immediately.
type Explorer struct {
	step      explorerStep
	listStep  explorerStep // step e.list was last built for (cursor preservation)
	list      list.Model
	podTable  *core.Table
	ctx       string
	ns        string
	pod       string
	action    string
	filter    string
	kube      core.KubeClient
	err       error
	kubeReady bool
	styles    *styles.Styles
	width     int
	height    int
	inputBuf  string

	// Loader sequences async kube fetches; cached fields hold the last result.
	loader     core.Loader
	namespaces []string
	pods       []kube.PodInfo
	podPorts   []kube.ContainerPort

	// loadErr holds the last async-load failure for the current step.
	// Set on explorerLoadErrMsg, cleared on success / Back / new load.
	// View() renders an inline error pane when this is non-nil.
	loadErr error

	// Port-form fields. Populated only when step == stepPortForm.
	formLocal  textinput.Model
	formRemote textinput.Model
	formFocus  int    // 0 = local, 1 = remote
	formErr    string // styles.Warn — blocks submit
	formInfo   string // styles.Muted — informational (auto-bump hint)

	resume ResumeStore
}

// NewExplorer builds the Explorer pane. The kube client may be nil
// (and kubeErr nil too) — the pane will render a "Connecting…"
// placeholder until SetKubeClient is called with a live client.
// Pass a non-nil kubeErr to render the configuration-error view.
func NewExplorer(k core.KubeClient, kubeErr error, s *styles.Styles, cfg ResumeStore) *Explorer {
	e := &Explorer{
		step:   stepContext,
		kube:   k,
		err:    kubeErr,
		styles: s,
		resume: cfg,
		podTable: core.NewTable(s, []table.Column{
			{Title: "NAME", Width: 30},
			{Title: "STATUS", Width: 12},
			{Title: "AGE", Width: 10},
		}, core.WithRowNumbers()),
	}
	// An empty placeholder list is required so View() / SetSize() don't
	// dereference a zero-value list.Model before the client arrives.
	e.list = list.New(nil, core.NewItemDelegate(s), 0, 0)

	switch {
	case kubeErr != nil:
		e.list.Title = "Kubernetes Error"
	case k != nil:
		// Eager construction path (tests only) — auto-resume is wired
		// exclusively in SetKubeClient, so a caller that pre-wires a
		// client must not also rely on cfg.LastContext().
		e.kubeReady = true
		e.initView(0, 0)
	default:
		// Lazy: no client yet, no error. Wait for SetKubeClient.
		e.list.Title = "Connecting to Kubernetes…"
	}
	return e
}

// Selection returns the currently selected context and namespace, either
// of which may be empty if the user has not yet picked them.
func (e *Explorer) Selection() (ctx, ns string) { return e.ctx, e.ns }

// SetKubeClient wires the kube client in asynchronously. Called by the
// root model when core.LoadKube completes. Returns a tea.Cmd if the new
// state requires kicking off any async fetches.
func (e *Explorer) SetKubeClient(k core.KubeClient, err error) tea.Cmd {
	e.kube = k
	e.err = err
	e.kubeReady = err == nil && k != nil
	if !e.kubeReady {
		return nil
	}

	// No saved state — behave exactly as before.
	if e.resume == nil || e.resume.LastContext() == "" {
		if e.step == stepContext {
			e.initView(e.width, e.height)
		}
		return nil
	}

	savedCtx := e.resume.LastContext()
	if !slices.Contains(k.GetContexts(), savedCtx) {
		e.resume.SetLastContext("")
		e.resume.SetLastNamespace("")
		saveCmd := e.saveResume()
		e.step = stepContext
		e.initView(e.width, e.height)
		return tea.Batch(saveCmd, func() tea.Msg {
			return overlays.ToastMsg{Kind: overlays.ToastInfo, Text: "saved context " + savedCtx + " not in kubeconfig, starting fresh"}
		})
	}

	k2, cerr := newKubeClient(savedCtx)
	if cerr != nil {
		e.step = stepContext
		e.initView(e.width, e.height)
		return func() tea.Msg {
			return overlays.ToastMsg{Kind: overlays.ToastError, Text: cerr.Error()}
		}
	}
	e.kube = k2
	e.ctx = savedCtx

	if e.resume.LastNamespace() == "" {
		e.step = stepNamespace
		e.namespaces = nil
		e.initView(e.width, e.height)
		return e.loadNamespaces()
	}

	e.ns = e.resume.LastNamespace()
	e.step = stepPod
	e.pods = nil
	e.initView(e.width, e.height)
	return e.loadPods()
}

// tryJumpKeys handles the `c` / `n` shortcuts that warp the wizard
// directly to the context or namespace step from anywhere except the
// port-form (which owns text input). Returns handled=true to short-
// circuit the caller's normal key routing. `n` is a no-op when no
// context has been chosen yet.
func (e *Explorer) tryJumpKeys(keyMsg tea.KeyMsg) (tea.Cmd, bool) {
	if e.step == stepPortForm {
		return nil, false
	}
	switch {
	case key.Matches(keyMsg, core.Keys.JumpContext):
		if e.step == stepContext {
			return nil, true
		}
		e.step = stepContext
		e.ctx = ""
		e.ns = ""
		e.loadErr = nil
		e.list.ResetFilter()
		e.filter = ""
		e.initView(e.width, e.height)
		return nil, true
	case key.Matches(keyMsg, core.Keys.JumpNamespace):
		if e.ctx == "" || e.step == stepNamespace {
			return nil, true
		}
		e.step = stepNamespace
		e.ns = ""
		e.loadErr = nil
		e.list.ResetFilter()
		e.filter = ""
		e.initView(e.width, e.height)
		// Namespaces were cached during the last forward load; if for
		// some reason they're empty, kick a fresh load so the user gets
		// a populated list rather than an empty pane.
		if len(e.namespaces) == 0 {
			return e.loadNamespaces(), true
		}
		return nil, true
	}
	return nil, false
}

// saveResume returns a tea.Cmd that calls Save() and emits a toast on
// failure. Safe to call when e.resume == nil — returns nil in that case.
// Captures e.resume into a local so a later assignment can't redirect
// the deferred Save() to a different store.
func (e *Explorer) saveResume() tea.Cmd {
	if e.resume == nil {
		return nil
	}
	r := e.resume
	return func() tea.Msg {
		if err := r.Save(); err != nil {
			return overlays.ToastMsg{Kind: overlays.ToastError, Text: "saving config: " + err.Error()}
		}
		return nil
	}
}

// Init returns no startup commands; data is fetched as the user
// navigates between steps.
func (e *Explorer) Init() tea.Cmd { return nil }

// SetSize lays out the pane at the given dimensions.
func (e *Explorer) SetSize(w, h int) {
	e.width, e.height = w, h
	e.list.SetSize(w, h)
	e.podTable.SetSize(w, h)
}

// usingTable reports whether the current step is rendered through the
// core.Table (vs. the bubbles/list).
func (e *Explorer) usingTable() bool { return e.step == stepPod }

// Update advances the step machine, accepting kube fetch results,
// keystrokes, and the periodic Tick.
func (e *Explorer) Update(msg tea.Msg) (*Explorer, tea.Cmd) {
	switch msg := msg.(type) {
	case core.TickMsg:
		if e.step == stepPod && e.kube != nil {
			return e, e.loadPods()
		}
		return e, nil

	case core.Result:
		if !e.loader.Accept(msg.Generation) {
			return e, nil
		}
		switch payload := msg.Payload.(type) {
		case namespacesLoadedMsg:
			e.loadErr = nil
			e.namespaces = payload.items
			e.initView(e.width, e.height)
		case podsLoadedMsg:
			e.loadErr = nil
			e.pods = payload.items
			e.initView(e.width, e.height)
		case podPortsLoadedMsg:
			e.loadErr = nil
			e.podPorts = payload.items
			e.initView(e.width, e.height)
		case podDeletedMsg:
			e.loadErr = nil
			e.pods = nil
			e.step = stepPod
			e.initView(e.width, e.height)
			cmds := []tea.Cmd{
				func() tea.Msg {
					return overlays.ToastMsg{Kind: overlays.ToastInfo, Text: "deleted pod " + payload.pod}
				},
				e.loadPods(),
			}
			return e, tea.Batch(cmds...)
		case explorerLoadErrMsg:
			// Persistent inline display; intentionally no ToastMsg so the
			// error doesn't disappear with the toast TTL.
			e.loadErr = payload.err
			return e, nil
		}
		return e, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		if e.usingTable() {
			var cmd tea.Cmd
			e.podTable, cmd = e.podTable.Update(msg)
			return e, cmd
		}
		var cmd tea.Cmd
		e.list, cmd = e.list.Update(msg)
		return e, cmd
	}

	if e.usingTable() {
		return e.updatePodTable(keyMsg)
	}
	return e.updateList(keyMsg)
}

// updatePodTable drives the table-backed Pod step.
func (e *Explorer) updatePodTable(keyMsg tea.KeyMsg) (*Explorer, tea.Cmd) {
	// While the user is typing in the table's filter input, the table
	// owns all keys except enter (commit) and esc (cancel) — handled inside.
	if e.podTable.FilterState() == core.TableFiltering {
		var cmd tea.Cmd
		e.podTable, cmd = e.podTable.Update(keyMsg)
		return e, cmd
	}

	if cmd, handled := e.tryJumpKeys(keyMsg); handled {
		return e, cmd
	}

	switch {
	case key.Matches(keyMsg, core.Keys.Retry):
		if e.loadErr != nil {
			if cmd := e.retryCurrentLoad(); cmd != nil {
				return e, cmd
			}
		}
	case key.Matches(keyMsg, core.Keys.Back):
		if e.podTable.FilterState() == core.TableFilterApplied {
			e.podTable.ResetFilter()
			return e, nil
		}
		if e.step > stepContext {
			e.step--
			e.loadErr = nil
			if e.step < stepPod {
				e.ns = ""
			}
			if e.step < stepNamespace {
				e.ctx = ""
			}
			e.initView(e.width, e.height)
			return e, nil
		}
		return e, nil
	case key.Matches(keyMsg, core.Keys.Select):
		return e.handleTableSelect()
	}

	var cmd tea.Cmd
	e.podTable, cmd = e.podTable.Update(keyMsg)

	// Numeric digit on a single-digit list jumps and selects in one shot —
	// match the list-flow UX.
	s := keyMsg.String()
	if s >= "0" && s <= "9" && e.podTable.Len() < 10 {
		return e.handleTableSelect()
	}
	return e, cmd
}

// updateList drives the list-backed steps (context/namespace/action/port)
// plus the two-field port form (stepPortForm), which owns its own key
// handling and bypasses the list entirely.
func (e *Explorer) updateList(keyMsg tea.KeyMsg) (*Explorer, tea.Cmd) {
	if e.step == stepPortForm {
		return e.updatePortForm(keyMsg)
	}

	if e.list.FilterState() == list.Filtering {
		if key.Matches(keyMsg, core.Keys.Select) {
			updated, _ := e.list.Update(keyMsg)
			e.list = updated
			e.filter = updated.FilterValue()
			return e.handleSelect()
		}
		var cmd tea.Cmd
		e.list, cmd = e.list.Update(keyMsg)
		return e, cmd
	}

	if cmd, handled := e.tryJumpKeys(keyMsg); handled {
		return e, cmd
	}

	keyStr := keyMsg.String()
	if keyStr >= "0" && keyStr <= "9" {
		if model, cmd, handled := e.handleNumeric(keyStr); handled {
			return model, cmd
		}
	} else {
		e.inputBuf = ""
	}

	switch {
	case key.Matches(keyMsg, core.Keys.Retry):
		if e.loadErr != nil {
			if cmd := e.retryCurrentLoad(); cmd != nil {
				return e, cmd
			}
		}
	case key.Matches(keyMsg, core.Keys.Filter):
		e.list.ResetFilter()
		e.filter = ""
	case key.Matches(keyMsg, core.Keys.Back):
		if e.list.FilterValue() != "" {
			e.list.ResetFilter()
			e.filter = ""
			return e, nil
		}
		if e.step > stepContext {
			e.step--
			e.loadErr = nil
			// Clear the selection that the user is stepping past so the
			// breadcrumb stays truthful. Do NOT update the resume store
			// here — persist is forward-only by design.
			if e.step < stepPod {
				e.ns = ""
			}
			if e.step < stepNamespace {
				e.ctx = ""
			}
			e.initView(e.width, e.height)
			return e, nil
		}
		e.loadErr = nil
	case key.Matches(keyMsg, core.Keys.Select):
		return e.handleSelect()
	}

	var cmd tea.Cmd
	e.list, cmd = e.list.Update(keyMsg)
	return e, cmd
}

func (e *Explorer) handleNumeric(digit string) (*Explorer, tea.Cmd, bool) {
	// The item delegate renders a 1-based IDX that skips separators
	// (so the user sees 1..N for selectable rows only). Numeric jump
	// must map the typed index back to the underlying list position
	// in the same way, otherwise typing "1" can land on a separator.
	items := e.list.VisibleItems()
	selectablePositions := make([]int, 0, len(items))
	for i, it := range items {
		if li, ok := core.AsListItem(it); ok && li.IsSeparator() {
			continue
		}
		selectablePositions = append(selectablePositions, i)
	}

	buf, idx, committed := core.NumericJump(e.inputBuf, digit, len(selectablePositions))
	e.inputBuf = buf
	if committed && idx > 0 {
		e.list.Select(selectablePositions[idx-1])
		m, c := e.handleSelect()
		return m, c, true
	}
	return e, nil, true
}

// handleTableSelect is the Pod-step variant of handleSelect.
func (e *Explorer) handleTableSelect() (*Explorer, tea.Cmd) {
	sel := e.podTable.SelectedItem()
	if sel == nil {
		return e, nil
	}
	row, ok := sel.(podRow)
	if !ok {
		return e, nil
	}
	e.pod = row.Name
	e.step = stepAction
	e.podTable.ResetFilter()
	e.initView(e.width, e.height)
	return e, nil
}

func (e *Explorer) handleSelect() (*Explorer, tea.Cmd) {
	it := e.list.SelectedItem()
	if it == nil {
		return e, nil
	}

	li, ok := core.AsListItem(it)
	if !ok {
		return e, nil
	}
	if li.IsSeparator() {
		return e, nil
	}
	val := li.Text

	e.filter = e.list.FilterValue()

	switch e.step {
	case stepContext:
		if e.resume != nil {
			e.resume.SetLastContext(val)
			e.resume.SetLastNamespace("")
		}
		k, err := newKubeClient(val)
		if err != nil {
			return e, tea.Batch(
				e.saveResume(),
				func() tea.Msg {
					return overlays.ToastMsg{Kind: overlays.ToastError, Text: err.Error()}
				},
			)
		}
		e.ctx = val
		e.kube = k
		e.step = stepNamespace
		e.namespaces = nil
		e.list.ResetFilter()
		e.filter = ""
		e.initView(e.width, e.height)
		return e, tea.Batch(e.saveResume(), e.loadNamespaces())
	case stepNamespace:
		e.ns = val
		if e.resume != nil {
			e.resume.SetLastNamespace(val)
		}
		e.step = stepPod
		e.pods = nil
		e.list.ResetFilter()
		e.filter = ""
		e.initView(e.width, e.height)
		return e, tea.Batch(e.saveResume(), e.loadPods())
	case stepAction:
		if val == "port-forward" {
			e.action = val
			e.step = stepPort
			e.podPorts = nil
			e.list.ResetFilter()
			e.filter = ""
			e.initView(e.width, e.height)
			return e, e.loadPodPorts()
		}
		return e, e.runAction(val, "")
	case stepPort:
		// Route every selection through the port-form so the user can
		// confirm or edit the mapping (and so we apply the bump-on-conflict
		// check). Custom prefills both fields with the first detected port
		// when one exists; otherwise the fields start empty.
		if overlays.IsCustomPortChoice(val) {
			def := 0
			if len(e.podPorts) > 0 {
				def = int(e.podPorts[0].Port)
			}
			e.enterPortForm(def, def)
			return e, nil
		}
		p, err := overlays.ParsePort(val)
		if err != nil {
			return e, func() tea.Msg {
				return overlays.ToastMsg{Kind: overlays.ToastError, Text: "invalid port: " + val}
			}
		}
		e.enterPortForm(p, p)
		return e, nil
	}
	return e, nil
}

// loadNamespaces returns a tea.Cmd that fetches namespaces for the
// current context. Clears e.loadErr at kick-off so a pending error
// view is replaced with the (briefly empty) list while the load runs.
func (e *Explorer) loadNamespaces() tea.Cmd {
	e.loadErr = nil
	kClient := e.kube
	return e.loader.Start(context.Background(), func(ctx context.Context) tea.Msg {
		items, err := kClient.GetNamespaces(ctx)
		if err != nil {
			return explorerLoadErrMsg{err: err}
		}
		return namespacesLoadedMsg{items: items}
	})
}

// loadPods returns a tea.Cmd that fetches pods for the current namespace.
func (e *Explorer) loadPods() tea.Cmd {
	e.loadErr = nil
	kClient, ns := e.kube, e.ns
	return e.loader.Start(context.Background(), func(ctx context.Context) tea.Msg {
		items, err := kClient.GetPods(ctx, ns)
		if err != nil {
			return explorerLoadErrMsg{err: err}
		}
		return podsLoadedMsg{items: items}
	})
}

// loadPodPorts returns a tea.Cmd that fetches container ports for the
// currently selected pod.
func (e *Explorer) loadPodPorts() tea.Cmd {
	e.loadErr = nil
	kClient, ns, pod := e.kube, e.ns, e.pod
	return e.loader.Start(context.Background(), func(ctx context.Context) tea.Msg {
		items, err := kClient.GetPodPorts(ctx, ns, pod)
		if err != nil {
			return explorerLoadErrMsg{err: err}
		}
		return podPortsLoadedMsg{items: items}
	})
}

// retryCurrentLoad re-kicks the load appropriate to the current step.
// Returns nil for steps that have no async load (context/action/port-form).
func (e *Explorer) retryCurrentLoad() tea.Cmd {
	switch e.step {
	case stepNamespace:
		return e.loadNamespaces()
	case stepPod:
		return e.loadPods()
	case stepPort:
		return e.loadPodPorts()
	}
	return nil
}

// initView builds the data-bound widget for the current step. For
// stepPod that's the Table; otherwise it's the bubbles/list.
func (e *Explorer) initView(w, availableH int) {
	if e.step == stepPod {
		e.populatePodTable(w, availableH)
		return
	}
	e.initList(w, availableH)
}

// populatePodTable hands the current pod set to the Table.
func (e *Explorer) populatePodTable(w, availableH int) {
	items := make([]core.RowProvider, 0, len(e.pods))
	for _, p := range e.pods {
		items = append(items, podRow{PodInfo: p})
	}
	e.podTable.SetItems(items)
	if w > 0 && availableH > 0 {
		e.podTable.SetSize(w, availableH)
	}
}

func (e *Explorer) initList(w, availableH int) {
	// Preserve the cursor only when rebuilding the same step's list
	// (retry, periodic refresh). On a step transition the old index is
	// meaningless for the new item set — start at the top.
	oldIdx := 0
	if e.listStep == e.step {
		oldIdx = e.list.Index()
	}
	e.listStep = e.step

	var items []list.Item
	title := ""
	switch e.step {
	case stepContext:
		title = "Select Context"
		contexts := e.kube.GetContexts()
		sort.Strings(contexts)
		for _, c := range contexts {
			items = append(items, core.Item(c))
		}
	case stepNamespace:
		title = "Select Namespace (" + e.ctx + ")"
		for _, n := range e.namespaces {
			items = append(items, core.Item(n))
		}
	case stepAction:
		title = "Action for " + e.pod
		for _, a := range []string{"logs", "exec", "describe", "port-forward", "delete"} {
			items = append(items, core.Item(a))
		}
	case stepPort:
		title = "Select Port for " + e.pod
		items = e.buildPortItems()
	}

	e.list = list.New(items, core.NewItemDelegate(e.styles), w, availableH)
	e.list.Title = title
	e.list.SetShowStatusBar(false)
	e.list.SetFilteringEnabled(true)
	e.list.Styles.Title = e.styles.Title
	e.list.SetShowHelp(false)
	// Suppress the list's built-in title — Explorer.View renders its
	// own pane title via styles.Title.Render(e.viewTitle()), and the
	// list would otherwise render a duplicate underneath.
	e.list.SetShowTitle(false)

	if e.filter != "" {
		e.list.FilterInput.SetValue(e.filter)
		e.list.SetFilterState(list.FilterApplied)
	}

	if oldIdx >= 0 && oldIdx < len(items) {
		e.list.Select(oldIdx)
	}
}

func (e *Explorer) buildPortItems() []list.Item {
	choices := overlays.BuildPortChoices(e.podPorts)
	var items []list.Item
	for _, label := range choices {
		if strings.HasPrefix(label, "──") {
			items = append(items, core.Separator(label))
		} else {
			items = append(items, core.Item(label))
		}
	}
	return items
}

// View renders the current step (or an error/connecting placeholder).
func (e *Explorer) View(_, _ int) string {
	if e.err != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			e.styles.Title.Render("Kubernetes Configuration Error"),
			"",
			e.styles.Warn.Render("Could not initialize Kubernetes client:"),
			"",
			e.err.Error(),
			"",
			e.styles.Muted.Render("Please ensure your KUBECONFIG is set correctly."),
		)
	}

	if !e.kubeReady {
		return lipgloss.JoinVertical(lipgloss.Left,
			e.styles.Title.Render("Connecting to Kubernetes…"),
			"",
			e.styles.Muted.Render("Loading kubeconfig in the background."),
		)
	}

	if e.loadErr != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			e.styles.Title.Render(e.viewTitle()),
			"",
			e.styles.Warn.Render("Failed to load:"),
			"",
			e.loadErr.Error(),
			"",
			e.styles.Muted.Render("r: retry  •  esc: back"),
		)
	}

	title := e.styles.Title.Render(e.viewTitle())

	var content string
	switch e.step {
	case stepPortForm:
		content = e.viewPortForm()
	case stepPod:
		if len(e.pods) == 0 {
			content = "No pods found"
		} else {
			content = e.podTable.View()
		}
	default:
		content = e.list.View()
	}

	return lipgloss.JoinVertical(lipgloss.Left, title, "", content)
}

// viewPortForm renders the two-field port-mapping form with footnotes.
func (e *Explorer) viewPortForm() string {
	rows := []string{
		"  Local  : " + e.formLocal.View(),
		"  Remote : " + e.formRemote.View(),
		"",
	}
	switch {
	case e.formErr != "":
		rows = append(rows, "  "+e.styles.Warn.Render(e.formErr))
	case e.formInfo != "":
		rows = append(rows, "  "+e.styles.Muted.Render("ℹ  "+e.formInfo))
	}
	rows = append(rows, "")
	rows = append(rows, e.styles.Muted.Render("  tab: switch field  •  enter: start  •  esc: back"))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// viewTitle is the human-readable title for the current step.
func (e *Explorer) viewTitle() string {
	switch e.step {
	case stepContext:
		return "Select Context"
	case stepNamespace:
		return "Select Namespace (" + e.ctx + ")"
	case stepPod:
		return "Select Pod (" + e.ns + ")"
	case stepAction:
		return "Action for " + e.pod
	case stepPort:
		return "Select Port for " + e.pod
	case stepPortForm:
		return "Port-forward for " + e.pod
	}
	return ""
}

// enterPortForm transitions the wizard into stepPortForm. If local > 0,
// IsLocalPortFree is probed against it; on miss, FindFreeLocalPort
// scans up to 100 ports forward. The Local field is populated with the
// resulting (possibly bumped) value; Remote is always populated with
// the picked value as-is. local == 0 (and remote == 0) means custom —
// both fields stay empty and no probe runs.
func (e *Explorer) enterPortForm(local, remote int) {
	e.formLocal = newPortInput()
	e.formRemote = newPortInput()
	e.formErr = ""
	e.formInfo = ""
	e.formFocus = 0
	e.formLocal.Focus()

	switch {
	case local == 0 && remote == 0:
		// Custom — leave fields empty.
	default:
		actualLocal := local
		if err := portfwd.IsLocalPortFree(local); err != nil {
			bumped, ferr := portfwd.FindFreeLocalPort(local, 100)
			switch {
			case ferr != nil:
				e.formErr = fmt.Sprintf("could not find a free local port in [%d, %d]", local, local+100)
			default:
				actualLocal = bumped
				e.formInfo = fmt.Sprintf("local %d was in use, using %d", local, bumped)
			}
		}
		e.formLocal.SetValue(strconv.Itoa(actualLocal))
		e.formRemote.SetValue(strconv.Itoa(remote))
	}

	e.step = stepPortForm
}

// updatePortForm handles keys while the port-mapping form is active:
// esc returns to the picker, tab/shift+tab toggles focus, enter validates
// and emits a PortForwardRequestMsg; other keys are forwarded to the
// focused textinput.
func (e *Explorer) updatePortForm(keyMsg tea.KeyMsg) (*Explorer, tea.Cmd) {
	switch keyMsg.String() {
	case "esc":
		e.step = stepPort
		e.formErr = ""
		e.formInfo = ""
		e.formLocal.Blur()
		e.formRemote.Blur()
		e.initView(e.width, e.height)
		return e, nil
	case "tab", "shift+tab":
		if e.formFocus == 0 {
			e.formFocus = 1
			e.formLocal.Blur()
			e.formRemote.Focus()
		} else {
			e.formFocus = 0
			e.formRemote.Blur()
			e.formLocal.Focus()
		}
		return e, nil
	case "enter":
		return e.submitPortForm()
	}

	var cmd tea.Cmd
	if e.formFocus == 0 {
		e.formLocal, cmd = e.formLocal.Update(keyMsg)
	} else {
		e.formRemote, cmd = e.formRemote.Update(keyMsg)
	}
	return e, cmd
}

// submitPortForm validates the form and emits a PortForwardRequestMsg
// for the AppModel to forward to the portfwd manager. Validation errors
// stay inline on the form via formErr; the form stays open so the user
// can correct and retry.
func (e *Explorer) submitPortForm() (*Explorer, tea.Cmd) {
	local, lerr := parseFormPort(e.formLocal.Value())
	remote, rerr := parseFormPort(e.formRemote.Value())
	switch {
	case lerr != nil:
		e.formErr = "Local: " + lerr.Error()
		return e, nil
	case rerr != nil:
		e.formErr = "Remote: " + rerr.Error()
		return e, nil
	}
	if perr := portfwd.IsLocalPortFree(local); perr != nil {
		e.formErr = fmt.Sprintf("local port %d is in use", local)
		return e, nil
	}
	ctx, ns, pod := e.ctx, e.ns, e.pod
	return e, func() tea.Msg {
		return core.PortForwardRequestMsg{
			Context: ctx, Namespace: ns, Target: pod,
			Kind:       portfwd.KindPod,
			LocalPort:  local,
			RemotePort: remote,
		}
	}
}

// parseFormPort validates a single port-form field. Returns a short
// message suitable for the inline formErr line.
func parseFormPort(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("required")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not a number")
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("out of range (1..65535)")
	}
	return n, nil
}

// newPortInput builds a digit-only textinput sized for a port number.
func newPortInput() textinput.Model {
	ti := textinput.New()
	ti.CharLimit = 5
	ti.Width = 8
	ti.Validate = func(s string) error {
		for _, r := range s {
			if r < '0' || r > '9' {
				return fmt.Errorf("digits only")
			}
		}
		return nil
	}
	return ti
}

func (e *Explorer) runAction(action, port string) tea.Cmd {
	// port-forward is handled separately through stepPortForm — it
	// doesn't pass through this function. The actions that land here are
	// either foreground kubectl invocations (logs/exec/describe), which
	// run via tea.ExecProcess and suspend the TUI for their duration, or
	// delete, which is a managed async load.
	if action == "delete" {
		e.loadErr = nil
		kClient, ns, pod := e.kube, e.ns, e.pod
		return e.loader.Start(context.Background(), func(ctx context.Context) tea.Msg {
			if err := kClient.DeletePod(ctx, ns, pod); err != nil {
				return explorerLoadErrMsg{err: err}
			}
			return podDeletedMsg{pod: pod}
		})
	}
	return tea.ExecProcess(buildExplorerCmd(e.ctx, e.ns, e.pod, action, port), func(_ error) tea.Msg {
		return nil
	})
}

// buildExplorerCmd shapes the kubectl invocation for foreground actions
// (logs/exec/describe). port-forward is handled separately via the
// portfwd manager — see runAction.
func buildExplorerCmd(ctx, ns, pod, action, _ string) *exec.Cmd {
	var args []string
	switch action {
	case "logs":
		args = []string{"--context", ctx, "-n", ns, "logs", "-f", pod}
	case "exec":
		args = []string{"--context", ctx, "-n", ns, "exec", "-it", pod, "--", "sh"}
	case "describe":
		args = []string{"--context", ctx, "-n", ns, "describe", "pod", pod}
	}
	c := exec.Command("kubectl", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c
}
