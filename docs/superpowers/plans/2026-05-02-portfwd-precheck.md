# Port-forward pre-check + custom port mapping — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Explorer's "pick port → start kubectl" flow with "pick port → confirm/edit form → validate → start", supporting custom local→remote mappings and auto-bumping the local port when the host already binds it.

**Architecture:** A new wizard step `stepPortForm` is added to the Explorer FSM after `stepPort`. The picker gains a `CUSTOM` entry (label `custom (edit ports)`); selecting any entry transitions into a two-field form (`bubbles/textinput` × 2) with synchronous validation on submit. Validation helpers live in `internal/portfwd/precheck.go` (host-port probe + range scan) and `internal/kube/types.go` (pod-spec port lookup).

**Tech Stack:** Go 1.26, Bubble Tea (`github.com/charmbracelet/bubbletea`), bubbles (`bubbles/textinput`, `bubbles/key`, `bubbles/list`, `bubbles/table`), lipgloss, k8s.io/client-go.

**Spec:** `docs/superpowers/specs/2026-05-02-portfwd-precheck-design.md`

---

## File Map

| File | Status | Responsibility |
| --- | --- | --- |
| `internal/kube/types.go` | modify | Add `HasContainerPort([]ContainerPort, int32) bool` next to `ContainerPort`. |
| `internal/kube/types_test.go` | create | Unit test for `HasContainerPort`. |
| `internal/portfwd/precheck.go` | create | `IsLocalPortFree(int) error`, `FindFreeLocalPort(start, span int) (int, error)`. |
| `internal/portfwd/precheck_test.go` | create | Unit tests for both functions. |
| `internal/tui/overlays/portselect.go` | modify | Prepend `── CUSTOM ──` group, add `customPortLabel` constant, add `IsCustomPortChoice`. |
| `internal/tui/overlays/portselect_test.go` | modify | Update `TestBuildPortChoices`; add `TestIsCustomPortChoice`. |
| `internal/tui/panes/explorer.go` | modify | Add `stepPortForm` to iota; form fields (`formLocal`, `formRemote`, `formFocus`, `formErr`, `formInfo`); `enterPortForm`, `submitPortForm`; render branch for the form; route picker selection through the form. |
| `internal/tui/panes/panes_test.go` | modify | Add `TestExplorerPortForm*` cases. |
| `CLAUDE.md` | modify | Update "Port-forward defaults" section with one paragraph about the form + pre-checks. |

---

## Task 1: `kube.HasContainerPort` helper

**Files:**
- Modify: `internal/kube/types.go`
- Create: `internal/kube/types_test.go`

- [ ] **Step 1: Write failing test in `internal/kube/types_test.go`**

```go
package kube

import "testing"

func TestHasContainerPort(t *testing.T) {
	ports := []ContainerPort{
		{Name: "http", Port: 8080},
		{Name: "metrics", Port: 9090},
	}
	cases := []struct {
		name string
		p    int32
		want bool
	}{
		{"present-first", 8080, true},
		{"present-second", 9090, true},
		{"absent", 1234, false},
		{"zero", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasContainerPort(ports, c.p); got != c.want {
				t.Errorf("HasContainerPort(_, %d) = %v, want %v", c.p, got, c.want)
			}
		})
	}
	if HasContainerPort(nil, 80) {
		t.Error("HasContainerPort(nil, 80) = true, want false")
	}
}
```

- [ ] **Step 2: Run the test — expect FAIL**

```
go test ./internal/kube -run TestHasContainerPort -v
```

Expected: build error or `undefined: HasContainerPort`.

- [ ] **Step 3: Add `HasContainerPort` to `internal/kube/types.go`**

Append to the end of the file (after the `ContainerPort` type):

```go
// HasContainerPort reports whether ports declares the given port number.
func HasContainerPort(ports []ContainerPort, port int32) bool {
	for _, p := range ports {
		if p.Port == port {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the test — expect PASS**

```
go test ./internal/kube -run TestHasContainerPort -v
```

Expected: `--- PASS: TestHasContainerPort` and all subtests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/kube/types.go internal/kube/types_test.go
git commit -m "feat(kube): add HasContainerPort helper for pod-spec port lookup"
```

