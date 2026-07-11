package core

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/billygate/kapctl/internal/tui/styles"
)

// RowProvider is the interface a typed data record implements to be
// displayed in a Table. Cells must return one string per column; the
// column count is fixed at table creation time. FilterValue is the
// substring-matched text when the user types into the / filter.
type RowProvider interface {
	Cells() table.Row
	FilterValue() string
}

// TableFilterState mirrors the three states of bubbles/list filtering.
type TableFilterState int

// Filter state constants for Table.
const (
	TableUnfiltered    TableFilterState = iota
	TableFiltering                      // user is actively typing in the filter input
	TableFilterApplied                  // a non-empty filter is active but the user has pressed enter
)

// Table is the wrapper around bubbles/table that adds /-filter, numeric
// jump, palette wiring, and a parallel typed-data slice so callers can
// recover the selected RowProvider rather than just a []string Row.
//
// Tables don't render separators — that affordance is list-only.
type Table struct {
	inner       table.Model
	cols        []table.Column // user-supplied columns (excluding IDX)
	rowNumbers  bool           // prepend a 1-based IDX column
	allItems    []RowProvider
	visible     []int // indices into allItems
	filterIn    textinput.Model
	filterState TableFilterState
	inputBuf    string // numeric-jump buffer
	styles      *styles.Styles
}

// TableOption configures a Table at construction.
type TableOption func(*Table)

// WithRowNumbers prepends a 1-based IDX column. The numbers reflect the
// position in the *visible* (post-filter) set, matching the numeric-jump
// shortcuts.
func WithRowNumbers() TableOption {
	return func(t *Table) { t.rowNumbers = true }
}

// NewTable constructs a Table for the given columns.
func NewTable(s *styles.Styles, cols []table.Column, opts ...TableOption) *Table {
	tt := &Table{
		cols:   cols,
		styles: s,
	}
	for _, opt := range opts {
		opt(tt)
	}

	ti := textinput.New()
	ti.Prompt = "/"
	ti.CharLimit = 64
	tt.filterIn = ti

	tt.inner = table.New(
		table.WithColumns(tt.innerColumns()),
		table.WithFocused(true),
	)
	tt.applyStyles()
	return tt
}

// innerColumns returns the columns actually given to bubbles/table —
// the user-supplied set, optionally prefixed with an IDX column.
func (t *Table) innerColumns() []table.Column {
	if !t.rowNumbers {
		return t.cols
	}
	out := make([]table.Column, 0, len(t.cols)+1)
	out = append(out, table.Column{Title: "IDX", Width: 4})
	out = append(out, t.cols...)
	return out
}

// applyStyles wires the palette into the inner table's Styles.
func (t *Table) applyStyles() {
	if t.styles == nil {
		return
	}
	p := t.styles.Palette
	st := table.Styles{
		Header: lipgloss.NewStyle().
			Foreground(p.Accent()).
			Bold(true).
			Underline(true).
			Padding(0, 1),
		Cell: lipgloss.NewStyle().Padding(0, 1),
		Selected: lipgloss.NewStyle().
			Foreground(p.Primary()).
			Background(p.Surface()).
			Bold(true),
	}
	t.inner.SetStyles(st)
}

// SetItems replaces the data set. Cursor is preserved when the
// previously selected row's FilterValue still appears in the new set;
// otherwise it resets to 0.
func (t *Table) SetItems(items []RowProvider) {
	// Capture the selection key BEFORE swapping allItems — once
	// t.allItems is the new (possibly shorter) slice, the old visible
	// indices may point past its end.
	var prevKey string
	if prev := t.SelectedItem(); prev != nil {
		prevKey = prev.FilterValue()
	}
	t.allItems = items
	t.refilterWithKey(prevKey)
}

// SetSize sets the table's outer width/height (header + viewport).
func (t *Table) SetSize(w, h int) {
	t.inner.SetWidth(w)
	t.inner.SetHeight(h)
	t.filterIn.Width = w - 2
}

// SelectedItem returns the currently selected RowProvider, or nil if
// the table is empty. The bounds check is intentionally defensive: a
// caller that mutates allItems between Updates can leave visible[idx]
// pointing past the new allItems, and we'd rather return nil than
// panic at the dereference.
func (t *Table) SelectedItem() RowProvider {
	idx := t.inner.Cursor()
	if idx < 0 || idx >= len(t.visible) {
		return nil
	}
	pos := t.visible[idx]
	if pos < 0 || pos >= len(t.allItems) {
		return nil
	}
	return t.allItems[pos]
}

// FilterValue returns the current filter text (whether actively typing
// or applied).
func (t *Table) FilterValue() string {
	return t.filterIn.Value()
}

// FilterState reports the current filter mode.
func (t *Table) FilterState() TableFilterState { return t.filterState }

// ResetFilter clears the filter and exits filter mode.
func (t *Table) ResetFilter() {
	t.filterIn.SetValue("")
	t.filterIn.Blur()
	t.filterState = TableUnfiltered
	t.refilter(false)
}

