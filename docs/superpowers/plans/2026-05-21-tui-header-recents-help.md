# TUI: Persistent Header, Resume Last Selection, Help Modal — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global ctx/ns breadcrumb header above panes, auto-resume to the last selected ctx/ns at startup, and a centered help modal opened with `?`.

**Architecture:** Persist `LastContext`/`LastNamespace` in `~/.config/kapctl/config.yaml`. `Explorer` remains the source of truth for current selection — exposes `Selection()` for the breadcrumb, calls a small `ResumeStore` interface (satisfied by `*config.Config`) for read/persist. `AppModel.View` composes `[tabs | divider | breadcrumb | divider2 | pane]`. A new `overlays.Help` modal is layered over the View when visible; while open, all keys except `?`/`esc`/`q` are absorbed.

**Tech Stack:** Go 1.26, Bubble Tea, bubbles/list, lipgloss, gopkg.in/yaml.v3.

**Spec:** `docs/superpowers/specs/2026-05-21-tui-header-recents-help-design.md`

---

## File Map

| File | Role |
|---|---|
| `internal/config/config.go` | Add `LastContext`/`LastNamespace` fields + accessor methods. |
| `internal/config/config_test.go` | Round-trip tests for new fields + backwards-compat. |
| `internal/config/testdata/with_last.yaml` | New fixture with `last_context`/`last_namespace`. |
| `internal/tui/overlays/help.go` | **New.** `Help` modal with static sections; `View(w,h,*styles.Styles) string` and `Visible bool`. |
| `internal/tui/overlays/help_test.go` | **New.** Rendering test for modal content. |
| `internal/tui/panes/explorer.go` | `ResumeStore` interface + `NewExplorer(... , ResumeStore)` + `Selection()` + auto-resume in `SetKubeClient` + persist on select. |
| `internal/tui/panes/panes_test.go` | Update Explorer constructor calls + new tests (auto-resume, persist-on-select). |
| `internal/tui/app.go` | Wire `cfg` into `NewExplorer`, add `helpOverlay`, add breadcrumb row, adjust `paneSize`, intercept keys while modal open. |
| `internal/tui/app_test.go` | Tests for `?` open, key absorption, esc/q/? close, breadcrumb in View. |

---

## Task 1: Config — persist last selection

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/testdata/with_last.yaml`
- Modify: `internal/config/config_test.go`

### Steps

- [ ] **Step 1.1 — Write failing test: round-trip with LastContext/LastNamespace**

Append to `internal/config/config_test.go`:

```go
func TestLastSelectionRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := &config.Config{Theme: "catppuccin", Ports: map[string]int{}}
	cfg.SetLastContext("prod-eu")
	cfg.SetLastNamespace("payments")
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, _, err := config.LoadFile(filepath.Join(dir, ".config", "kapctl", "config.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := loaded.LastContext(); got != "prod-eu" {
		t.Errorf("LastContext = %q, want prod-eu", got)
	}
	if got := loaded.LastNamespace(); got != "payments" {
		t.Errorf("LastNamespace = %q, want payments", got)
	}
}
```

- [ ] **Step 1.2 — Run test, expect FAIL**

Run: `go test ./internal/config/ -run TestLastSelectionRoundtrip -v`
Expected: FAIL with `cfg.SetLastContext undefined` (and friends).

- [ ] **Step 1.3 — Add fields and accessors**

Edit `internal/config/config.go`. Change the `Config` struct (around line 15) to:

```go
type Config struct {
	Theme           string         `yaml:"theme"`
	Ports           map[string]int `yaml:"ports"` // key: "<context>.<namespace>"
	LastContextRaw  string         `yaml:"last_context,omitempty"`
	LastNamespaceRaw string         `yaml:"last_namespace,omitempty"`
}
```

(The `Raw` suffix on the struct fields is intentional — the public API for callers is the accessor methods below. yaml uses the snake_case tags.)

Append to the end of `internal/config/config.go` (after `Save`):

```go
// LastContext returns the most recently selected kube context, or "".
func (c *Config) LastContext() string { return c.LastContextRaw }