---

## Task 2: `portfwd.IsLocalPortFree` host-port probe

**Files:**
- Create: `internal/portfwd/precheck.go`
- Create: `internal/portfwd/precheck_test.go`

- [ ] **Step 1: Write failing test in `internal/portfwd/precheck_test.go`**

```go
package portfwd

import (
	"net"
	"strconv"
	"testing"
)

// listenLocal binds 127.0.0.1:0 and returns the listener and the
// allocated port. Caller must Close() the listener.
func listenLocal(t *testing.T) (net.Listener, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		_ = l.Close()
		t.Fatalf("split host/port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		_ = l.Close()
		t.Fatalf("atoi: %v", err)
	}
	return l, port
}

func TestIsLocalPortFree_Occupied(t *testing.T) {
	l, port := listenLocal(t)
	defer l.Close()
	if err := IsLocalPortFree(port); err == nil {
		t.Errorf("IsLocalPortFree(%d) = nil, want error", port)
	}
}

func TestIsLocalPortFree_AfterClose(t *testing.T) {
	l, port := listenLocal(t)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := IsLocalPortFree(port); err != nil {
		t.Errorf("IsLocalPortFree(%d) after close = %v, want nil", port, err)
	}
}
```

- [ ] **Step 2: Run the test — expect FAIL**

```
go test ./internal/portfwd -run TestIsLocalPortFree -v
```

Expected: `undefined: IsLocalPortFree`.

- [ ] **Step 3: Create `internal/portfwd/precheck.go`**

```go
package portfwd

import (
	"net"
	"strconv"
)

// IsLocalPortFree probes whether 127.0.0.1:port can be bound. The
// listener is closed immediately. Returns nil if free, a non-nil error
// if the port is in use or the bind fails for any other reason.
func IsLocalPortFree(port int) error {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return l.Close()
}
```

- [ ] **Step 4: Run the test — expect PASS**

```
go test ./internal/portfwd -run TestIsLocalPortFree -v
```

Expected: both subtests `--- PASS`.

- [ ] **Step 5: Commit**

```bash
git add internal/portfwd/precheck.go internal/portfwd/precheck_test.go
git commit -m "feat(portfwd): add IsLocalPortFree host-port probe"
```

---

## Task 3: `portfwd.FindFreeLocalPort` range scan

**Files:**
- Modify: `internal/portfwd/precheck.go`
- Modify: `internal/portfwd/precheck_test.go`

- [ ] **Step 1: Append failing tests to `internal/portfwd/precheck_test.go`**

```go
func TestFindFreeLocalPort_StartFree(t *testing.T) {
	l, port := listenLocal(t)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := FindFreeLocalPort(port, 10)
	if err != nil {
		t.Fatalf("FindFreeLocalPort: %v", err)
	}
	if got != port {
		t.Errorf("got %d, want %d (start was free)", got, port)
	}
}

func TestFindFreeLocalPort_BumpsPastOccupied(t *testing.T) {
	// Occupy three consecutive ports: start, start+1, start+2.
	l1, start := listenLocal(t)
	defer l1.Close()
	l2, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(start+1)))
	if err != nil {
		t.Skipf("could not bind start+1=%d: %v", start+1, err)
	}
	defer l2.Close()
	l3, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(start+2)))
	if err != nil {
		t.Skipf("could not bind start+2=%d: %v", start+2, err)
	}
	defer l3.Close()

	got, err := FindFreeLocalPort(start, 10)
	if err != nil {
		t.Fatalf("FindFreeLocalPort: %v", err)
	}
	if got <= start+2 {
		t.Errorf("got %d, want > %d", got, start+2)
	}
}

func TestFindFreeLocalPort_NegativeSpan(t *testing.T) {
	if _, err := FindFreeLocalPort(8080, -1); err == nil {
		t.Error("FindFreeLocalPort(_, -1) = nil error, want non-nil")
	}
}
```