// Len returns the number of items currently visible after filtering.
func (t *Table) Len() int { return len(t.visible) }

// Update is the bubbletea update loop.
//
// The pane is responsible for routing tea.KeyMsg here only when it
// wants the table to consume the event. Numeric-jump is handled inside
// — the returned bool reports whether the table consumed the key, so
// the pane can fall through (e.g. on enter to act on the selection).
func (t *Table) Update(msg tea.Msg) (*Table, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// While typing in the filter, the textinput owns the keys.
		if t.filterState == TableFiltering {
			switch {
			case key.Matches(msg, Keys.Select):
				// Commit filter — keep the value, exit typing mode.
				if t.filterIn.Value() == "" {
					t.filterState = TableUnfiltered
				} else {
					t.filterState = TableFilterApplied
				}
				t.filterIn.Blur()
				return t, nil
			case key.Matches(msg, Keys.Back):
				// Cancel filter.
				t.ResetFilter()
				return t, nil
			}
			var cmd tea.Cmd
			t.filterIn, cmd = t.filterIn.Update(msg)
			t.refilter(false)
			return t, cmd
		}

		// Not filtering. Numeric jump first.
		s := msg.String()
		if s >= "0" && s <= "9" {
			t.handleNumeric(s)
			return t, nil
		}
		t.inputBuf = ""

		switch {
		case key.Matches(msg, Keys.Filter):
			t.filterState = TableFiltering
			t.filterIn.Focus()
			return t, nil
		case key.Matches(msg, Keys.Back):
			if t.filterState == TableFilterApplied {
				t.ResetFilter()
				return t, nil
			}
		}

		var cmd tea.Cmd
		t.inner, cmd = t.inner.Update(msg)
		return t, cmd
	}

	var cmd tea.Cmd
	t.inner, cmd = t.inner.Update(msg)
	return t, cmd
}

// View renders the table plus, when relevant, the filter input line.
func (t *Table) View() string {
	body := t.inner.View()
	switch t.filterState {
	case TableFiltering:
		return lipgloss.JoinVertical(lipgloss.Left, t.filterIn.View(), body)
	case TableFilterApplied:
		hint := t.styles.Muted.Render("/" + t.filterIn.Value())
		return lipgloss.JoinVertical(lipgloss.Left, hint, body)
	}
	return body
}

// handleNumeric implements the same 1-9 / 2-digit jump rules used by
// the panes' list flows.
func (t *Table) handleNumeric(digit string) {
	buf, idx, committed := NumericJump(t.inputBuf, digit, len(t.visible))
	t.inputBuf = buf
	if committed && idx > 0 {
		t.inner.SetCursor(idx - 1)
	}
}

// refilter rebuilds the visible index list using the current
// FilterValue of the selected row (if any) as the cursor anchor. Used
// when the filter text changes; SetItems uses refilterWithKey directly
// because it must capture the key BEFORE swapping allItems.
func (t *Table) refilter(preserveCursor bool) {
	var prevKey string
	if preserveCursor {
		if prev := t.SelectedItem(); prev != nil {
			prevKey = prev.FilterValue()
		}
	}
	t.refilterWithKey(prevKey)
}

// refilterWithKey rebuilds visible/rows and restores the cursor onto
// the row whose FilterValue equals prevKey, if any. An empty prevKey
// means "don't try to preserve" — the cursor lands on row 0 unless
// it already points within the new visible range.
func (t *Table) refilterWithKey(prevKey string) {
	q := strings.ToLower(strings.TrimSpace(t.filterIn.Value()))
	t.visible = t.visible[:0]
	for i, it := range t.allItems {
		if q == "" || strings.Contains(strings.ToLower(it.FilterValue()), q) {
			t.visible = append(t.visible, i)
		}
	}

	rows := make([]table.Row, 0, len(t.visible))
	for displayIdx, i := range t.visible {
		cells := t.allItems[i].Cells()
		if t.rowNumbers {
			row := make(table.Row, 0, len(cells)+1)
			row = append(row, strconv.Itoa(displayIdx+1))
			row = append(row, cells...)
			rows = append(rows, row)
		} else {
			rows = append(rows, cells)
		}
	}
	t.inner.SetRows(rows)

	// Identity is matched via FilterValue rather than interface
	// equality: RowProvider implementations may embed structs with
	// non-comparable fields (e.g. kube.PodInfo's []int32 ports), in
	// which case `==` would panic at runtime.
	if prevKey != "" {
		for i, idx := range t.visible {
			if t.allItems[idx].FilterValue() == prevKey {
				t.inner.SetCursor(i)
				return
			}
		}
	}
	// Cursor may be -1 here: bubbles' SetCursor clamps against an empty
	// row set, and the inner table never recovers on its own when rows
	// arrive later — reset it alongside the out-of-range case.
	if c := t.inner.Cursor(); c < 0 || c >= len(t.visible) {
		t.inner.SetCursor(0)
	}
}
