package overlays

import (
	"strings"
	"time"

	"github.com/billygate/kap-toolsbox/internal/tui/styles"
	"github.com/charmbracelet/lipgloss"
)

// ToastKind selects the colour and prefix used to render a toast.
type ToastKind int

// Toast severity levels.
const (
	ToastInfo ToastKind = iota
	ToastSuccess
	ToastError
)

// ToastMsg is the tea.Msg flavour — emit one to push a toast onto the queue.
type ToastMsg struct {
	Kind ToastKind
	Text string
}

const toastTTL = 3 * time.Second

type entry struct {
	kind    ToastKind
	text    string
	expires time.Time
}

// Toasts is a TTL-based queue rendered in the bottom-right of the screen.
type Toasts struct {
	items []entry
}

// Push adds a toast with the default TTL.
func (q *Toasts) Push(kind ToastKind, text string) {
	q.items = append(q.items, entry{kind: kind, text: text, expires: time.Now().Add(toastTTL)})
}

// Tick drops expired entries. Call from the model on each tickMsg.
func (q *Toasts) Tick(now time.Time) {
	kept := q.items[:0]
	for _, e := range q.items {
		if now.Before(e.expires) {
			kept = append(kept, e)
		}
	}
	q.items = kept
}

// Empty reports whether there's anything to render.
func (q *Toasts) Empty() bool { return len(q.items) == 0 }

// View renders the queue as a stack of right-aligned lines.
// Returns "" if no toasts are active.
func (q *Toasts) View(width int, s *styles.Styles) string {
	if len(q.items) == 0 {
		return ""
	}
	var lines []string
	for _, e := range q.items {
		var st lipgloss.Style
		switch e.kind {
		case ToastError:
			st = s.Warn
		case ToastSuccess:
			st = s.Master
		default:
			st = s.Muted
		}
		lines = append(lines, st.Render("• "+e.text))
	}
	block := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Right).Render(block)
}