- [ ] **Step 2: Run the tests — expect FAIL**

```
go test ./internal/portfwd -run TestFindFreeLocalPort -v
```

Expected: `undefined: FindFreeLocalPort`.

- [ ] **Step 3: Append `FindFreeLocalPort` to `internal/portfwd/precheck.go`**

Add `"fmt"` to the imports, then append:

```go
// FindFreeLocalPort returns the first port in [start, start+span] for
// which IsLocalPortFree succeeds. Returns 0 and a non-nil error if no
// port in the range is free. Out-of-range candidates (<1 or >65535)
// are skipped. span must be >= 0.
func FindFreeLocalPort(start, span int) (int, error) {
	if span < 0 {
		return 0, fmt.Errorf("portfwd: negative span %d", span)
	}
	for p := start; p <= start+span; p++ {
		if p < 1 || p > 65535 {
			continue
		}
		if err := IsLocalPortFree(p); err == nil {
			return p, nil
		}
	}
	return 0, fmt.Errorf("portfwd: no free port in [%d, %d]", start, start+span)
}
```

- [ ] **Step 4: Run the tests — expect PASS**

```
go test ./internal/portfwd -run TestFindFreeLocalPort -v
```

Expected: all three subtests `--- PASS`.

- [ ] **Step 5: Commit**

```bash
git add internal/portfwd/precheck.go internal/portfwd/precheck_test.go
git commit -m "feat(portfwd): add FindFreeLocalPort range scan"
```

---

## Task 4: CUSTOM entry in `BuildPortChoices` + `IsCustomPortChoice`

**Files:**
- Modify: `internal/tui/overlays/portselect.go`
- Modify: `internal/tui/overlays/portselect_test.go`

- [ ] **Step 1: Write failing tests — replace and extend `portselect_test.go`**

Add at the end of the existing `portselect_test.go`:

```go
func TestIsCustomPortChoice(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"custom (edit ports)", true},
		{"  custom (edit ports)  ", true},
		{"5432 (postgresql)", false},
		{"── PRIORITY ──", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := IsCustomPortChoice(c.in); got != c.want {
				t.Errorf("IsCustomPortChoice(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestBuildPortChoices_CustomFirst(t *testing.T) {
	choices := BuildPortChoices(nil)
	if len(choices) < 2 {
		t.Fatalf("choices too short: %v", choices)
	}
	if choices[0] != "── CUSTOM ──" {
		t.Errorf("choices[0] = %q, want \"── CUSTOM ──\"", choices[0])
	}
	if choices[1] != "custom (edit ports)" {
		t.Errorf("choices[1] = %q, want \"custom (edit ports)\"", choices[1])
	}
}
```

- [ ] **Step 2: Run the tests — expect FAIL**

```
go test ./internal/tui/overlays -run "TestIsCustomPortChoice|TestBuildPortChoices_CustomFirst" -v
```

Expected: `undefined: IsCustomPortChoice` and `TestBuildPortChoices_CustomFirst` fails because `choices[0]` is `── PRIORITY ──`.

- [ ] **Step 3: Modify `internal/tui/overlays/portselect.go`**

Add a constant near the top of the file, just after the `import` block:

```go
const customPortLabel = "custom (edit ports)"

// IsCustomPortChoice reports whether the picker label is the CUSTOM
// sentinel. ParsePort errors on this string by design — callers must
// branch on IsCustomPortChoice first.
func IsCustomPortChoice(label string) bool {
	return strings.TrimSpace(label) == customPortLabel
}
```

In `BuildPortChoices`, prepend the CUSTOM group before the existing PRIORITY block. Replace the line `out = append(out, "── PRIORITY ──")` with:

```go
out = append(out, "── CUSTOM ──")
out = append(out, customPortLabel)
out = append(out, "── PRIORITY ──")
```

- [ ] **Step 4: Run the new tests — expect PASS**

```
go test ./internal/tui/overlays -run "TestIsCustomPortChoice|TestBuildPortChoices_CustomFirst" -v
```