// LastNamespace returns the most recently selected namespace, or "".
func (c *Config) LastNamespace() string { return c.LastNamespaceRaw }

// SetLastContext stores the given context as the last-used selection.
func (c *Config) SetLastContext(ctx string) { c.LastContextRaw = ctx }

// SetLastNamespace stores the given namespace as the last-used selection.
func (c *Config) SetLastNamespace(ns string) { c.LastNamespaceRaw = ns }
```

- [ ] **Step 1.4 — Run test, expect PASS**

Run: `go test ./internal/config/ -run TestLastSelectionRoundtrip -v`
Expected: PASS.

- [ ] **Step 1.5 — Add backwards-compat fixture and test**

Create `internal/config/testdata/with_last.yaml`:

```yaml
theme: nord
ports:
  ctx-a.ns-x: 5432
last_context: prod-eu
last_namespace: payments
```

Append to `config_test.go`:

```go
func TestLoadFileReadsLastSelection(t *testing.T) {
	cfg, _, err := config.LoadFile(filepath.Join("testdata", "with_last.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := cfg.LastContext(); got != "prod-eu" {
		t.Errorf("LastContext = %q, want prod-eu", got)
	}
	if got := cfg.LastNamespace(); got != "payments" {
		t.Errorf("LastNamespace = %q, want payments", got)
	}
}

func TestLoadFileMinimalHasEmptyLastSelection(t *testing.T) {
	// minimal.yaml is "{}" — must yield empty last fields, not error.
	cfg, _, err := config.LoadFile(filepath.Join("testdata", "minimal.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := cfg.LastContext(); got != "" {
		t.Errorf("LastContext = %q, want empty", got)
	}
	if got := cfg.LastNamespace(); got != "" {
		t.Errorf("LastNamespace = %q, want empty", got)
	}
}
```

- [ ] **Step 1.6 — Run tests, expect PASS**

Run: `go test ./internal/config/ -v`
Expected: all tests PASS (including pre-existing ones).

- [ ] **Step 1.7 — Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/testdata/with_last.yaml
git commit -m "config: persist last selected context and namespace

Add LastContext/LastNamespace fields with snake_case yaml tags and
accessor methods, plus round-trip tests and a fixture covering the
backwards-compat path for configs that omit the new keys."
```

---

## Task 2: Help modal overlay

**Files:**
- Create: `internal/tui/overlays/help.go`
- Create: `internal/tui/overlays/help_test.go`

### Steps

- [ ] **Step 2.1 — Write failing test**

Create `internal/tui/overlays/help_test.go`:

```go
package overlays_test

import (
	"strings"
	"testing"

	"github.com/billygate/kap-toolsbox/internal/tui/overlays"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
	"github.com/billygate/kap-toolsbox/internal/tui/themes"
)

func newTestStyles(t *testing.T) *styles.Styles {
	t.Helper()
	p, ok := themes.Get("catppuccin")
	if !ok {
		t.Fatal("catppuccin theme not registered")
	}
	return styles.New(p)
}

func TestHelpViewContainsSectionsAndBindings(t *testing.T) {
	s := newTestStyles(t)
	h := &overlays.Help{Visible: true}
	v := h.View(120, 40, s)

	for _, want := range []string{"Help", "Global", "enter", "esc", "?"} {
		if !strings.Contains(v, want) {
			t.Errorf("Help.View missing %q in:\n%s", want, v)
		}
	}
}

func TestHelpViewEmptyWhenHidden(t *testing.T) {
	s := newTestStyles(t)
	h := &overlays.Help{Visible: false}
	if got := h.View(120, 40, s); got != "" {
		t.Errorf("Help.View when hidden = %q, want empty", got)
	}
}
```

- [ ] **Step 2.2 — Run test, expect FAIL**

Run: `go test ./internal/tui/overlays/ -run TestHelp -v`
Expected: FAIL with `undefined: overlays.Help`.

- [ ] **Step 2.3 — Implement Help overlay**

Create `internal/tui/overlays/help.go`:

```go
package overlays

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kap-toolsbox/internal/tui/core"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
)

// Help is the modal opened with `?`. View returns the rendered modal
// when Visible is true, or an empty string when hidden.
type Help struct {
	Visible bool
}

// helpSection is one titled group of bindings rendered inside the modal.
type helpSection struct {
	title    string
	bindings [][2]string // {keys, description}
}

// helpSections returns the static content of the modal. Where possible
// the strings come from core.Keys via key.WithHelp so a binding rename
// is reflected automatically.
func helpSections() []helpSection {
	k := core.Keys
	return []helpSection{
		{
			title: "Global",
			bindings: [][2]string{
				{"?", "help (close)"},
				{"q / ctrl+c", "quit"},
				{k.NextTab.Help().Key + " / " + k.PrevTab.Help().Key, "next / prev tab"},
				{k.Retry.Help().Key, "retry load (when error shown)"},
			},
		},
		{
			title: "Navigation in lists",
			bindings: [][2]string{
				{k.Select.Help().Key, "select"},
				{k.Filter.Help().Key, "filter"},
				{k.Back.Help().Key, "back / clear filter"},
				{"1–9", "jump to item"},
				{"↑/↓  k/j", "move cursor"},
			},
		},
		{
			title: "Tables (Pod step)",
			bindings: [][2]string{
				{"/", "filter (enter commits, esc cancels)"},
				{"enter", "select"},
				{"esc", "back / reset filter"},
			},
		},
		{
			title: "Port-forwards tab",
			bindings: [][2]string{
				{"d", "stop forward"},
				{"x", "remove entry"},
				{"l", "toggle log view"},
				{"/", "filter"},
			},
		},
	}
}

// View renders the modal centered on a (w x h) area. Returns "" when hidden.
func (h *Help) View(w, h2 int, s *styles.Styles) string {
	if !h.Visible {
		return ""
	}

	var rows []string
	rows = append(rows, s.Title.Render("Help"))
	rows = append(rows, "")
	for _, sec := range helpSections() {
		rows = append(rows, s.Master.Render(sec.title))
		for _, b := range sec.bindings {
			rows = append(rows, "  "+s.Value.Render(padRight(b[0], 14))+s.Muted.Render(b[1]))
		}
		rows = append(rows, "")
	}
	rows = append(rows, s.Muted.Render("?/esc/q  close"))

	body := strings.Join(rows, "\n")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(s.Palette.Accent()).
		Padding(1, 2).
		Render(body)

	return lipgloss.Place(w, h2, lipgloss.Center, lipgloss.Center, box)
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len(s))
}
```

- [ ] **Step 2.4 — Run test, expect PASS**

Run: `go test ./internal/tui/overlays/ -run TestHelp -v`
Expected: PASS.

- [ ] **Step 2.5 — Run all overlay tests**

Run: `go test ./internal/tui/overlays/ -v`
Expected: all PASS (no regressions in existing picker/portselect/toast tests).

- [ ] **Step 2.6 — Commit**

```bash
git add internal/tui/overlays/help.go internal/tui/overlays/help_test.go
git commit -m "tui: add Help modal overlay

Static sections rendered as a centered box, with binding labels read
from core.Keys so a future rename of a binding shows up automatically.
Not yet wired into AppModel."
```

---

## Task 3: Explorer — ResumeStore, auto-resume, persist-on-select

**Files:**
- Modify: `internal/tui/panes/explorer.go`
- Modify: `internal/tui/panes/panes_test.go`

### Steps

- [ ] **Step 3.1 — Add ResumeStore interface and update Explorer struct**

In `internal/tui/panes/explorer.go`, just below the `package panes` import block (after line 28), add:

```go
// ResumeStore is the slice of *config.Config that Explorer needs in
// order to persist and read the last-selected context/namespace.
// Declaring it here keeps panes free of a direct config import.
type ResumeStore interface {
	LastContext() string
	LastNamespace() string
	SetLastContext(string)
	SetLastNamespace(string)
	Save() error
}
```

In the `Explorer` struct (around line 66), add the field at the bottom of the struct (before the closing `}` after `formInfo`):

```go
	resume ResumeStore
```

- [ ] **Step 3.2 — Update NewExplorer signature**

Replace the existing `NewExplorer(k core.KubeClient, kubeErr error, s *styles.Styles) *Explorer` signature and add `cfg ResumeStore` as a 4th parameter. Updated function:

```go
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
	e.list = list.New(nil, core.NewItemDelegate(s), 0, 0)

	switch {
	case kubeErr != nil:
		e.list.Title = "Kubernetes Error"
	case k != nil:
		e.kubeReady = true
		e.initView(0, 0)
	default:
		e.list.Title = "Connecting to Kubernetes…"
	}
	return e
}
```

- [ ] **Step 3.3 — Add Selection accessor**

Append to `internal/tui/panes/explorer.go` (anywhere after `NewExplorer`):

```go
// Selection returns the currently selected context and namespace, either
// of which may be empty if the user has not yet picked them.
func (e *Explorer) Selection() (ctx, ns string) { return e.ctx, e.ns }
```

- [ ] **Step 3.4 — Run build to catch all call sites**

Run: `go build ./...`
Expected: FAIL — call sites `panes.NewExplorer(...)` in `internal/tui/app.go` and `internal/tui/app_test.go` and `internal/tui/panes/panes_test.go` need updating. That's done in later steps; for now, fix only the panes_test.go file so the panes package compiles.

Find every `NewExplorer(` call in `internal/tui/panes/panes_test.go` and add `nil` as the 4th argument (a `nil` ResumeStore is fine for tests that don't exercise resume). Example: `NewExplorer(nil, errors.New("no kube"), s)` → `NewExplorer(nil, errors.New("no kube"), s, nil)`.

Run: `go build ./internal/tui/panes/`
Expected: PASS.

- [ ] **Step 3.5 — Write failing tests for persist-on-select and auto-resume**

Append to `internal/tui/panes/panes_test.go`:

```go
// fakeResume is a ResumeStore for tests that records writes.
type fakeResume struct {
	ctx       string
	ns        string
	saveCalls int
	saveErr   error
}

func (f *fakeResume) LastContext() string         { return f.ctx }
func (f *fakeResume) LastNamespace() string       { return f.ns }
func (f *fakeResume) SetLastContext(ctx string)   { f.ctx = ctx }
func (f *fakeResume) SetLastNamespace(ns string)  { f.ns = ns }
func (f *fakeResume) Save() error                 { f.saveCalls++; return f.saveErr }

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

```

- [ ] **Step 3.6 — Run tests, expect FAILs**

Run: `go test ./internal/tui/panes/ -run "TestExplorerPersists|TestExplorerAutoResume" -v`
Expected: FAILs — auto-resume not implemented; persist-on-select not implemented.

- [ ] **Step 3.7 — Implement auto-resume in SetKubeClient**

Replace the existing `SetKubeClient` body in `explorer.go` with:

```go
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
	if !containsString(k.GetContexts(), savedCtx) {
		e.resume.SetLastContext("")
		e.resume.SetLastNamespace("")
		saveCmd := e.saveResume()
		e.step = stepContext
		e.initView(e.width, e.height)
		return tea.Batch(saveCmd, func() tea.Msg {
			return overlays.ToastMsg{Kind: overlays.ToastInfo, Text: "saved context " + savedCtx + " not in kubeconfig, starting fresh"}
		})
	}

	k2, cerr := kube.NewClient(savedCtx)
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

// containsString reports whether v is present in xs.
func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// saveResume returns a tea.Cmd that calls Save() and emits a toast on
// failure. Safe to call when e.resume == nil — returns nil in that case.
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
```

- [ ] **Step 3.8 — Wire persist-on-select into handleSelect**

In `internal/tui/panes/explorer.go`, find the `case stepContext:` block inside `handleSelect` (around line 405). The persist happens **before** the `kube.NewClient(val)` call — user intent is recorded regardless of whether the client switch succeeds. The full updated block:

```go
	case stepContext:
		if e.resume != nil {
			e.resume.SetLastContext(val)
			e.resume.SetLastNamespace("")
		}
		k, err := kube.NewClient(val)
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
```

In the `case stepNamespace:` block (just below), persist after `e.ns = val`:

```go
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
```

- [ ] **Step 3.9 — Run tests, expect PASS**

Run: `go test ./internal/tui/panes/ -run "TestExplorerPersists|TestExplorerAutoResume" -v`
Expected: all PASS.

- [ ] **Step 3.10 — Run all pane tests for regressions**

Run: `go test ./internal/tui/panes/ -v`
Expected: all PASS.

- [ ] **Step 3.11 — Commit**

```bash
git add internal/tui/panes/explorer.go internal/tui/panes/panes_test.go
git commit -m "explorer: auto-resume to last selected ctx/ns and persist on select

Add ResumeStore interface (satisfied by *config.Config) and a Selection
accessor. SetKubeClient now jumps the wizard to stepNamespace or stepPod
when a valid LastContext/LastNamespace is found, clears them otherwise.
handleSelect writes the new value through ResumeStore and triggers a
fire-and-forget Save tea.Cmd that surfaces errors via toast."
```

---

## Task 4: AppModel — breadcrumb header + wire `?` to Help modal

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/app_test.go`

### Steps

- [ ] **Step 4.1 — Update AppModel struct and constructor**

In `internal/tui/app.go`, change the `AppModel` struct (around line 29). Replace the line `help      help.Model` with two fields:

```go
	footerHelp  help.Model
	helpOverlay *overlays.Help
```

In `NewAppModel` (around line 49), update the explorer construction and add the help overlay. Replace `panes.NewExplorer(nil, nil, s)` with `panes.NewExplorer(nil, nil, s, cfg)`, and replace `help: help.New(),` with:

```go
		footerHelp:  help.New(),
		helpOverlay: &overlays.Help{},
```

- [ ] **Step 4.2 — Wire `?` and intercept keys while modal open**

In `Update`, replace the existing `case key.Matches(msg, core.Keys.Help):` branch with key interception. Replace the entire `case tea.KeyMsg:` block (the one between lines 109 and 134 in the current code) with:

```go
	case tea.KeyMsg:
		// While the help modal is open, intercept all keys. ?/esc/q
		// close it; everything else is absorbed so the pane underneath
		// doesn't react. ctrl+c still quits (not trapped here — falls
		// through to Quit binding which matches "q" and "ctrl+c").
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
```

Also in `Update`, change the existing `tea.WindowSizeMsg` branch to set `m.footerHelp.Width` instead of `m.help.Width`. Replace `m.help.Width = paneW` with:

```go
		m.footerHelp.Width = paneW
```

- [ ] **Step 4.3 — Add breadcrumb to View and adjust paneSize**

In `internal/tui/app.go`, change `paneSize` (around line 229). Replace its body with:

```go
func (m *AppModel) paneSize() (int, int) {
	h, v := m.styles.Window.GetFrameSize()
	// 6 = tab row + divider + footer line + spacing (as before).
	// 2 more = breadcrumb row + secondary divider.
	return m.width - h, m.height - v - 8
}
```

In `View` (around line 236), insert breadcrumb rendering between the `divider` line and the `header := lipgloss.JoinVertical(...)` line:

```go
	ctx, ns := m.explorer.Selection()
	breadcrumb := renderBreadcrumb(ctx, ns, m.styles)
	divider2 := lipgloss.NewStyle().
		Border(lipgloss.Border{Bottom: "─"}, false, false, true, false).
		BorderForeground(m.styles.Palette.Surface()).
		Width(paneW).
		Render("")
	header := lipgloss.JoinVertical(lipgloss.Left, tabRow, divider, breadcrumb, divider2)
```

Replace the old `header := lipgloss.JoinVertical(lipgloss.Left, tabRow, divider)` line entirely with the block above.

Replace the existing `helpView := m.styles.Footer.Render(m.help.View(core.Keys))` with:

```go
	helpView := m.styles.Footer.Render(m.footerHelp.View(core.Keys))
```

At the end of `View`, before the final `return m.styles.Window.Width(...).Height(...).Render(joined)`, layer the help modal on top:

```go
	rendered := m.styles.Window.
		Width(m.width - 2).
		Height(m.height - 2).
		Render(joined)

	if m.helpOverlay.Visible {
		overlay := m.helpOverlay.View(m.width, m.height, m.styles)
		return overlay // lipgloss.Place inside Help.View already centers on the whole screen
	}
	return rendered
```

Replace the existing trailing `return m.styles.Window...` block with the block above.

Append a helper at the bottom of the file:

```go
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
```

- [ ] **Step 4.4 — Update existing tests that reference m.help**

In `internal/tui/app_test.go`, fix two failures the previous step introduces:

1. In `newTestAppModel` (around line 21), update the Explorer call: replace `panes.NewExplorer(nil, kubeErr, s)` with `panes.NewExplorer(nil, kubeErr, s, cfg)`. The current code has no `help` or `footerHelp` field set in the struct literal, so just append two new fields to the literal:

```go
		footerHelp:  help.New(),
		helpOverlay: &overlays.Help{},
```

The `help` import is already in `app_test.go`'s file via the existing dependency on `*help.Model`; if a stale compile complains, ensure `"github.com/charmbracelet/bubbles/help"` is in the import block.

2. The test `TestAppModelHelpToggle` (around line 140) — replace `am.help.ShowAll` with `am.helpOverlay.Visible`. Updated test:

```go
func TestAppModelHelpToggle(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}
	next, _ := m.Update(msg)
	am := next.(*AppModel)
	if !am.helpOverlay.Visible {
		t.Error("? key should open the help modal")
	}
}
```

- [ ] **Step 4.5 — Add new tests for modal behavior and breadcrumb**

Append to `internal/tui/app_test.go`:

```go
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
```

Add the `strings` import to `app_test.go` if not already present.

- [ ] **Step 4.6 — Run tests, expect PASS**

Run: `go test ./internal/tui/ -v`
Expected: all PASS, including the new and updated tests.

- [ ] **Step 4.7 — Run full repo build and lint**

Run: `make build && make test && make lint`
Expected: all green.

- [ ] **Step 4.8 — Manual smoke (optional but recommended)**

Run: `./bin/kapctl`
Confirm:
- Header shows `(no context selected)` before picking anything.
- After selecting `ctx → ns`, header shows `ctx: X  •  ns: Y`.
- Quit (`q`), restart, lands directly on the Pod list for the saved ctx/ns.
- `?` opens centered help modal; `esc` / `?` / `q` close it.
- While modal is open, `]` / `[` / `enter` do nothing.

- [ ] **Step 4.9 — Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "tui: persistent ctx/ns header and help modal

Render a breadcrumb above every pane sourced from Explorer.Selection,
swap the ?-toggles-ShowAll behavior for an overlay Help modal, and
absorb non-close keys while the modal is open. paneSize loses two
rows to make room for the new header."
```

---

## Self-Review Notes (pre-execution)

- **Spec coverage:** every requirement in `docs/superpowers/specs/2026-05-21-tui-header-recents-help-design.md` is mapped: persist fields → Task 1; modal overlay → Task 2; auto-resume + persist-on-select → Task 3; breadcrumb + key wiring + paneSize → Task 4.
- **No placeholders:** every step shows code.
- **Type consistency:** `ResumeStore` defined in Task 3 with exactly the methods that `*config.Config` gets in Task 1. `Help{Visible bool}` is consistent across Tasks 2 and 4. `Explorer.Selection()` is defined in Task 3 and consumed in Task 4.
- **Test order:** tests are written before the code that satisfies them, per TDD; each task ends with a green run.