Expected: both `--- PASS`.

- [ ] **Step 5: Run the full overlays test suite — expect PASS**

```
go test ./internal/tui/overlays -v
```

Expected: existing `TestBuildPortChoices` still passes (it only checks for presence of separators/values via `contains()`, no order assertions, so the prepended CUSTOM group does not break it).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/overlays/portselect.go internal/tui/overlays/portselect_test.go
git commit -m "feat(overlays): prepend CUSTOM entry to port picker choices"
```

---

## Task 5: Explorer — add `stepPortForm`, form state, `enterPortForm`

This task introduces the new step and the entry transition. Submit logic + rendering are added in subsequent tasks; for now `enterPortForm` only sets state and the form is not yet rendered or interactive.

**Files:**
- Modify: `internal/tui/panes/explorer.go`
- Modify: `internal/tui/panes/panes_test.go`

- [ ] **Step 1: Write failing test in `internal/tui/panes/panes_test.go`**

Append at the end of the file:

```go
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
```

Add these imports to `panes_test.go` (next to the existing `"context"`, `"errors"`, `"testing"` block):

```go
"net"
"strconv"
```

- [ ] **Step 2: Run the test — expect FAIL**

```
go test ./internal/tui/panes -run TestExplorerEnterPortForm -v
```

Expected: build errors — `undefined: stepPortForm`, `e.formLocal undefined`, `e.formRemote undefined`, `e.formInfo undefined`, `e.enterPortForm undefined`.

- [ ] **Step 3: Modify `internal/tui/panes/explorer.go`**

3a. Add the textinput import — replace the import block (lines 7-26) with the same set plus `"github.com/charmbracelet/bubbles/textinput"`:

```go
import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kap-toolsbox/internal/kube"
	"github.com/billygate/kap-toolsbox/internal/portfwd"
	"github.com/billygate/kap-toolsbox/internal/tui/core"
	"github.com/billygate/kap-toolsbox/internal/tui/overlays"
	"github.com/billygate/kap-toolsbox/internal/tui/styles"
)
```

3b. Append `stepPortForm` to the existing iota block. Replace lines 36-43 (the const block) with:

```go
type explorerStep int

const (
	stepContext explorerStep = iota
	stepNamespace
	stepPod
	stepAction
	stepPort
	stepPortForm
)
```

3c. Add the form fields to the `Explorer` struct. After the existing field block (around line 84, before the closing brace of `Explorer`), insert:

```go
	// Port-form fields. Populated only when step == stepPortForm.
	formLocal  textinput.Model
	formRemote textinput.Model
	formFocus  int    // 0 = local, 1 = remote
	formErr    string // styles.Warn — blocks submit
	formInfo   string // styles.Muted — informational (auto-bump hint)
```

3d. Add `enterPortForm` near the bottom of the file (before `runAction`):

```go
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
```

- [ ] **Step 4: Run the test — expect PASS**

```
go test ./internal/tui/panes -run TestExplorerEnterPortForm -v
```

Expected: both subtests `--- PASS`.

- [ ] **Step 5: Run `go vet ./...` and `go build ./...`**

```
go vet ./... && go build ./...
```

Expected: clean output. The form fields are unreferenced outside `enterPortForm`/tests at this point — that is intentional; rendering and submit follow.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/panes/explorer.go internal/tui/panes/panes_test.go
git commit -m "feat(explorer): add stepPortForm + enterPortForm with auto-bump"
```

---

## Task 6: Explorer — render the form

**Files:**
- Modify: `internal/tui/panes/explorer.go`
- Modify: `internal/tui/panes/panes_test.go`

- [ ] **Step 1: Write failing test in `internal/tui/panes/panes_test.go`**

Append:

```go
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
```

Ensure `"strings"` is imported in `panes_test.go` (it currently is not — add it to the import block).

- [ ] **Step 2: Run the test — expect FAIL**

```
go test ./internal/tui/panes -run TestExplorerViewRendersPortForm -v
```

Expected: failing assertions — `View` does not yet recognise `stepPortForm` and falls through to the bubbles/list path which has no "Local"/"Remote" labels.

- [ ] **Step 3: Modify `View` in `internal/tui/panes/explorer.go`**

Replace the existing `View` method (around lines 513-548) with:

```go
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

	title := e.styles.Title.Render(e.viewTitle())

	var content string
	switch {
	case e.step == stepPortForm:
		content = e.viewPortForm()
	case e.step == stepPod:
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
```

Update `viewTitle` to handle the new step. Replace the existing `viewTitle` (around lines 551-565) with:

```go
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
```

- [ ] **Step 4: Run the test — expect PASS**

```
go test ./internal/tui/panes -run TestExplorerViewRendersPortForm -v
```

Expected: `--- PASS`.

- [ ] **Step 5: Run the full panes test suite**

```
go test ./internal/tui/panes -v
```

Expected: all existing tests still pass (the `View` change only adds a new case; non-port-form paths are unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/tui/panes/explorer.go internal/tui/panes/panes_test.go
git commit -m "feat(explorer): render stepPortForm with two textinputs + hints"
```

---

## Task 7: Explorer — form key routing + submit

**Files:**
- Modify: `internal/tui/panes/explorer.go`
- Modify: `internal/tui/panes/panes_test.go`

- [ ] **Step 1: Write failing tests in `internal/tui/panes/panes_test.go`**

Append:

```go
func TestExplorerFormSubmitValid(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.ctx = "ctx-a"
	e.ns = "billing"
	e.pod = "pg-0"
	e.action = "port-forward"
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}

	// Bind a known-busy local so we can choose a different one explicitly.
	e.enterPortForm(0, 0)
	e.formLocal.SetValue("18080")
	e.formRemote.SetValue("5432")

	_, cmd := e.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submit produced nil cmd, want PortForwardRequestMsg")
	}
	msg := cmd()
	req, ok := msg.(core.PortForwardRequestMsg)
	if !ok {
		t.Fatalf("msg type = %T, want core.PortForwardRequestMsg", msg)
	}
	if req.LocalPort != 18080 || req.RemotePort != 5432 {
		t.Errorf("ports = %d/%d, want 18080/5432", req.LocalPort, req.RemotePort)
	}
	if e.step != stepPort {
		t.Errorf("step after successful submit = %v, want stepPort", e.step)
	}
}

func TestExplorerFormSubmitRemoteNotInPodSpec(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}

	e.enterPortForm(0, 0)
	e.formLocal.SetValue("18080")
	e.formRemote.SetValue("9999") // not in pod spec

	_, cmd := e.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		// The cmd, if any, must NOT be a PortForwardRequestMsg.
		if msg := cmd(); msg != nil {
			if _, isReq := msg.(core.PortForwardRequestMsg); isReq {
				t.Fatal("submit emitted PortForwardRequestMsg despite invalid remote port")
			}
		}
	}
	if e.step != stepPortForm {
		t.Errorf("step = %v, want stepPortForm (stay)", e.step)
	}
	if e.formErr == "" {
		t.Error("formErr should be non-empty for invalid remote")
	}
}

func TestExplorerFormEscBacksToPicker(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}
	e.enterPortForm(5432, 5432)

	_, _ = e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if e.step != stepPort {
		t.Errorf("step = %v, want stepPort after esc", e.step)
	}
}

func TestExplorerFormTabSwitchesFocus(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}
	e.enterPortForm(5432, 5432)

	if e.formFocus != 0 {
		t.Fatalf("initial formFocus = %d, want 0", e.formFocus)
	}
	_, _ = e.Update(tea.KeyMsg{Type: tea.KeyTab})
	if e.formFocus != 1 {
		t.Errorf("formFocus after tab = %d, want 1", e.formFocus)
	}
}
```

- [ ] **Step 2: Run the tests — expect FAIL**

```
go test ./internal/tui/panes -run TestExplorerForm -v
```

Expected: failures — `Update` does not yet route keys for `stepPortForm`; the form behaves as a bubbles/list which won't emit `PortForwardRequestMsg`.

- [ ] **Step 3: Modify `Update` and add `submitPortForm` in `internal/tui/panes/explorer.go`**

3a. Add a new branch at the top of the `KeyMsg` handling in `Update`. Find the block (around lines 186-202):

```go
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
```

Replace it with:

```go
keyMsg, ok := msg.(tea.KeyMsg)
if !ok {
	switch {
	case e.step == stepPortForm:
		return e.updatePortForm(msg)
	case e.usingTable():
		var cmd tea.Cmd
		e.podTable, cmd = e.podTable.Update(msg)
		return e, cmd
	}
	var cmd tea.Cmd
	e.list, cmd = e.list.Update(msg)
	return e, cmd
}

switch {
case e.step == stepPortForm:
	return e.updatePortForm(keyMsg)
case e.usingTable():
	return e.updatePodTable(keyMsg)
}
return e.updateList(keyMsg)
```

3b. Append `updatePortForm` and `submitPortForm` at the bottom of the file (before `runAction`):

```go
// updatePortForm handles all messages while step == stepPortForm.
// Tab/shift-tab switch focus, enter submits, esc returns to the
// picker. Everything else is forwarded to the focused textinput.
func (e *Explorer) updatePortForm(msg tea.Msg) (*Explorer, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if !isKey {
		// Forward non-key messages (e.g. blink) to both inputs.
		var c1, c2 tea.Cmd
		e.formLocal, c1 = e.formLocal.Update(msg)
		e.formRemote, c2 = e.formRemote.Update(msg)
		return e, tea.Batch(c1, c2)
	}

	switch keyMsg.Type {
	case tea.KeyEsc:
		e.step = stepPort
		return e, nil
	case tea.KeyEnter:
		return e.submitPortForm()
	case tea.KeyTab:
		e.formFocus = 1 - e.formFocus
		e.applyFormFocus()
		return e, nil
	case tea.KeyShiftTab:
		e.formFocus = 1 - e.formFocus
		e.applyFormFocus()
		return e, nil
	}

	var cmd tea.Cmd
	if e.formFocus == 0 {
		e.formLocal, cmd = e.formLocal.Update(keyMsg)
	} else {
		e.formRemote, cmd = e.formRemote.Update(keyMsg)
	}
	return e, cmd
}

func (e *Explorer) applyFormFocus() {
	if e.formFocus == 0 {
		e.formLocal.Focus()
		e.formRemote.Blur()
	} else {
		e.formLocal.Blur()
		e.formRemote.Focus()
	}
}

// submitPortForm validates both fields and either emits a
// PortForwardRequestMsg (returning the wizard to stepPort) or sets
// formErr and stays on stepPortForm.
func (e *Explorer) submitPortForm() (*Explorer, tea.Cmd) {
	local, lerr := parsePortField(e.formLocal.Value())
	if lerr != nil {
		e.formErr = "local: " + lerr.Error()
		return e, nil
	}
	remote, rerr := parsePortField(e.formRemote.Value())
	if rerr != nil {
		e.formErr = "remote: " + rerr.Error()
		return e, nil
	}
	if err := portfwd.IsLocalPortFree(local); err != nil {
		e.formErr = fmt.Sprintf("local port %d in use", local)
		return e, nil
	}
	if !kube.HasContainerPort(e.podPorts, int32(remote)) {
		e.formErr = fmt.Sprintf("remote port %d not declared in pod spec", remote)
		return e, nil
	}
	e.formErr = ""
	e.formInfo = ""
	ctx, ns, pod := e.ctx, e.ns, e.pod
	e.step = stepPort
	return e, func() tea.Msg {
		return core.PortForwardRequestMsg{
			Context: ctx, Namespace: ns, Target: pod,
			Kind:       portfwd.KindPod,
			LocalPort:  local,
			RemotePort: remote,
		}
	}
}

// parsePortField parses a textinput value into a TCP port number.
// Returns an error if the field is empty or out of [1, 65535].
func parsePortField(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("port is required")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", v)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("out of range: %d", n)
	}
	return n, nil
}
```

- [ ] **Step 4: Run the new tests — expect PASS**

```
go test ./internal/tui/panes -run TestExplorerForm -v
```

Expected: all four `TestExplorerForm*` subtests `--- PASS`.

- [ ] **Step 5: Run the full repo test suite — expect PASS**

```
go test ./...
```

Expected: clean — no regressions in other packages.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/panes/explorer.go internal/tui/panes/panes_test.go
git commit -m "feat(explorer): wire port-form key routing + validate-on-submit"
```

---

## Task 8: Wire picker → form

**Files:**
- Modify: `internal/tui/panes/explorer.go`
- Modify: `internal/tui/panes/panes_test.go`

- [ ] **Step 1: Write failing tests in `internal/tui/panes/panes_test.go`**

Append:

```go
func TestExplorerStepPortPickerOpensForm(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.SetSize(80, 24)
	e.ctx = "ctx-a"
	e.ns = "billing"
	e.pod = "pg-0"
	e.action = "port-forward"
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}
	e.step = stepPort
	e.initView(80, 24)

	// Find the index of "5432 (postgresql)" in the rendered list.
	target := -1
	for i, it := range e.list.Items() {
		li, ok := core.AsListItem(it)
		if !ok {
			continue
		}
		if li.Text == "5432 (postgresql)" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("could not locate 5432 in picker items")
	}
	e.list.Select(target)

	_, _ = e.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if e.step != stepPortForm {
		t.Errorf("step after select 5432 = %v, want stepPortForm", e.step)
	}
	if e.formRemote.Value() != "5432" {
		t.Errorf("formRemote = %q, want %q", e.formRemote.Value(), "5432")
	}
}

func TestExplorerStepPortCustomOpensEmptyForm(t *testing.T) {
	s := newTestStyles()
	mk := &mockKubeClient{contexts: []string{"ctx-a"}}
	e := NewExplorer(mk, nil, s)
	e.SetSize(80, 24)
	e.ctx = "ctx-a"
	e.ns = "billing"
	e.pod = "pg-0"
	e.action = "port-forward"
	e.podPorts = []kube.ContainerPort{{Name: "pg", Port: 5432}}
	e.step = stepPort
	e.initView(80, 24)

	target := -1
	for i, it := range e.list.Items() {
		li, ok := core.AsListItem(it)
		if !ok {
			continue
		}
		if li.Text == "custom (edit ports)" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("could not locate custom entry in picker items")
	}
	e.list.Select(target)

	_, _ = e.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if e.step != stepPortForm {
		t.Errorf("step after select custom = %v, want stepPortForm", e.step)
	}
	if e.formLocal.Value() != "" || e.formRemote.Value() != "" {
		t.Errorf("custom should leave fields empty, got local=%q remote=%q",
			e.formLocal.Value(), e.formRemote.Value())
	}
}
```

- [ ] **Step 2: Run the tests — expect FAIL**

```
go test ./internal/tui/panes -run TestExplorerStepPort -v
```

Expected: failures — selecting from the picker still calls `runAction` directly and emits `PortForwardRequestMsg` instead of routing through the form.

- [ ] **Step 3: Modify `handleSelect` in `internal/tui/panes/explorer.go`**

Find the `case stepPort:` arm in `handleSelect` (around lines 422-423):

```go
case stepPort:
	return e, e.runAction(e.action, val)
```

Replace it with:

```go
case stepPort:
	if overlays.IsCustomPortChoice(val) {
		e.enterPortForm(0, 0)
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
```

The `runAction` function still handles the `stepAction` foreground actions (logs/exec/describe), so it remains in the file and is unchanged. The port-forward branch inside `runAction` (lines 572-588) becomes unreachable from the wizard but is harmless dead code; remove it for hygiene by replacing `runAction` with:

```go
func (e *Explorer) runAction(action, port string) tea.Cmd {
	// port-forward is now driven through stepPortForm, which emits
	// PortForwardRequestMsg directly from submitPortForm. The other
	// actions (logs, exec, describe) are foreground — tea.ExecProcess
	// suspends the TUI for their duration.
	return tea.ExecProcess(buildExplorerCmd(e.ctx, e.ns, e.pod, action, port), func(_ error) tea.Msg {
		return nil
	})
}
```

- [ ] **Step 4: Run the new tests — expect PASS**

```
go test ./internal/tui/panes -run TestExplorerStepPort -v
```

Expected: both subtests `--- PASS`.

- [ ] **Step 5: Run the full repo test suite — expect PASS**

```
go test ./... && go vet ./... && go build ./...
```

Expected: all green. Pay attention to any unused-import warnings in `explorer.go` after removing the port-forward arm of `runAction` — `portfwd.KindPod` is still referenced from `submitPortForm`, so the `portfwd` import stays.

- [ ] **Step 6: Manual smoke test**

```
make build && ./bin/kap
```

Drive the TUI: pick a context → namespace → pod → `port-forward`. Confirm:

1. Picker now shows `── CUSTOM ──` / `custom (edit ports)` as the first group.
2. Selecting a numeric entry opens the form with both fields populated.
3. Selecting `custom (edit ports)` opens the form with both fields empty.
4. Tab switches focus, esc returns to the picker, enter validates.
5. With another process holding the picked local port (e.g. `nc -l 5432` in another terminal): selecting `5432 (postgresql)` opens the form with Local already bumped to a free port and the `ℹ` hint shown.
6. Editing Remote to a port not in the pod spec and pressing enter shows the red error and keeps the form open.

Document any UI surprises (alignment, theme rendering, focus indicator) inline before committing.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/panes/explorer.go internal/tui/panes/panes_test.go
git commit -m "feat(explorer): route port picker through stepPortForm"
```

---

## Task 9: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Read the current "Port-forward defaults" section**

```
grep -n "Port-forward defaults" CLAUDE.md
```

- [ ] **Step 2: Replace the section body with the updated description**

Replace the existing paragraph in the `### Port-forward defaults` section with:

```markdown
### Port-forward defaults

`overlays.BuildPortChoices` produces a labelled menu in the order **CUSTOM → priority → detected → common**. The `── CUSTOM ──` group is first and contains a single entry `custom (edit ports)` (matched by `overlays.IsCustomPortChoice`); the priority block is `5432 postgres` and `8080 http-alt`; detected ports come from the pod spec; common is `80, 6379, 9090, 3000`. Detected/common are deduped against priority. **Preserve the CUSTOM → priority → detected → common grouping when changing this — it's the UX contract.** `overlays.ParsePort` extracts the leading integer from a label like `"5432 (postgresql)"` and returns an error on the custom sentinel.

After the user picks an entry, the Explorer transitions into `stepPortForm` — a two-field form (`bubbles/textinput` × 2) for `local` and `remote`. The form runs two synchronous pre-checks on submit: `portfwd.IsLocalPortFree` (host-level bind probe) and `kube.HasContainerPort` (pod-spec lookup); both fail closed with an inline error. On entry to the form, if the picked `local` is in use, `portfwd.FindFreeLocalPort(picked, 100)` substitutes the next free port and surfaces a muted hint. The CUSTOM entry opens the form with both fields empty.
```

- [ ] **Step 3: Run `make test` to confirm nothing broke**

```
make test
```

Expected: all packages pass.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(claude): describe stepPortForm and pre-checks in port-forward defaults"
```

---

## Self-review checklist (engineer runs at the end)

- [ ] All nine tasks committed in order; each commit message starts with the conventional prefix used elsewhere in the repo (`feat:`, `docs:`, `chore:`, `test:`).
- [ ] `make test` and `make lint` are green.
- [ ] `make cover` total coverage has not regressed below the prior baseline.
- [ ] Manual TUI walkthrough from Task 8 step 6 was performed at least once and the screen looked right.
- [ ] No new `fmt.Println` calls introduced in TUI code paths (CLAUDE.md convention).
- [ ] No new hex colour literals introduced (theme convention).
